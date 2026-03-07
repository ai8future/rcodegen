// rserve is a gRPC server that exposes rclaude, rcodex, rgemini, and
// bundle orchestration as streaming RPCs for the web dashboard.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"time"

	"rcodegen/pkg/runner"
	"rcodegen/pkg/server"
	"rcodegen/pkg/server/pb"
	"rcodegen/pkg/settings"
	"rcodegen/pkg/tools/claude"
	"rcodegen/pkg/tools/codex"
	"rcodegen/pkg/tools/gemini"

	chassis "github.com/ai8future/chassis-go/v8"
	"github.com/ai8future/chassis-go/v8/lifecycle"
	"github.com/ai8future/chassis-go/v8/registry"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
)

func main() {
	chassis.RequireMajor(8)

	defaultPort := chassis.Port("rserve", chassis.PortGRPC)
	port := flag.Int("port", defaultPort, "gRPC listen port")
	maxConcurrent := flag.Int("max-concurrent", 3, "max simultaneous runs")
	showVersion := flag.Bool("v", false, "show version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("rserve %s\n", runner.GetVersion())
		os.Exit(0)
	}

	// Load settings (non-interactive: fall back to defaults if missing)
	s, _, err := settings.LoadWithFallback()
	if err != nil {
		log.Fatalf("settings error: %v", err)
	}

	// Tool factories create fresh instances per request to avoid shared mutable state
	toolFactories := map[string]server.ToolFactory{
		"claude": func() runner.Tool { return claude.New() },
		"codex":  func() runner.Tool { return codex.New() },
		"gemini": func() runner.Tool { return gemini.New() },
	}

	runRegistry := server.NewRunRegistry(*maxConcurrent)
	srv := server.NewServer(s, toolFactories, runRegistry)

	// Bind localhost only — use a reverse proxy for remote access
	lis, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", *port))
	if err != nil {
		log.Fatalf("failed to listen on port %d: %v", *port, err)
	}

	grpcServer := grpc.NewServer()
	pb.RegisterRServeServer(grpcServer, srv)
	reflection.Register(grpcServer)

	// Register with chassis registry for operational visibility
	os.Setenv("CHASSIS_SERVICE_NAME", "rserve")
	registry.Port(chassis.PortGRPC, *port, "gRPC API")

	log.Printf("rserve %s listening on 127.0.0.1:%d (max-concurrent=%d)", runner.GetVersion(), *port, *maxConcurrent)

	// lifecycle.Run handles SIGTERM/SIGINT, coordinated shutdown, and registry
	if err := lifecycle.Run(context.Background(), func(ctx context.Context) error {
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
				log.Println("graceful stop timed out, forcing stop")
				grpcServer.Stop()
			}
			return nil
		case err := <-errCh:
			return err
		}
	}); err != nil {
		log.Fatalf("serve error: %v", err)
	}
}
