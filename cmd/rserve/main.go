// rserve exposes rclaude, rcodex, rgemini, ropencode, and bundle orchestration
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
	bind := flag.String("bind", "127.0.0.1", "bind address (use 0.0.0.0 for all interfaces)")
	maxConcurrent := flag.Int("max-concurrent", 3, "max simultaneous runs")
	sessionTTL := flag.Int("session-ttl", 30, "session TTL in minutes (0 = no expiry)")
	flag.Parse()

	logger := logz.New("info")

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
	toolFactories := map[string]server.ToolFactory{
		"claude":   func() runner.Tool { return claude.New() },
		"codex":    func() runner.Tool { return codex.New() },
		"gemini":   func() runner.Tool { return gemini.New() },
		"opencode": func() runner.Tool { return opencode.New() },
	}

	runRegistry := server.NewRunRegistry(*maxConcurrent)
	sessionStore := server.NewSessionStore(time.Duration(*sessionTTL) * time.Minute)
	srv := server.NewServer(s, toolFactories, runRegistry, sessionStore)

	lis, err := net.Listen("tcp", fmt.Sprintf("%s:%d", *bind, *port))
	if err != nil {
		logger.Error("failed to listen", "port", *port, "error", err)
		os.Exit(1)
	}

	// --- chassis: gRPC server with recovery, logging, tracing, and metrics interceptors ---
	grpcServer := grpc.NewServer(
		grpc.ChainUnaryInterceptor(
			grpckit.UnaryRecovery(logger),
			grpckit.UnaryTracing(),
			grpckit.UnaryMetrics(),
			grpckit.UnaryLogging(logger),
		),
		grpc.ChainStreamInterceptor(
			grpckit.StreamRecovery(logger),
			grpckit.StreamMetrics(),
			grpckit.StreamLogging(logger),
		),
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
	httpHandler := openai.NewHandler(s, toolFactories, runRegistry, availableTools, fileStore, sessionStore)
	httpPort := *port + 1

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
				return httpServer.Shutdown(shutCtx)
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
