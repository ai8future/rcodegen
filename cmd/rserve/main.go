// rserve exposes the configured CLI and local API tools plus bundle orchestration
// via gRPC (streaming RPCs) and an OpenAI-compatible HTTP API.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	rcodegenpkg "rcodegen"
	"rcodegen/pkg/runner"
	"rcodegen/pkg/server"
	"rcodegen/pkg/server/openai"
	"rcodegen/pkg/server/pb"
	"rcodegen/pkg/settings"
	"rcodegen/pkg/tools/claude"
	"rcodegen/pkg/tools/codex"
	"rcodegen/pkg/tools/gemini"
	"rcodegen/pkg/tools/kilocode"
	"rcodegen/pkg/tools/localai"
	"rcodegen/pkg/tools/opencode"

	chassis "github.com/ai8future/chassis-go/v11"
	"github.com/ai8future/chassis-go/v11/grpckit"
	"github.com/ai8future/chassis-go/v11/health"
	"github.com/ai8future/chassis-go/v11/kafkakit"
	"github.com/ai8future/chassis-go/v11/lifecycle"
	"github.com/ai8future/chassis-go/v11/logz"
	otelinit "github.com/ai8future/chassis-go/v11/otel"
	"github.com/ai8future/chassis-go/v11/registry"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
)

