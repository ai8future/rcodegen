package batch

import (
	"context"
	"net"
	"testing"

	"rcodegen/pkg/server/pb"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type sessionTestServer struct {
	pb.UnimplementedRServeServer
	received chan string
}

func (s *sessionTestServer) RunTask(req *pb.RunTaskRequest, stream pb.RServe_RunTaskServer) error {
	s.received <- req.SessionId
	return stream.Send(&pb.RunEvent{Event: &pb.RunEvent_Result{Result: &pb.ResultEvent{
		ExitCode:     0,
		TotalCostUsd: 1.25,
		SessionId:    "session-new",
	}}})
}

func TestRemoteExecutorCarriesSessionIDs(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	grpcServer := grpc.NewServer()
	service := &sessionTestServer{received: make(chan string, 1)}
	pb.RegisterRServeServer(grpcServer, service)
	go func() { _ = grpcServer.Serve(listener) }()
	defer grpcServer.Stop()

	conn, err := grpc.NewClient(
		listener.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	defer conn.Close()

	exec := &RemoteExecutor{conn: conn}
	result, err := exec.Execute(context.Background(), &JobDef{
		Name: "session-job", Tool: "claude", Task: "continue",
	}, "session-old")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := <-service.received; got != "session-old" {
		t.Fatalf("request session_id = %q, want session-old", got)
	}
	if result.SessionID != "session-new" {
		t.Fatalf("result session_id = %q, want session-new", result.SessionID)
	}
	if result.Cost != 1.25 {
		t.Fatalf("result cost = %v, want 1.25", result.Cost)
	}
}
