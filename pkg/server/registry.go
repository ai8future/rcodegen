// Package server implements the rserve gRPC API for programmatic
// access to rclaude, rcodex, rgemini, and bundle orchestration.
package server

import (
	"context"
	"crypto/rand"
	"fmt"
	"sync"
	"time"
)

// RunRegistry manages active runs with concurrency control.
type RunRegistry struct {
	mu            sync.Mutex
	runs          map[string]*ActiveRun
	maxConcurrent int
	semaphore     chan struct{}
}

// ActiveRun tracks a single in-flight task or bundle execution.
type ActiveRun struct {
	ID        string
	Tool      string
	Task      string
	StartedAt time.Time
	Cancel    context.CancelFunc
	// CorrelationID is the caller's own identifier for this run (a Windmill
	// job UUID, say), recorded so status output can name the external run that
	// owns each slot. Empty when the caller supplied none.
	CorrelationID string
}

// AcquireOptions carries the optional per-run metadata Acquire does not need.
type AcquireOptions struct {
	// CorrelationID is stored on the run entry as-is; callers sanitize
	// externally supplied values before passing them.
	CorrelationID string
}

// Acquisition is a held run slot: the run's ID, its cancellable context, and
// the cancel func. The caller must Release the ID when the run ends.
type Acquisition struct {
	RunID  string
	Ctx    context.Context
	Cancel context.CancelFunc
}

// NewRunRegistry creates a registry that allows up to max concurrent runs.
func NewRunRegistry(max int) *RunRegistry {
	return &RunRegistry{
		runs:          make(map[string]*ActiveRun),
		maxConcurrent: max,
		semaphore:     make(chan struct{}, max),
	}
}

// Acquire blocks until a concurrency slot is available (or ctx is cancelled).
// Returns a unique run ID and a cancel function for the run's context.
func (rr *RunRegistry) Acquire(ctx context.Context, tool, task string) (string, context.Context, context.CancelFunc, error) {
	a, err := rr.AcquireWith(ctx, tool, task, AcquireOptions{})
	if err != nil {
		return "", nil, nil, err
	}
	return a.RunID, a.Ctx, a.Cancel, nil
}

// AcquireWith is Acquire with per-run metadata attached to the registry entry.
func (rr *RunRegistry) AcquireWith(ctx context.Context, tool, task string, opts AcquireOptions) (*Acquisition, error) {
	// Block until a slot opens up
	select {
	case rr.semaphore <- struct{}{}:
		// got a slot
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	runCtx, cancel := context.WithCancel(ctx)
	runID := generateRunID()

	rr.mu.Lock()
	rr.runs[runID] = &ActiveRun{
		ID:            runID,
		Tool:          tool,
		Task:          task,
		StartedAt:     time.Now(),
		Cancel:        cancel,
		CorrelationID: opts.CorrelationID,
	}
	rr.mu.Unlock()

	return &Acquisition{RunID: runID, Ctx: runCtx, Cancel: cancel}, nil
}

// Release frees the concurrency slot for a completed run.
func (rr *RunRegistry) Release(runID string) {
	rr.mu.Lock()
	delete(rr.runs, runID)
	rr.mu.Unlock()

	// Free the semaphore slot
	<-rr.semaphore
}

// Cancel cancels a running task by its run ID. Returns true if found.
func (rr *RunRegistry) Cancel(runID string) bool {
	rr.mu.Lock()
	run, ok := rr.runs[runID]
	rr.mu.Unlock()

	if !ok {
		return false
	}
	run.Cancel()
	return true
}

// List returns a snapshot of all active runs (copies, safe to read without locks).
func (rr *RunRegistry) List() []*ActiveRun {
	rr.mu.Lock()
	defer rr.mu.Unlock()

	result := make([]*ActiveRun, 0, len(rr.runs))
	for _, r := range rr.runs {
		cp := *r
		cp.Cancel = nil // Don't expose cancel func
		result = append(result, &cp)
	}
	return result
}

// ActiveCount returns the number of currently active runs.
func (rr *RunRegistry) ActiveCount() int {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	return len(rr.runs)
}

// MaxConcurrent returns the configured maximum concurrent runs.
func (rr *RunRegistry) MaxConcurrent() int {
	return rr.maxConcurrent
}

// generateRunID produces a short unique hex ID.
func generateRunID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return fmt.Sprintf("%x", b)
}
