package batch

import "sync"

// JobState represents the current state of a job in the queue.
type JobState int

const (
	StatePending   JobState = iota
	StateRunning
	StateCompleted
	StateFailed
)

// JobResult holds the outcome of a completed or failed job.
type JobResult struct {
	ExitCode  int     `json:"exit_code"`
	Cost      float64 `json:"cost"`
	Duration  string  `json:"duration"`
	SessionID string  `json:"session_id"`
	Error     string  `json:"error,omitempty"`
}

// CompletedJob is a summary of a successfully completed job.
type CompletedJob struct {
	Name      string  `json:"name"`
	Cost      float64 `json:"cost"`
	Duration  string  `json:"duration"`
	SessionID string  `json:"session_id"`
}

// FailedJob is a summary of a failed job.
type FailedJob struct {
	Name  string `json:"name"`
	Error string `json:"error"`
}

// QueueSnapshot is a serializable representation of queue state,
// suitable for checkpointing. Running jobs are folded back into Pending.
type QueueSnapshot struct {
	Completed []CompletedJob `json:"completed"`
	Failed    []FailedJob    `json:"failed"`
	Pending   []JobDef       `json:"pending"`
	TotalCost float64        `json:"total_cost"`
}

// trackedJob wraps a JobDef with mutable state and result.
type trackedJob struct {
	def    JobDef
	state  JobState
	result *JobResult
}

// Queue manages job state transitions in a thread-safe manner.
// Jobs flow through: pending -> running -> completed | failed.
type Queue struct {
	mu   sync.Mutex
	jobs []*trackedJob
}

// NewQueue creates a new Queue from a slice of job definitions.
// All jobs start in StatePending.
func NewQueue(jobs []JobDef) *Queue {
	tracked := make([]*trackedJob, len(jobs))
	for i, j := range jobs {
		tracked[i] = &trackedJob{def: j, state: StatePending}
	}
	return &Queue{jobs: tracked}
}

// Next returns the next pending job and moves it to running.
// Returns false if no pending jobs remain.
func (q *Queue) Next() (*JobDef, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()

	for _, tj := range q.jobs {
		if tj.state == StatePending {
			tj.state = StateRunning
			// Return a copy so callers cannot mutate internal state.
			def := tj.def
			return &def, true
		}
	}
	return nil, false
}

// Complete marks a running job as completed with the given result.
func (q *Queue) Complete(name string, result *JobResult) {
	q.mu.Lock()
	defer q.mu.Unlock()

	for _, tj := range q.jobs {
		if tj.def.Name == name && tj.state == StateRunning {
			tj.state = StateCompleted
			tj.result = result
			return
		}
	}
}

// Fail marks a running job as failed with the given result.
func (q *Queue) Fail(name string, result *JobResult) {
	q.mu.Lock()
	defer q.mu.Unlock()

	for _, tj := range q.jobs {
		if tj.def.Name == name && tj.state == StateRunning {
			tj.state = StateFailed
			tj.result = result
			return
		}
	}
}

// PendingCount returns the number of jobs in pending state.
func (q *Queue) PendingCount() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.countState(StatePending)
}

// RunningCount returns the number of jobs in running state.
func (q *Queue) RunningCount() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.countState(StateRunning)
}

// CompletedCount returns the number of jobs in completed state.
func (q *Queue) CompletedCount() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.countState(StateCompleted)
}

// FailedCount returns the number of jobs in failed state.
func (q *Queue) FailedCount() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.countState(StateFailed)
}

// countState counts jobs in the given state. Caller must hold q.mu.
func (q *Queue) countState(s JobState) int {
	n := 0
	for _, tj := range q.jobs {
		if tj.state == s {
			n++
		}
	}
	return n
}

// Snapshot returns a serializable snapshot of the queue state.
// Running jobs are folded back into the Pending list so that a
// checkpoint can be resumed without losing in-flight work.
func (q *Queue) Snapshot() *QueueSnapshot {
	q.mu.Lock()
	defer q.mu.Unlock()

	snap := &QueueSnapshot{}

	for _, tj := range q.jobs {
		switch tj.state {
		case StatePending, StateRunning:
			snap.Pending = append(snap.Pending, tj.def)
		case StateCompleted:
			cj := CompletedJob{Name: tj.def.Name}
			if tj.result != nil {
				cj.Cost = tj.result.Cost
				cj.Duration = tj.result.Duration
				cj.SessionID = tj.result.SessionID
				snap.TotalCost += tj.result.Cost
			}
			snap.Completed = append(snap.Completed, cj)
		case StateFailed:
			fj := FailedJob{Name: tj.def.Name}
			if tj.result != nil {
				fj.Error = tj.result.Error
			}
			snap.Failed = append(snap.Failed, fj)
		}
	}

	return snap
}