func main() {
	chassis.SetAppVersion(rcodegenpkg.AppVersion)
	chassis.RequireMajor(11)

	defaultPort := chassis.Port("rserve", chassis.PortGRPC)
	port := flag.Int("port", defaultPort, "gRPC listen port")
	bind := flag.String("bind", "127.0.0.1", "listen address (remote binds require RSERVE_ALLOW_INSECURE_REMOTE=1)")
	maxConcurrent := flag.Int("max-concurrent", 3, "max simultaneous runs")
	sessionTTL := flag.Int("session-ttl", 30, "session TTL in minutes (0 = no expiry)")
	showVersion := flag.Bool("v", false, "show version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("rserve version %s\n", rcodegenpkg.AppVersion)
		os.Exit(0)
	}

	logger := logz.New("info")
	// One token guards both protocols. The HTTP handler reads it for itself; the
	// gRPC interceptors are only installed when it is set, so an unset token
	// leaves gRPC exactly as it was.
	authToken := os.Getenv("RSERVE_TOKEN")
	allowInsecureRemote := os.Getenv("RSERVE_ALLOW_INSECURE_REMOTE") == "1"
	if err := validateBindAddress(*bind, allowInsecureRemote); err != nil {
		logger.Error("unsafe bind refused", "bind", *bind, "error", err)
		os.Exit(1)
	}
	if !isLoopbackBind(*bind) {
		if authToken != "" {
			logger.Warn("remote bind explicitly enabled; gRPC and HTTP require a bearer token from non-loopback peers, but both listeners are plaintext (no TLS) so the token travels in the clear", "bind", *bind)
		} else {
			logger.Warn("remote bind explicitly enabled; native gRPC is unauthenticated and both listeners are plaintext", "bind", *bind)
		}
	}

	// --- chassis: OTel initialization (before creating interceptors/metrics) ---
	var shutdownOtel otelinit.ShutdownFunc
	if endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"); endpoint != "" {
		shutdownOtel = otelinit.Init(otelinit.Config{
			ServiceName:    "rserve",
			ServiceVersion: rcodegenpkg.AppVersion,
			Endpoint:       endpoint,
			Insecure:       true,
		})
		logger.Info("OpenTelemetry initialized", "endpoint", endpoint)
	}

	// Load settings (non-interactive: fall back to defaults if missing)
	s, _, err := settings.LoadWithFallback()
	if err != nil {
		logger.Error("settings error", "error", err)
		os.Exit(1)
	}

	// Tool factories create fresh instances per request to avoid shared mutable state
	toolFactories := newToolFactories()

	// Async admission limits are read once, here, and refused loudly if they are
	// unusable: a limit that silently parsed to zero would disable the bound it
	// was set to tighten.
	asyncLimits, err := asyncLimitsFromEnv(*maxConcurrent)
	if err != nil {
		logger.Error("invalid async admission configuration", "error", err)
		os.Exit(1)
	}

	runRegistry := server.NewRunRegistry(*maxConcurrent)
	sessionStore := server.NewSessionStore(time.Duration(*sessionTTL) * time.Minute)
	srv := server.NewServer(s, toolFactories, runRegistry, sessionStore)

	lis, err := net.Listen("tcp", fmt.Sprintf("%s:%d", *bind, *port))
	if err != nil {
		logger.Error("failed to listen", "port", *port, "error", err)
		os.Exit(1)
	}

	// Sweep orphaned clone scratch dirs now: after the port is bound, so holding
	// it proves no other rserve is running and every leftover is genuinely
	// stale, and before anything is served, so no live run's dir can be caught.
	// Sweeping before the bind would let a second, doomed process delete the
	// running instance's scratch dirs on its way to failing.
	sweptClones := openai.SweepOrphanedClones(os.TempDir(), logger)
	logger.Info("swept orphaned work_dir clones", "dir", os.TempDir(), "removed", sweptClones)

	// --- chassis: gRPC server with recovery, logging, tracing, and metrics interceptors ---
	unaryInterceptors := []grpc.UnaryServerInterceptor{
		grpckit.UnaryRecovery(logger),
		grpckit.UnaryTracing(),
		grpckit.UnaryMetrics(),
		grpckit.UnaryLogging(logger),
	}
	streamInterceptors := []grpc.StreamServerInterceptor{
		grpckit.StreamRecovery(logger),
		grpckit.StreamMetrics(),
		grpckit.StreamLogging(logger),
	}
	// Auth goes last in the chain so a rejected call is still traced, counted,
	// and logged: an unauthenticated attempt from the LAN is precisely the event
	// an operator wants to find in the log.
	if authToken != "" {
		auth := server.NewTokenAuth(authToken)
		unaryInterceptors = append(unaryInterceptors, auth.Unary())
		streamInterceptors = append(streamInterceptors, auth.Stream())
	}
	grpcServer := grpc.NewServer(
		grpc.ChainUnaryInterceptor(unaryInterceptors...),
		grpc.ChainStreamInterceptor(streamInterceptors...),
	)
	pb.RegisterRServeServer(grpcServer, srv)
	reflection.Register(grpcServer)

	grpckit.RegisterHealth(grpcServer, health.CheckFunc(map[string]health.Check{
		"self": func(_ context.Context) error { return nil },
	}))

	// File store for upload endpoint (rooted in /tmp/rserve-files)
	fileDir := filepath.Join(os.TempDir(), "rserve-files")
	fileStore, err := openai.NewFileStore(fileDir)
	if err != nil {
		logger.Error("failed to create file store", "dir", fileDir, "error", err)
		os.Exit(1)
	}
	defer fileStore.Stop()

	// Detect available tool CLIs and create OpenAI-compatible HTTP handler
	availableTools := openai.DetectAvailableTools(toolFactories)
	httpHandler := openai.NewHandler(s, toolFactories, runRegistry, availableTools, fileStore, sessionStore,
		openai.WithAsyncLimits(asyncLimits))
	httpPort := *port + 1

	// An async run ID is cancellable through either protocol, and only the async
	// store can end one: gRPC CancelRun asks it before the registry.
	srv.SetAsyncCanceller(httpHandler)

	// --- chassis: kafkakit publisher (optional — enabled when KAFKAKIT_BOOTSTRAP_SERVERS is set) ---
	var pub *kafkakit.Publisher
	kafkaCfg := kafkakit.Config{
		BootstrapServers: os.Getenv("KAFKAKIT_BOOTSTRAP_SERVERS"),
		Source:           "rserve",
		TenantID:         os.Getenv("KAFKAKIT_TENANT_ID"),
	}
	if kafkaCfg.TenantID == "" {
		kafkaCfg.TenantID = "ai8"
	}
	if kafkaCfg.Enabled() {
		pub, err = kafkakit.NewPublisher(kafkaCfg)
		if err != nil {
			logger.Warn("kafkakit publisher initialization failed (non-fatal)", "error", err)
		} else {
			logger.Info("kafkakit publisher initialized")
		}
	}

	// Register with chassis registry for operational visibility
	os.Setenv("CHASSIS_SERVICE_NAME", "rserve")
	registry.Port(chassis.PortGRPC, *port, "gRPC API")
	registry.Port(chassis.PortHTTP, httpPort, "OpenAI-compatible HTTP API")

	logger.Info("rserve starting",
		"version", rcodegenpkg.AppVersion,
		"bind", *bind,
		"grpc_port", *port,
		"http_port", httpPort,
		"max_concurrent", *maxConcurrent,
		"session_ttl_min", *sessionTTL,
		"available_tools", availableTools,
		"async_max_live", asyncLimits.MaxLive,
		"async_max_bytes", asyncLimits.MaxBytes,
	)

	// Build lifecycle components.
	components := []any{
		// gRPC server component
		func(ctx context.Context) error {
			errCh := make(chan error, 1)
			go func() { errCh <- grpcServer.Serve(lis) }()
			select {
			case <-ctx.Done():
				done := make(chan struct{})
				go func() {
					grpcServer.GracefulStop()
					close(done)
				}()
				select {
				case <-done:
				case <-time.After(30 * time.Second):
					logger.Warn("graceful stop timed out, forcing stop")
					grpcServer.Stop()
				}
				// Shutdown kafkakit publisher
				if pub != nil {
					pub.Close()
				}
				// Shutdown OTel
				if shutdownOtel != nil {
					shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer shutCancel()
					if err := shutdownOtel(shutCtx); err != nil {
						logger.Warn("OTel shutdown error", "error", err)
					}
				}
				return nil
			case err := <-errCh:
				return err
			}
		},
		// OpenAI-compatible HTTP server on port+1
		func(ctx context.Context) error {
			httpServer := &http.Server{
				Addr:    fmt.Sprintf("%s:%d", *bind, httpPort),
				Handler: httpHandler,
			}
			errCh := make(chan error, 1)
			go func() { errCh <- httpServer.ListenAndServe() }()
			select {
			case <-ctx.Done():
				shutCtx, shutCancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer shutCancel()
				err := httpServer.Shutdown(shutCtx)
				// Async runs have no connection left to fail on, so they are
				// told the server is going away through their callbacks —
				// best-effort, and bounded so a dead receiver cannot hold the
				// exit open.
				asyncCtx, asyncCancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer asyncCancel()
				httpHandler.Shutdown(asyncCtx)
				return err
			case err := <-errCh:
				if err == http.ErrServerClosed {
					return nil
				}
				return err
			}
		},
	}

	// lifecycle.Run handles SIGTERM/SIGINT, coordinated shutdown, and registry
	lifecycleArgs := []any{lifecycle.WithKafkaConfig(kafkaCfg)}
	for _, c := range components {
		lifecycleArgs = append(lifecycleArgs, c)
	}
	if err := lifecycle.Run(context.Background(), lifecycleArgs...); err != nil {
		logger.Error("serve error", "error", err)
		os.Exit(1)
	}
}

