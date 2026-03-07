package batch

import "context"

// Executor runs a single job and returns its result.
type Executor interface {
	Execute(ctx context.Context, job *JobDef, sessionID string) (*JobResult, error)
	CheckBudget(ctx context.Context) (remainingPct int, err error)
}
