package batch

import (
	"context"
	"fmt"
)

// BudgetAction represents the action to take based on budget status.
type BudgetAction int

const (
	// BudgetContinue means budget is fine, keep running jobs.
	BudgetContinue BudgetAction = iota
	// BudgetStop means budget threshold hit and policy is to stop.
	BudgetStop
	// BudgetWait means budget threshold hit and policy is to wait for replenishment.
	BudgetWait
	// BudgetAsk means budget threshold hit and policy is to ask the user.
	BudgetAsk
)

// String returns a human-readable name for the BudgetAction.
func (a BudgetAction) String() string {
	switch a {
	case BudgetContinue:
		return "continue"
	case BudgetStop:
		return "stop"
	case BudgetWait:
		return "wait"
	case BudgetAsk:
		return "ask"
	default:
		return fmt.Sprintf("BudgetAction(%d)", int(a))
	}
}

// BudgetChecker evaluates the current budget against the configured threshold
// and returns the appropriate action.
type BudgetChecker struct {
	Config   BudgetConfig
	Executor Executor
}

// Check queries the executor for remaining budget and decides what to do.
//
// Rules:
//   - If ThresholdPct <= 0, budget checking is disabled; return Continue.
//   - If the executor returns remaining < 0 (budget info unavailable), return Continue
//     so that jobs are not blocked by missing data.
//   - If remaining <= ThresholdPct, return the action dictated by Config.OnBudget.
//   - Otherwise return Continue.
func (bc *BudgetChecker) Check(ctx context.Context) (BudgetAction, error) {
	// Budget checking disabled if no threshold configured.
	if bc.Config.ThresholdPct <= 0 {
		return BudgetContinue, nil
	}

	remaining, err := bc.Executor.CheckBudget(ctx)
	if err != nil {
		return BudgetContinue, fmt.Errorf("checking budget: %w", err)
	}

	// Negative means budget info is unavailable; don't block.
	if remaining < 0 {
		return BudgetContinue, nil
	}

	// Budget is above threshold; all clear.
	if remaining > bc.Config.ThresholdPct {
		return BudgetContinue, nil
	}

	// Threshold breached — pick action from config.
	switch bc.Config.OnBudget {
	case "stop":
		return BudgetStop, nil
	case "wait":
		return BudgetWait, nil
	case "ask":
		return BudgetAsk, nil
	default:
		// Defensive: treat unknown policy as stop.
		return BudgetStop, nil
	}
}
