package batch

import (
	"context"
	"net"
	"strings"
	"testing"

	"rcodegen/pkg/server/pb"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type sessionTestServer struct {
	pb.UnimplementedRServeServer
	received chan string
	effort   chan string
}

type failureTestServer struct {
	pb.UnimplementedRServeServer
}

func (s *failureTestServer) RunTask(_ *pb.RunTaskRequest, stream pb.RServe_RunTaskServer) error {
	_ = stream.Send(&pb.RunEvent{Event: &pb.RunEvent_Text{Text: &pb.TextEvent{Content: "fallback"}}})
	_ = stream.Send(&pb.RunEvent{Event: &pb.RunEvent_Error{Error: &pb.ErrorEvent{Message: "remote backend failed", Code: 2}}})
	return stream.Send(&pb.RunEvent{Event: &pb.RunEvent_Result{Result: &pb.ResultEvent{
		ExitCode: 2,
		Output:   strings.Repeat("x", 70<<10),
	}}})
}

func (s *sessionTestServer) RunTask(req *pb.RunTaskRequest, stream pb.RServe_RunTaskServer) error {
	s.received <- req.SessionId
	s.effort <- req.Effort
	_ = stream.Send(&pb.RunEvent{Event: &pb.RunEvent_Text{Text: &pb.TextEvent{Content: "fallback must not duplicate"}}})
	return stream.Send(&pb.RunEvent{Event: &pb.RunEvent_Result{Result: &pb.ResultEvent{
		ExitCode:        0,
		TotalCostUsd:    1.25,
		SessionId:       "session-new",
		Output:          "remote output",
		OutputTruncated: true,
	}}})
}

func TestRemoteExecutorCarriesSessionIDs(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	grpcServer := grpc.NewServer()
	service := &sessionTestServer{received: make(chan string, 1), effort: make(chan string, 1)}
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
		Name: "session-job", Tool: "claude", Task: "continue", Effort: "high",
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
	if got := <-service.effort; got != "high" {
		t.Fatalf("request effort = %q, want high", got)
	}
	if result.Output != "remote output" || !result.OutputTruncated {
		t.Fatalf("remote output = %+v", result)
	}
}

func TestRemoteExecutorBoundsFailureOutputAndKeepsError(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	grpcServer := grpc.NewServer()
	pb.RegisterRServeServer(grpcServer, &failureTestServer{})
	go func() { _ = grpcServer.Serve(listener) }()
	defer grpcServer.Stop()
	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	result, err := (&RemoteExecutor{conn: conn}).Execute(context.Background(), &JobDef{Name: "failed", Tool: "ollama", Model: "model", Task: "hi"}, "")
	if err != nil || result.ExitCode != 2 || result.Error != "remote backend failed" || !result.OutputTruncated || len(result.Output) != 64<<10 || strings.Contains(result.Output, "fallback") {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
}
