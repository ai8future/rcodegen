// rserve exposes rclaude, rcodex, rgemini, and bundle orchestration
// via gRPC (streaming RPCs) and an OpenAI-compatible HTTP API.
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"rcodegen/pkg/runner"
	"rcodegen/pkg/server"
	"rcodegen/pkg/server/openai"
	"rcodegen/pkg/server/pb"
	"rcodegen/pkg/settings"
	"rcodegen/pkg/tools/claude"
	"rcodegen/pkg/tools/codex"
	"rcodegen/pkg/tools/gemini"

	chassis "github.com/ai8future/chassis-go/v9"
	"github.com/ai8future/chassis-go/v9/grpckit"
	"github.com/ai8future/chassis-go/v9/health"
	"github.com/ai8future/chassis-go/v9/lifecycle"
	"github.com/ai8future/chassis-go/v9/logz"
	"github.com/ai8future/chassis-go/v9/registry"
	"github.com/ai8future/chassis-go/v9/xyops"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
)

func main() {
	chassis.RequireMajor(9)

	defaultPort := chassis.Port("rserve", chassis.PortGRPC)
	port := flag.Int("port", defaultPort, "gRPC listen port")
	bind := flag.String("bind", "127.0.0.1", "bind address (use 0.0.0.0 for all interfaces)")
	maxConcurrent := flag.Int("max-concurrent", 3, "max simultaneous runs")
	showVersion := flag.Bool("v", false, "show version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("rserve %s\n", runner.GetVersion())
		os.Exit(0)
	}

	logger := logz.New("info")

	// Load settings (non-interactive: fall back to defaults if missing)
	s, _, err := settings.LoadWithFallback()
	if err != nil {
		logger.Error("settings error", "error", err)
		os.Exit(1)
	}

	// Tool factories create fresh instances per request to avoid shared mutable state
	toolFactories := map[string]server.ToolFactory{
		"claude": func() runner.Tool { return claude.New() },
		"codex":  func() runner.Tool { return codex.New() },
		"gemini": func() runner.Tool { return gemini.New() },
	}

	runRegistry := server.NewRunRegistry(*maxConcurrent)
	srv := server.NewServer(s, toolFactories, runRegistry)

	lis, err := net.Listen("tcp", fmt.Sprintf("%s:%d", *bind, *port))
	if err != nil {
		logger.Error("failed to listen", "port", *port, "error", err)
		os.Exit(1)
	}

	grpcServer := grpc.NewServer(
		grpc.ChainUnaryInterceptor(
			grpckit.UnaryRecovery(logger),
			grpckit.UnaryLogging(logger),
		),
		grpc.ChainStreamInterceptor(
			grpckit.StreamRecovery(logger),
			grpckit.StreamLogging(logger),
		),
	)
	pb.RegisterRServeServer(grpcServer, srv)
	reflection.Register(grpcServer)

	grpckit.RegisterHealth(grpcServer, health.CheckFunc(map[string]health.Check{
		"self": func(_ context.Context) error { return nil },
	}))

	// Detect available tool CLIs and create OpenAI-compatible HTTP handler
	availableTools := openai.DetectAvailableTools(toolFactories)
	httpHandler := openai.NewHandler(s, toolFactories, runRegistry, availableTools)
	httpPort := *port + 1

	// Register with chassis registry for operational visibility
	os.Setenv("CHASSIS_SERVICE_NAME", "rserve")
	registry.Port(chassis.PortGRPC, *port, "gRPC API")
	registry.Port(chassis.PortHTTP, httpPort, "OpenAI-compatible HTTP API")

	logger.Info("rserve starting",
		"version", runner.GetVersion(),
		"bind", *bind,
		"grpc_port", *port,
		"http_port", httpPort,
		"max_concurrent", *maxConcurrent,
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

	// Optional xyops monitoring bridge — enabled when XYOPS_BASE_URL is set.
	if baseURL := os.Getenv("XYOPS_BASE_URL"); baseURL != "" {
		ops := xyops.New(xyops.Config{
			BaseURL:     baseURL,
			APIKey:      os.Getenv("XYOPS_API_KEY"),
			ServiceName: "rserve",
		}, xyops.WithMonitoring(30))
		components = append(components, ops.Run)
		logger.Info("xyops monitoring enabled", "base_url", baseURL)
	}

	// lifecycle.Run handles SIGTERM/SIGINT, coordinated shutdown, and registry
	if err := lifecycle.Run(context.Background(), components...); err != nil {
		logger.Error("serve error", "error", err)
		os.Exit(1)
	}
}
