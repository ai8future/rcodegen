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

// A caller's own identifier is recorded on the run entry. The proto has no
// field for it, so GetStatus renders it into the task string — the form
// operators have always seen for bundle runs.
func TestGetStatus_RendersCorrelationIDIntoTheTaskString(t *testing.T) {
	reg := NewRunRegistry(2)
	s := NewServer(nil, nil, reg, nil)

	correlated, err := reg.AcquireWith(context.Background(), "bundle", "research-report",
		AcquireOptions{CorrelationID: "wm-job-1"})
	if err != nil {
		t.Fatalf("AcquireWith: %v", err)
	}
	defer func() {
		correlated.Cancel()
		reg.Release(correlated.RunID)
	}()

	plainID, _, plainCancel, err := reg.Acquire(context.Background(), "claude", "audit this")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer func() {
		plainCancel()
		reg.Release(plainID)
	}()

	resp, err := s.GetStatus(context.Background(), &pb.GetStatusRequest{})
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	tasks := make(map[string]string, len(resp.Runs))
	for _, run := range resp.Runs {
		tasks[run.RunId] = run.Task
	}
	if got := tasks[correlated.RunID]; got != "research-report corr=wm-job-1" {
		t.Errorf("correlated task = %q, want %q", got, "research-report corr=wm-job-1")
	}
	if got := tasks[plainID]; got != "audit this" {
		t.Errorf("uncorrelated task = %q, want %q", got, "audit this")
	}
}

// waitForQueuedCount blocks until the registry reports want waiters.
func waitForQueuedCount(t *testing.T, reg *RunRegistry, want int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if reg.QueuedCount() == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("queued count never reached %d (currently %d)", want, reg.QueuedCount())
}

// A saturated registry tells each waiter where it stands and how long it
// waited — the difference between "queued behind other work" and "running
// slowly", which look identical from outside.
func TestAcquireWith_ReportsQueuePositionAndWait(t *testing.T) {
	reg := NewRunRegistry(2)

	var held []*Acquisition
	for i := 0; i < 2; i++ {
		a, err := reg.AcquireWith(context.Background(), "claude", "held", AcquireOptions{})
		if err != nil {
			t.Fatalf("hold slot %d: %v", i, err)
		}
		held = append(held, a)
	}
	if got := reg.QueuedCount(); got != 0 {
		t.Fatalf("queued = %d before any waiter, want 0", got)
	}

	type outcome struct {
		acq *Acquisition
		err error
	}
	startWaiter := func(name string) (<-chan int, <-chan outcome) {
		positions := make(chan int, 1)
		results := make(chan outcome, 1)
		go func() {
			acq, err := reg.AcquireWith(context.Background(), "claude", name, AcquireOptions{
				OnQueued: func(position int) { positions <- position },
			})
			results <- outcome{acq, err}
		}()
		return positions, results
	}
	awaitPosition := func(positions <-chan int) int {
		t.Helper()
		select {
		case p := <-positions:
			return p
		case <-time.After(10 * time.Second):
			t.Fatal("waiter was never told it was queued")
			return 0
		}
	}

	firstPos, firstResult := startWaiter("first")
	if p := awaitPosition(firstPos); p != 1 {
		t.Errorf("first waiter position = %d, want 1", p)
	}
	secondPos, secondResult := startWaiter("second")
	if p := awaitPosition(secondPos); p != 2 {
		t.Errorf("second waiter position = %d, want 2", p)
	}
	if got := reg.QueuedCount(); got != 2 {
		t.Errorf("queued = %d with two waiters, want 2", got)
	}

	for _, a := range held {
		a.Cancel()
		reg.Release(a.RunID)
	}

	for _, results := range []<-chan outcome{firstResult, secondResult} {
		select {
		case got := <-results:
			if got.err != nil {
				t.Fatalf("waiter: %v", got.err)
			}
			if got.acq.QueueWait <= 0 {
				t.Errorf("QueueWait = %v after waiting for a slot, want > 0", got.acq.QueueWait)
			}
			got.acq.Cancel()
			reg.Release(got.acq.RunID)
		case <-time.After(10 * time.Second):
			t.Fatal("waiter never acquired a slot after one freed")
		}
	}
	waitForQueuedCount(t, reg, 0)
}

// A slot that is free is taken without announcing a wait that never happened.
func TestAcquireWith_NoQueueEventWhenASlotIsFree(t *testing.T) {
	reg := NewRunRegistry(1)
	queued := false
	acq, err := reg.AcquireWith(context.Background(), "claude", "immediate", AcquireOptions{
		OnQueued: func(int) { queued = true },
	})
	if err != nil {
		t.Fatalf("AcquireWith: %v", err)
	}
	defer func() {
		acq.Cancel()
		reg.Release(acq.RunID)
	}()

	if queued {
		t.Error("OnQueued fired for a request that never waited")
	}
	if acq.QueueWait != 0 {
		t.Errorf("QueueWait = %v with a free slot, want 0", acq.QueueWait)
	}
}

// A client that gives up while queued must not be counted as waiting forever.
func TestAcquireWith_CancelledWaiterLeavesTheQueue(t *testing.T) {
	reg := NewRunRegistry(1)
	held, err := reg.AcquireWith(context.Background(), "claude", "held", AcquireOptions{})
	if err != nil {
		t.Fatalf("hold slot: %v", err)
	}
	defer func() {
		held.Cancel()
		reg.Release(held.RunID)
	}()

	ctx, cancel := context.WithCancel(context.Background())
	errs := make(chan error, 1)
	go func() {
		_, err := reg.AcquireWith(ctx, "claude", "gives up", AcquireOptions{})
		errs <- err
	}()
	waitForQueuedCount(t, reg, 1)

	cancel()
	select {
	case err := <-errs:
		if err == nil {
			t.Fatal("cancelled waiter acquired a slot")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cancelled waiter never returned")
	}
	waitForQueuedCount(t, reg, 0)
}
