package batch

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// countingExecutor is a mock Executor for testing the BatchRunner.
type countingExecutor struct {
	calls     atomic.Int32
	remaining int
	delay     time.Duration
}

func (e *countingExecutor) Execute(ctx context.Context, job *JobDef, sessionID string) (*JobResult, error) {
	if e.delay > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(e.delay):
		}
	}
	e.calls.Add(1)
	return &JobResult{
		ExitCode:  0,
		Cost:      0.01,
		Duration:  "1s",
		SessionID: "sess-" + job.Name,
	}, nil
}

func (e *countingExecutor) CheckBudget(ctx context.Context) (int, error) {
	return e.remaining, nil
}

func TestBatchRunnerBasic(t *testing.T) {
	exec := &countingExecutor{remaining: 100}

	m := &Manifest{
		Name:        "basic-test",
		Concurrency: 2,
		Budget:      BudgetConfig{OnBudget: "stop", CheckInterval: "1s", MaxWait: "1s"},
		Jobs: []JobDef{
			{Name: "job-a", Task: "do A", Tool: "claude"},
			{Name: "job-b", Task: "do B", Tool: "claude"},
		},
	}

	runner := NewBatchRunner(m, exec)

	var mu sync.Mutex
	var events []BatchEvent
	runner.OnEvent = func(e BatchEvent) {
		mu.Lock()
		events = append(events, e)
		mu.Unlock()
	}

	result := runner.Run(context.Background())

	if result.JobsTotal != 2 {
		t.Errorf("expected JobsTotal=2, got %d", result.JobsTotal)
	}
	if result.JobsSucceeded != 2 {
		t.Errorf("expected JobsSucceeded=2, got %d", result.JobsSucceeded)
	}
	if result.JobsFailed != 0 {
		t.Errorf("expected JobsFailed=0, got %d", result.JobsFailed)
	}
	if got := int(exec.calls.Load()); got != 2 {
		t.Errorf("expected executor called 2 times, got %d", got)
	}
	if result.Status != "completed" {
		t.Errorf("expected status=completed, got %s", result.Status)
	}

	// Verify events were emitted.
	mu.Lock()
	eventCount := len(events)
	mu.Unlock()
	if eventCount == 0 {
		t.Error("expected at least one event, got none")
	}

	// Verify total cost accumulated.
	expectedCost := 0.02
	if result.TotalCost < expectedCost-0.001 || result.TotalCost > expectedCost+0.001 {
		t.Errorf("expected TotalCost ~%.2f, got %.4f", expectedCost, result.TotalCost)
	}
}

func TestBatchRunnerSessionChain(t *testing.T) {
	exec := &countingExecutor{remaining: 100}

	m := &Manifest{
		Name:        "chain-test",
		Concurrency: 2,
		Budget:      BudgetConfig{OnBudget: "stop", CheckInterval: "1s", MaxWait: "1s"},
		Jobs: []JobDef{
			{Name: "chain-1", Task: "step 1", Tool: "claude", Session: "chain"},
			{Name: "chain-2", Task: "step 2", Tool: "claude", Session: "chain"},
		},
	}

	runner := NewBatchRunner(m, exec)

	var mu sync.Mutex
	var completedJobs []string
	runner.OnEvent = func(e BatchEvent) {
		if e.Type == "job_complete" {
			mu.Lock()
			completedJobs = append(completedJobs, e.JobName)
			mu.Unlock()
		}
	}

	result := runner.Run(context.Background())

	if result.JobsSucceeded != 2 {
		t.Errorf("expected JobsSucceeded=2, got %d", result.JobsSucceeded)
	}
	if result.JobsFailed != 0 {
		t.Errorf("expected JobsFailed=0, got %d", result.JobsFailed)
	}

	// Both jobs in the session chain should complete.
	mu.Lock()
	completedCopy := make([]string, len(completedJobs))
	copy(completedCopy, completedJobs)
	mu.Unlock()

	if len(completedCopy) != 2 {
		t.Errorf("expected 2 completed jobs, got %d: %v", len(completedCopy), completedCopy)
	}

	// They should be in order (chain-1 before chain-2) since they share a session.
	if len(completedCopy) == 2 {
		if completedCopy[0] != "chain-1" || completedCopy[1] != "chain-2" {
			t.Errorf("expected jobs in order [chain-1, chain-2], got %v", completedCopy)
		}
	}

	if got := int(exec.calls.Load()); got != 2 {
		t.Errorf("expected executor called 2 times, got %d", got)
	}

	fmt.Printf("session chain result: status=%s succeeded=%d failed=%d\n",
		result.Status, result.JobsSucceeded, result.JobsFailed)
}

func TestBatchRunnerCancellation(t *testing.T) {
	exec := &countingExecutor{remaining: 100, delay: 100 * time.Millisecond}

	m := &Manifest{
		Name:        "cancel-test",
		Concurrency: 1, // sequential so timing is predictable
		Budget:      BudgetConfig{OnBudget: "stop", CheckInterval: "1s", MaxWait: "1s"},
		Jobs: []JobDef{
			{Name: "slow-1", Task: "slow task 1", Tool: "claude"},
			{Name: "slow-2", Task: "slow task 2", Tool: "claude"},
			{Name: "slow-3", Task: "slow task 3", Tool: "claude"},
		},
	}

	runner := NewBatchRunner(m, exec)

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	result := runner.Run(ctx)

	completed := int(exec.calls.Load())
	if completed >= 3 {
		t.Errorf("expected fewer than 3 jobs to succeed, got %d", completed)
	}

	fmt.Printf("cancellation result: status=%s succeeded=%d calls=%d\n",
		result.Status, result.JobsSucceeded, completed)
}
