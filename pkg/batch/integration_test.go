package batch

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// integrationExecutor is a mock Executor for integration tests.
type integrationExecutor struct {
	calls     atomic.Int32
	remaining int
	delay     time.Duration
}

func (e *integrationExecutor) Execute(ctx context.Context, job *JobDef, sessionID string) (*JobResult, error) {
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

func (e *integrationExecutor) CheckBudget(ctx context.Context) (int, error) {
	return e.remaining, nil
}

// TestIntegrationFullBatchRun exercises the full BatchRunner with 5 jobs:
// 2 in chain-1 (a1, a2), 1 in chain-2 (b1), and 2 standalone (s1, s2).
// Concurrency is 2. We verify completion, cost, events, and chain ordering.
func TestIntegrationFullBatchRun(t *testing.T) {
	exec := &integrationExecutor{remaining: 100, delay: 10 * time.Millisecond}

	m := &Manifest{
		Name:        "integration-full",
		Concurrency: 2,
		Budget:      BudgetConfig{OnBudget: "stop", CheckInterval: "1s", MaxWait: "1s"},
		Jobs: []JobDef{
			{Name: "a1", Task: "chain-1 step 1", Tool: "claude", Session: "chain-1"},
			{Name: "a2", Task: "chain-1 step 2", Tool: "claude", Session: "chain-1"},
			{Name: "b1", Task: "chain-2 step 1", Tool: "claude", Session: "chain-2"},
			{Name: "s1", Task: "standalone 1", Tool: "claude"},
			{Name: "s2", Task: "standalone 2", Tool: "claude"},
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

	// --- Status ---
	if result.Status != "completed" {
		t.Errorf("expected status=completed, got %s (stop_reason=%s)", result.Status, result.StopReason)
	}

	// --- All 5 jobs succeed ---
	if result.JobsTotal != 5 {
		t.Errorf("expected JobsTotal=5, got %d", result.JobsTotal)
	}
	if result.JobsSucceeded != 5 {
		t.Errorf("expected JobsSucceeded=5, got %d", result.JobsSucceeded)
	}
	if result.JobsFailed != 0 {
		t.Errorf("expected JobsFailed=0, got %d", result.JobsFailed)
	}

	// --- Non-zero total cost (5 jobs * 0.01 = 0.05) ---
	if result.TotalCost < 0.001 {
		t.Errorf("expected non-zero TotalCost, got %.4f", result.TotalCost)
	}
	expectedCost := 0.05
	if result.TotalCost < expectedCost-0.001 || result.TotalCost > expectedCost+0.001 {
		t.Errorf("expected TotalCost ~%.2f, got %.4f", expectedCost, result.TotalCost)
	}

	// --- Events were emitted ---
	mu.Lock()
	eventsCopy := make([]BatchEvent, len(events))
	copy(eventsCopy, events)
	mu.Unlock()

	if len(eventsCopy) == 0 {
		t.Fatal("expected events to be emitted, got none")
	}

	// --- Chain ordering: a1 completes before a2 starts ---
	a1CompleteIdx := -1
	a2StartIdx := -1
	for i, ev := range eventsCopy {
		if ev.Type == "job_complete" && ev.JobName == "a1" {
			a1CompleteIdx = i
		}
		if ev.Type == "job_start" && ev.JobName == "a2" {
			a2StartIdx = i
		}
	}

	if a1CompleteIdx == -1 {
		t.Error("did not find job_complete event for a1")
	}
	if a2StartIdx == -1 {
		t.Error("did not find job_start event for a2")
	}
	if a1CompleteIdx != -1 && a2StartIdx != -1 {
		if a1CompleteIdx >= a2StartIdx {
			t.Errorf("chain ordering violated: a1 completed at event index %d but a2 started at index %d",
				a1CompleteIdx, a2StartIdx)
		}
	}

	// --- Verify executor was called exactly 5 times ---
	if got := int(exec.calls.Load()); got != 5 {
		t.Errorf("expected executor called 5 times, got %d", got)
	}
}

// TestIntegrationBudgetStop verifies that when the budget is below the
// threshold and on_budget="stop", the runner stops before executing jobs.
func TestIntegrationBudgetStop(t *testing.T) {
	// remaining=2 which is <= threshold_pct=5, so budget check triggers stop.
	exec := &integrationExecutor{remaining: 2}

	m := &Manifest{
		Name:        "integration-budget-stop",
		Concurrency: 1,
		Budget: BudgetConfig{
			ThresholdPct:  5,
			OnBudget:      "stop",
			CheckInterval: "1s",
			MaxWait:       "1s",
		},
		Jobs: []JobDef{
			{Name: "job-1", Task: "task 1", Tool: "claude"},
			{Name: "job-2", Task: "task 2", Tool: "claude"},
		},
	}

	runner := NewBatchRunner(m, exec)

	result := runner.Run(context.Background())

	// --- Status is "stopped" ---
	if result.Status != "stopped" {
		t.Errorf("expected status=stopped, got %s", result.Status)
	}

	// --- StopReason is "budget threshold" ---
	if result.StopReason != "budget threshold" {
		t.Errorf("expected StopReason=%q, got %q", "budget threshold", result.StopReason)
	}

	// --- No jobs should have executed ---
	if got := int(exec.calls.Load()); got != 0 {
		t.Errorf("expected 0 executor calls (budget stopped before running), got %d", got)
	}
}
