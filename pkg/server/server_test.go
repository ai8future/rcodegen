package server

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"rcodegen/pkg/bundle"
	"rcodegen/pkg/envelope"
	"rcodegen/pkg/runner"
	"rcodegen/pkg/server/pb"
	"rcodegen/pkg/tools/opencode"

	chassis "github.com/ai8future/chassis-go/v11"
	"google.golang.org/grpc/metadata"
)

type bundleTestStream struct {
	ctx context.Context

	mu      sync.Mutex
	events  []*pb.RunEvent
	initRun chan string
}

func TestRunTask_NonStreamToolReturnsStdout(t *testing.T) {
	chassis.RequireMajor(11)
	binDir := t.TempDir()
	script := filepath.Join(binDir, "opencode")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s' 'plain gRPC output'\n"), 0o755); err != nil {
		t.Fatalf("write fake opencode: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	s := NewServer(nil, map[string]ToolFactory{
		"opencode": func() runner.Tool { return opencode.New() },
	}, NewRunRegistry(1), nil)
	stream := newBundleTestStream(context.Background())
	if err := s.RunTask(&pb.RunTaskRequest{
		Tool: "opencode", Task: "hello", WorkDirs: []string{t.TempDir()},
	}, stream); err != nil {
		t.Fatalf("RunTask: %v", err)
	}

	var textOutput, resultOutput string
	for _, ev := range stream.events {
		if ev.GetText() != nil {
			textOutput += ev.GetText().Content
		}
		if ev.GetResult() != nil {
			resultOutput = ev.GetResult().Output
		}
	}
	if textOutput != "plain gRPC output" {
		t.Fatalf("text output = %q, want plain gRPC output", textOutput)
	}
	if resultOutput != "plain gRPC output" {
		t.Fatalf("result output = %q, want plain gRPC output", resultOutput)
	}
}

func newBundleTestStream(ctx context.Context) *bundleTestStream {
	return &bundleTestStream{ctx: ctx, initRun: make(chan string, 1)}
}

func (s *bundleTestStream) Send(ev *pb.RunEvent) error {
	s.mu.Lock()
	s.events = append(s.events, ev)
	s.mu.Unlock()
	if ev.GetInit() != nil {
		select {
		case s.initRun <- ev.RunId:
		default:
		}
	}
	return nil
}

func (s *bundleTestStream) SetHeader(metadata.MD) error  { return nil }
func (s *bundleTestStream) SendHeader(metadata.MD) error { return nil }
func (s *bundleTestStream) SetTrailer(metadata.MD)       {}
func (s *bundleTestStream) Context() context.Context     { return s.ctx }
func (s *bundleTestStream) SendMsg(v any) error {
	ev, ok := v.(*pb.RunEvent)
	if !ok {
		return nil
	}
	return s.Send(ev)
}
func (s *bundleTestStream) RecvMsg(any) error { return io.EOF }

func TestRunBundle_CancelRunCancelsExecutionContext(t *testing.T) {
	streamCtx, cancelStream := context.WithCancel(context.Background())
	defer cancelStream()

	s := NewServer(nil, nil, NewRunRegistry(1), nil)
	runnerStarted := make(chan struct{})
	runnerCancelled := make(chan struct{})
	s.runBundle = func(ctx context.Context, _ *bundle.Bundle, _ map[string]string, _, _ bool) (*envelope.Envelope, error) {
		close(runnerStarted)
		<-ctx.Done()
		close(runnerCancelled)
		return envelope.New().Failure("CANCELLED", ctx.Err().Error()).Build(), ctx.Err()
	}

	stream := newBundleTestStream(streamCtx)
	done := make(chan error, 1)
	go func() {
		done <- s.RunBundle(&pb.RunBundleRequest{Bundle: "ensemble"}, stream)
	}()

	var runID string
	select {
	case runID = <-stream.initRun:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for RunBundle init event")
	}
	select {
	case <-runnerStarted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for bundle runner")
	}

	resp, err := s.CancelRun(context.Background(), &pb.CancelRunRequest{RunId: runID})
	if err != nil {
		t.Fatalf("CancelRun: %v", err)
	}
	if !resp.Cancelled {
		t.Fatalf("CancelRun response = %+v, want cancelled", resp)
	}

	select {
	case <-runnerCancelled:
	case <-time.After(time.Second):
		cancelStream() // ensure the goroutine cannot leak on regression
		<-done
		t.Fatal("CancelRun did not cancel the bundle execution context")
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunBundle: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("RunBundle did not return after cancellation")
	}
}
