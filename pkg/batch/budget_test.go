package batch

import (
	"context"
	"testing"
)

// fakeExecutor implements Executor for testing budget checks.
type fakeExecutor struct {
	remaining int
	err       error
}

func (f *fakeExecutor) Execute(_ context.Context, _ *JobDef, _ string) (*JobResult, error) {
	return nil, nil
}

func (f *fakeExecutor) CheckBudget(_ context.Context) (int, error) {
	return f.remaining, f.err
}

func TestBudgetCheckerOK(t *testing.T) {
	bc := &BudgetChecker{
		Config: BudgetConfig{
			ThresholdPct: 5,
			OnBudget:     "stop",
		},
		Executor: &fakeExecutor{remaining: 80},
	}

	action, err := bc.Check(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if action != BudgetContinue {
		t.Fatalf("expected BudgetContinue, got %v", action)
	}
}

func TestBudgetCheckerThresholdHit(t *testing.T) {
	bc := &BudgetChecker{
		Config: BudgetConfig{
			ThresholdPct: 5,
			OnBudget:     "stop",
		},
		Executor: &fakeExecutor{remaining: 3},
	}

	action, err := bc.Check(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if action != BudgetStop {
		t.Fatalf("expected BudgetStop, got %v", action)
	}
}

func TestBudgetCheckerWait(t *testing.T) {
	bc := &BudgetChecker{
		Config: BudgetConfig{
			ThresholdPct: 5,
			OnBudget:     "wait",
		},
		Executor: &fakeExecutor{remaining: 2},
	}

	action, err := bc.Check(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if action != BudgetWait {
		t.Fatalf("expected BudgetWait, got %v", action)
	}
}

func TestBudgetCheckerUnavailable(t *testing.T) {
	bc := &BudgetChecker{
		Config: BudgetConfig{
			ThresholdPct: 5,
			OnBudget:     "stop",
		},
		Executor: &fakeExecutor{remaining: -1},
	}

	action, err := bc.Check(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if action != BudgetContinue {
		t.Fatalf("expected BudgetContinue when budget unavailable, got %v", action)
	}
}
