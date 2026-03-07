package batch

import (
	"context"
	"testing"
)

// mockExecutor is a test double that implements the Executor interface.
type mockExecutor struct {
	executeResult  *JobResult
	executeErr     error
	budgetRemaining int
	budgetErr      error
}

func (m *mockExecutor) Execute(ctx context.Context, job *JobDef, sessionID string) (*JobResult, error) {
	if m.executeErr != nil {
		return nil, m.executeErr
	}
	// Copy the result and stamp the sessionID from the call.
	r := *m.executeResult
	r.SessionID = sessionID
	return &r, nil
}

func (m *mockExecutor) CheckBudget(ctx context.Context) (int, error) {
	return m.budgetRemaining, m.budgetErr
}

// TestExecutorInterface verifies that a mock satisfying Executor can execute
// a job and return a result with ExitCode=0 and the correct SessionID.
func TestExecutorInterface(t *testing.T) {
	var exec Executor = &mockExecutor{
		executeResult: &JobResult{
			ExitCode: 0,
			Cost:     1.25,
			Duration: "3.5s",
		},
	}

	job := &JobDef{
		Name: "test-job",
		Task: "say hello",
		Tool: "claude",
	}

	result, err := exec.Execute(context.Background(), job, "sess-abc-123")
	if err != nil {
		t.Fatalf("Execute returned unexpected error: %v", err)
	}
	if result.ExitCode != 0 {
		t.Errorf("expected ExitCode=0, got %d", result.ExitCode)
	}
	if result.SessionID != "sess-abc-123" {
		t.Errorf("expected SessionID=%q, got %q", "sess-abc-123", result.SessionID)
	}
	if result.Cost != 1.25 {
		t.Errorf("expected Cost=1.25, got %f", result.Cost)
	}
}

// TestExecutorBudgetCheck verifies that CheckBudget returns the expected
// remaining percentage from the mock.
func TestExecutorBudgetCheck(t *testing.T) {
	var exec Executor = &mockExecutor{
		budgetRemaining: 80,
	}

	remaining, err := exec.CheckBudget(context.Background())
	if err != nil {
		t.Fatalf("CheckBudget returned unexpected error: %v", err)
	}
	if remaining != 80 {
		t.Errorf("expected remainingPct=80, got %d", remaining)
	}
}
