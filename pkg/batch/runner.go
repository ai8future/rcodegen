package batch

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/ai8future/chassis-go/v10/registry"
)

// BatchResult summarises the outcome of a complete batch run.
type BatchResult struct {
	Name          string  `json:"name"`
	Status        string  `json:"status"`
	JobsTotal     int     `json:"jobs_total"`
	JobsSucceeded int     `json:"jobs_succeeded"`
	JobsFailed    int     `json:"jobs_failed"`
	TotalCost     float64 `json:"total_cost"`
	TotalDuration string  `json:"total_duration"`
	StopReason    string  `json:"stop_reason,omitempty"`
}

// BatchEvent is emitted during a batch run for progress reporting.
type BatchEvent struct {
	Type      string     `json:"type"` // "job_start","job_complete","job_fail","budget_check","group_start"
	JobName   string     `json:"job_name,omitempty"`
	GroupID   string     `json:"group_id,omitempty"`
	Result    *JobResult `json:"result,omitempty"`
	Remaining int        `json:"remaining,omitempty"`
}

// BatchRunner orchestrates a batch run: it builds session groups, controls
// concurrency via a semaphore, checks budget between groups, and records results.
type BatchRunner struct {
	Manifest *Manifest
	Executor Executor
	Budget   *BudgetChecker
	OnEvent  func(BatchEvent)
}

// NewBatchRunner creates a BatchRunner wired to the given manifest and executor.
func NewBatchRunner(m *Manifest, exec Executor) *BatchRunner {
	return &BatchRunner{
		Manifest: m,
		Executor: exec,
		Budget: &BudgetChecker{
			Config:   m.Budget,
			Executor: exec,
		},
	}
}

// Run executes all jobs described in the manifest, respecting concurrency,
// session ordering, budget limits, and context cancellation.
func (br *BatchRunner) Run(ctx context.Context) *BatchResult {
	start := time.Now()

	result := &BatchResult{
		Name:      br.Manifest.Name,
		Status:    "completed",
		JobsTotal: len(br.Manifest.Jobs),
	}

	groups := BuildSessionGroups(br.Manifest.Jobs)

	sem := make(chan struct{}, br.Manifest.Concurrency)
	var wg sync.WaitGroup
	var mu sync.Mutex

	for _, g := range groups {
		// Check context cancellation before launching each group.
		select {
		case <-ctx.Done():
			mu.Lock()
			result.Status = "cancelled"
			result.StopReason = "context cancelled"
			mu.Unlock()
			goto wait
		default:
		}

		// Check if a graceful stop has been requested via the registry.
		if registry.StopRequested() {
			mu.Lock()
			result.Status = "stopped"
			result.StopReason = "stop requested"
			mu.Unlock()
			goto wait
		}

		// Budget check at group boundaries.
		action, _ := br.Budget.Check(ctx)
		switch action {
		case BudgetStop:
			mu.Lock()
			result.Status = "stopped"
			result.StopReason = "budget threshold"
			mu.Unlock()
			goto wait
		case BudgetWait:
			if !br.waitForBudget(ctx) {
				mu.Lock()
				result.Status = "stopped"
				result.StopReason = "budget wait timeout"
				mu.Unlock()
				goto wait
			}
		case BudgetAsk:
			// In non-interactive mode, treat ask as stop.
			mu.Lock()
			result.Status = "stopped"
			result.StopReason = "budget ask (non-interactive)"
			mu.Unlock()
			goto wait
		}

		br.emit(BatchEvent{Type: "budget_check", Remaining: 100})

		// Acquire semaphore slot (respects cancellation).
		select {
		case <-ctx.Done():
			mu.Lock()
			result.Status = "cancelled"
			result.StopReason = "context cancelled"
			mu.Unlock()
			goto wait
		case sem <- struct{}{}:
		}

		wg.Add(1)
		go func(group SessionGroup) {
			defer func() {
				<-sem
				wg.Done()
			}()
			br.runGroup(ctx, group, &mu, result)
		}(g)
	}

wait:
	wg.Wait()

	result.TotalDuration = time.Since(start).Truncate(time.Millisecond).String()

	// Derive status if not already set to cancelled/stopped.
	mu.Lock()
	if result.Status == "completed" {
		if result.JobsFailed > 0 {
			result.Status = "partial"
		}
	}
	mu.Unlock()

	br.saveCheckpoint(result)

	return result
}

// runGroup executes jobs in a session group sequentially, carrying the
// session ID forward. A failure in one job stops the chain.
func (br *BatchRunner) runGroup(ctx context.Context, group SessionGroup, mu *sync.Mutex, result *BatchResult) {
	br.emit(BatchEvent{Type: "group_start", GroupID: group.ID})

	var sessionID string

	for _, job := range group.Jobs {
		// Check context cancellation before each job.
		select {
		case <-ctx.Done():
			return
		default:
		}

		br.emit(BatchEvent{Type: "job_start", JobName: job.Name, GroupID: group.ID})

		jr, err := br.Executor.Execute(ctx, &job, sessionID)
		if err != nil {
			// Execution error (e.g. context cancelled, process failure).
			if jr == nil {
				jr = &JobResult{ExitCode: 1, Error: err.Error()}
			}
			br.emit(BatchEvent{Type: "job_fail", JobName: job.Name, GroupID: group.ID, Result: jr})
			mu.Lock()
			result.JobsFailed++
			result.TotalCost += jr.Cost
			mu.Unlock()
			return // stop the chain on failure
		}

		if jr.ExitCode != 0 {
			br.emit(BatchEvent{Type: "job_fail", JobName: job.Name, GroupID: group.ID, Result: jr})
			mu.Lock()
			result.JobsFailed++
			result.TotalCost += jr.Cost
			mu.Unlock()
			return // stop the chain on failure
		}

		// Success — carry the session ID forward.
		sessionID = jr.SessionID
		br.emit(BatchEvent{Type: "job_complete", JobName: job.Name, GroupID: group.ID, Result: jr})
		mu.Lock()
		result.JobsSucceeded++
		result.TotalCost += jr.Cost
		mu.Unlock()
	}
}

// waitForBudget polls the executor at CheckInterval until the budget recovers
// or MaxWait expires. Returns true if budget recovered, false on timeout or
// context cancellation.
func (br *BatchRunner) waitForBudget(ctx context.Context) bool {
	interval, err := br.Budget.Config.CheckIntervalDuration()
	if err != nil {
		interval = 3 * time.Minute
	}
	maxWait, err := br.Budget.Config.MaxWaitDuration()
	if err != nil {
		maxWait = 1 * time.Hour
	}

	deadline := time.Now().Add(maxWait)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
			if time.Now().After(deadline) {
				return false
			}
			action, _ := br.Budget.Check(ctx)
			if action == BudgetContinue {
				return true
			}
		}
	}
}

// emit sends a BatchEvent to the OnEvent callback if one is configured.
func (br *BatchRunner) emit(event BatchEvent) {
	if br.OnEvent != nil {
		br.OnEvent(event)
	}
}

// saveCheckpoint persists the current batch result as a checkpoint file.
func (br *BatchRunner) saveCheckpoint(result *BatchResult) {
	name := br.Manifest.Name
	if name == "" {
		name = "unnamed"
	}

	dir, err := BatchDir(name)
	if err != nil {
		return // best-effort
	}

	cp := &Checkpoint{
		Batch:  name,
		Reason: fmt.Sprintf("run %s: %s", result.Status, result.StopReason),
	}

	snap := &QueueSnapshot{
		TotalCost: result.TotalCost,
	}

	_ = cp.Save(dir, snap)
}