func newToolFactories() map[string]server.ToolFactory {
	return map[string]server.ToolFactory{
		"claude":   func() runner.Tool { return claude.New() },
		"codex":    func() runner.Tool { return codex.New() },
		"gemini":   func() runner.Tool { return gemini.New() },
		"kilocode": func() runner.Tool { return kilocode.New() },
		"opencode": func() runner.Tool { return opencode.New() },
		"ollama":   func() runner.Tool { return localai.NewOllama() },
		"lmstudio": func() runner.Tool { return localai.NewLMStudio() },
	}
}

// asyncLimitsFromEnv resolves the async admission bounds: the defaults for this
// server's slot count, with RSERVE_ASYNC_MAX_LIVE and RSERVE_ASYNC_MAX_BYTES
// overriding them.
//
// A value that does not parse, or that is not positive, is an error rather than
// a fallback. Both variables exist to bound memory, and the failure mode of
// treating "0" or "sixty-four" as "unset" is a server that runs without the
// bound its operator believed they had set.
func asyncLimitsFromEnv(maxConcurrent int) (openai.AsyncLimits, error) {
	limits := openai.DefaultAsyncLimits(maxConcurrent)
	if raw := strings.TrimSpace(os.Getenv("RSERVE_ASYNC_MAX_LIVE")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			return limits, fmt.Errorf("RSERVE_ASYNC_MAX_LIVE must be a positive integer, got %q", raw)
		}
		limits.MaxLive = n
	}
	if raw := strings.TrimSpace(os.Getenv("RSERVE_ASYNC_MAX_BYTES")); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n <= 0 {
			return limits, fmt.Errorf("RSERVE_ASYNC_MAX_BYTES must be a positive integer, got %q", raw)
		}
		limits.MaxBytes = n
	}
	return limits, nil
}

func validateBindAddress(address string, allowInsecureRemote bool) error {
	if isLoopbackBind(address) || allowInsecureRemote {
		return nil
	}
	return fmt.Errorf("non-loopback bind requires RSERVE_ALLOW_INSECURE_REMOTE=1; prefer loopback behind authenticated TLS transport")
}

func isLoopbackBind(address string) bool {
	host := strings.TrimSpace(address)
	host = strings.TrimPrefix(host, "[")
	host = strings.TrimSuffix(host, "]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// publishRunCompleted publishes a codegen run.completed event to the event bus.
// Exported for use by the gRPC server handlers if needed.
func publishRunCompleted(ctx context.Context, pub *kafkakit.Publisher, logger *slog.Logger, data map[string]any) {
	if pub == nil {
		return
	}
	if err := pub.Publish(ctx, "ai8.codegen.run.completed", data); err != nil {
		logger.Warn("failed to publish event", "error", err, "subject", "ai8.codegen.run.completed")
	}
}
