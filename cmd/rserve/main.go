// rserve is a gRPC server that exposes rclaude, rcodex, rgemini, and
// bundle orchestration as streaming RPCs for the web dashboard.
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	"rcodegen/pkg/runner"
	"rcodegen/pkg/server"
	"rcodegen/pkg/server/pb"
	"rcodegen/pkg/settings"
	"rcodegen/pkg/tools/claude"
	"rcodegen/pkg/tools/codex"
	"rcodegen/pkg/tools/gemini"

	chassis "github.com/ai8future/chassis-go/v5"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
)

func main() {
	chassis.RequireMajor(5)

	port := flag.Int("port", 26147, "gRPC listen port")
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

	// Build tool registry
	tools := map[string]runner.Tool{
		"claude": claude.New(),
		"codex":  codex.New(),
		"gemini": gemini.New(),
	}

	registry := server.NewRunRegistry(*maxConcurrent)
	srv := server.NewServer(s, tools, registry)

	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", *port))
	if err != nil {
		log.Fatalf("failed to listen on port %d: %v", *port, err)
	}

	grpcServer := grpc.NewServer()
	pb.RegisterRServeServer(grpcServer, srv)
	reflection.Register(grpcServer)

	// Graceful shutdown on SIGTERM/SIGINT
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	go func() {
		<-sigCh
		log.Println("shutting down gRPC server...")
		grpcServer.GracefulStop()
	}()

	log.Printf("rserve %s listening on :%d (max-concurrent=%d)", runner.GetVersion(), *port, *maxConcurrent)
	if err := grpcServer.Serve(lis); err != nil {
		log.Fatalf("serve error: %v", err)
	}
}
