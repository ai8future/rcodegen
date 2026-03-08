# rbatch Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Build `rbatch`, a batch job runner that pulls jobs from manifests, spool directories, or gRPC, executes them with session-aware concurrency, budget checking, and checkpoint/resume.

**Architecture:** New `pkg/batch/` package with queue, scheduler, and dual executors (local via `pkg/runner`, remote via gRPC to rserve). New `cmd/rbatch/main.go` CLI with subcommands `run`, `spool`, `watch`, `resume`, `status`. Follows existing patterns from `cmd/rserve/` and `pkg/server/`.

**Tech Stack:** Go 1.25, existing `pkg/runner` + `pkg/tools/*` + `pkg/tracking`, `fsnotify` for watch mode, existing gRPC client for rserve delegation.

---

### Task 1: Manifest Types and Parsing

**Files:**
- Create: `pkg/batch/manifest.go`
- Test: `pkg/batch/manifest_test.go`

**Step 1: Write the failing test**

```go
package batch

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadManifest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "batch.json")
	data := `{
		"name": "test-batch",
		"concurrency": 4,
		"budget": {
			"threshold_pct": 1,
			"on_budget": "stop",
			"check_interval": "3m",
			"max_wait": "1h"
		},
		"jobs": [
			{
				"name": "audit-a",
				"task": "audit",
				"tool": "claude",
				"dir": "/code/a",
				"session": "chain-1"
			},
			{
				"name": "fix-a",
				"task": "fix issues",
				"tool": "claude",
				"dir": "/code/a",
				"session": "chain-1"
			},
			{
				"name": "lint-b",
				"task": "fix lint",
				"tool": "gemini",
				"dir": "/code/b"
			}
		]
	}`
	os.WriteFile(path, []byte(data), 0644)

	m, err := LoadManifest(path)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if m.Name != "test-batch" {
		t.Errorf("name = %q, want %q", m.Name, "test-batch")
	}
	if m.Concurrency != 4 {
		t.Errorf("concurrency = %d, want 4", m.Concurrency)
	}
	if len(m.Jobs) != 3 {
		t.Fatalf("jobs count = %d, want 3", len(m.Jobs))
	}
	if m.Budget.ThresholdPct != 1 {
		t.Errorf("threshold_pct = %d, want 1", m.Budget.ThresholdPct)
	}
	if m.Budget.OnBudget != "stop" {
		t.Errorf("on_budget = %q, want %q", m.Budget.OnBudget, "stop")
	}
}

func TestLoadManifestDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "minimal.json")
	data := `{"name": "minimal", "jobs": [{"task": "hello", "dir": "/tmp"}]}`
	os.WriteFile(path, []byte(data), 0644)

	m, err := LoadManifest(path)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if m.Concurrency != 1 {
		t.Errorf("default concurrency = %d, want 1", m.Concurrency)
	}
	if m.Jobs[0].Tool != "claude" {
		t.Errorf("default tool = %q, want %q", m.Jobs[0].Tool, "claude")
	}
	if m.Jobs[0].Name != "job-1" {
		t.Errorf("auto name = %q, want %q", m.Jobs[0].Name, "job-1")
	}
}

func TestLoadManifestValidation(t *testing.T) {
	dir := t.TempDir()

	// No jobs
	path := filepath.Join(dir, "empty.json")
	os.WriteFile(path, []byte(`{"name": "empty", "jobs": []}`), 0644)
	_, err := LoadManifest(path)
	if err == nil {
		t.Error("expected error for empty jobs")
	}

	// Missing task
	path = filepath.Join(dir, "no-task.json")
	os.WriteFile(path, []byte(`{"name": "x", "jobs": [{"dir": "/tmp"}]}`), 0644)
	_, err = LoadManifest(path)
	if err == nil {
		t.Error("expected error for missing task")
	}

	// Invalid on_budget
	path = filepath.Join(dir, "bad-budget.json")
	os.WriteFile(path, []byte(`{"name": "x", "budget": {"on_budget": "explode"}, "jobs": [{"task": "hi"}]}`), 0644)
	_, err = LoadManifest(path)
	if err == nil {
		t.Error("expected error for invalid on_budget")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `cd /Users/cliff/Desktop/_code/codegen_suite/rcodegen && go test ./pkg/batch/ -v -run TestLoadManifest`
Expected: FAIL - package does not exist

**Step 3: Write minimal implementation**

```go
package batch

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// Manifest describes a batch of jobs to execute.
type Manifest struct {
	Name        string       `json:"name"`
	Concurrency int          `json:"concurrency"`
	Budget      BudgetConfig `json:"budget"`
	Jobs        []JobDef     `json:"jobs"`
}

// BudgetConfig controls budget-aware pausing.
type BudgetConfig struct {
	ThresholdPct  int    `json:"threshold_pct"`
	OnBudget      string `json:"on_budget"`      // stop, wait, ask
	CheckInterval string `json:"check_interval"`  // duration string (e.g. "3m")
	MaxWait       string `json:"max_wait"`         // duration string (e.g. "1h")
}

// CheckIntervalDuration parses CheckInterval as a time.Duration.
func (b *BudgetConfig) CheckIntervalDuration() time.Duration {
	d, err := time.ParseDuration(b.CheckInterval)
	if err != nil {
		return 3 * time.Minute
	}
	return d
}

// MaxWaitDuration parses MaxWait as a time.Duration.
func (b *BudgetConfig) MaxWaitDuration() time.Duration {
	d, err := time.ParseDuration(b.MaxWait)
	if err != nil {
		return 1 * time.Hour
	}
	return d
}

// JobDef describes a single job within a batch.
type JobDef struct {
	Name      string `json:"name"`
	Task      string `json:"task"`
	Tool      string `json:"tool"`
	Dir       string `json:"dir"`
	Session   string `json:"session,omitempty"`
	Model     string `json:"model,omitempty"`
	Effort    string `json:"effort,omitempty"`
	MaxBudget string `json:"max_budget,omitempty"`
}

// LoadManifest reads and validates a batch manifest from a JSON file.
func LoadManifest(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}

	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}

	// Apply defaults
	if m.Concurrency < 1 {
		m.Concurrency = 1
	}
	if m.Budget.OnBudget == "" {
		m.Budget.OnBudget = "stop"
	}
	if m.Budget.CheckInterval == "" {
		m.Budget.CheckInterval = "3m"
	}
	if m.Budget.MaxWait == "" {
		m.Budget.MaxWait = "1h"
	}

	// Auto-name and default tool
	for i := range m.Jobs {
		if m.Jobs[i].Name == "" {
			m.Jobs[i].Name = fmt.Sprintf("job-%d", i+1)
		}
		if m.Jobs[i].Tool == "" {
			m.Jobs[i].Tool = "claude"
		}
	}

	// Validate
	if err := m.validate(); err != nil {
		return nil, err
	}

	return &m, nil
}

func (m *Manifest) validate() error {
	if len(m.Jobs) == 0 {
		return fmt.Errorf("manifest has no jobs")
	}
	validBudgetActions := map[string]bool{"stop": true, "wait": true, "ask": true}
	if !validBudgetActions[m.Budget.OnBudget] {
		return fmt.Errorf("invalid on_budget value: %q (must be stop, wait, or ask)", m.Budget.OnBudget)
	}
	validTools := map[string]bool{"claude": true, "codex": true, "gemini": true}
	for i, j := range m.Jobs {
		if j.Task == "" {
			return fmt.Errorf("job %d (%s): task is required", i, j.Name)
		}
		if !validTools[j.Tool] {
			return fmt.Errorf("job %d (%s): unknown tool %q", i, j.Name, j.Tool)
		}
	}
	return nil
}
```

**Step 4: Run test to verify it passes**

Run: `cd /Users/cliff/Desktop/_code/codegen_suite/rcodegen && go test ./pkg/batch/ -v -run TestLoadManifest`
Expected: PASS (all 3 test functions)

**Step 5: Commit**

```bash
git add pkg/batch/manifest.go pkg/batch/manifest_test.go
git commit -m "feat(rbatch): add manifest types and parsing"
```

---

### Task 2: Job Queue State Machine

**Files:**
- Create: `pkg/batch/queue.go`
- Test: `pkg/batch/queue_test.go`

**Step 1: Write the failing test**

```go
package batch

import (
	"testing"
)

func TestQueueBasicFlow(t *testing.T) {
	jobs := []JobDef{
		{Name: "a", Task: "do a", Tool: "claude"},
		{Name: "b", Task: "do b", Tool: "codex"},
	}
	q := NewQueue(jobs)

	if q.PendingCount() != 2 {
		t.Errorf("pending = %d, want 2", q.PendingCount())
	}

	j, ok := q.Next()
	if !ok {
		t.Fatal("expected a job")
	}
	if j.Name != "a" {
		t.Errorf("first job = %q, want %q", j.Name, "a")
	}
	if q.PendingCount() != 1 {
		t.Errorf("pending after next = %d, want 1", q.PendingCount())
	}
	if q.RunningCount() != 1 {
		t.Errorf("running = %d, want 1", q.RunningCount())
	}

	q.Complete(j.Name, &JobResult{ExitCode: 0, Cost: 0.05, Duration: "10s"})
	if q.CompletedCount() != 1 {
		t.Errorf("completed = %d, want 1", q.CompletedCount())
	}
}

func TestQueueFail(t *testing.T) {
	jobs := []JobDef{{Name: "x", Task: "fail", Tool: "claude"}}
	q := NewQueue(jobs)
	j, _ := q.Next()
	q.Fail(j.Name, &JobResult{ExitCode: 1, Error: "boom"})
	if q.FailedCount() != 1 {
		t.Errorf("failed = %d, want 1", q.FailedCount())
	}
}

func TestQueueEmpty(t *testing.T) {
	q := NewQueue(nil)
	_, ok := q.Next()
	if ok {
		t.Error("expected no job from empty queue")
	}
}

func TestQueueSnapshot(t *testing.T) {
	jobs := []JobDef{
		{Name: "a", Task: "do a", Tool: "claude"},
		{Name: "b", Task: "do b", Tool: "codex"},
	}
	q := NewQueue(jobs)
	j, _ := q.Next()
	q.Complete(j.Name, &JobResult{ExitCode: 0, Cost: 0.10})

	snap := q.Snapshot()
	if len(snap.Completed) != 1 {
		t.Errorf("snapshot completed = %d, want 1", len(snap.Completed))
	}
	if len(snap.Pending) != 1 {
		t.Errorf("snapshot pending = %d, want 1", len(snap.Pending))
	}
	if snap.TotalCost != 0.10 {
		t.Errorf("snapshot cost = %f, want 0.10", snap.TotalCost)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `cd /Users/cliff/Desktop/_code/codegen_suite/rcodegen && go test ./pkg/batch/ -v -run TestQueue`
Expected: FAIL - undefined: NewQueue

**Step 3: Write minimal implementation**

```go
package batch

import (
	"sync"
)

// JobState represents the current state of a job.
type JobState int

const (
	StatePending JobState = iota
	StateRunning
	StateCompleted
	StateFailed
)

// JobResult holds the outcome of a completed or failed job.
type JobResult struct {
	ExitCode  int     `json:"exit_code"`
	Cost      float64 `json:"cost"`
	Duration  string  `json:"duration"`
	SessionID string  `json:"session_id,omitempty"`
	Error     string  `json:"error,omitempty"`
}

// trackedJob wraps a job definition with its runtime state.
type trackedJob struct {
	Def    JobDef
	State  JobState
	Result *JobResult
}

// QueueSnapshot is a serializable snapshot of the queue for checkpointing.
type QueueSnapshot struct {
	Completed []CompletedJob `json:"completed"`
	Failed    []FailedJob    `json:"failed"`
	Pending   []JobDef       `json:"pending"`
	TotalCost float64        `json:"total_cost"`
}

// CompletedJob records a finished job in the snapshot.
type CompletedJob struct {
	Name      string  `json:"name"`
	Cost      float64 `json:"cost"`
	Duration  string  `json:"duration"`
	SessionID string  `json:"session_id,omitempty"`
}

// FailedJob records a failed job in the snapshot.
type FailedJob struct {
	Name  string `json:"name"`
	Error string `json:"error"`
}

// Queue manages job state transitions. Thread-safe.
type Queue struct {
	mu   sync.Mutex
	jobs []*trackedJob
}

// NewQueue creates a queue from a list of job definitions.
func NewQueue(jobs []JobDef) *Queue {
	tracked := make([]*trackedJob, len(jobs))
	for i, j := range jobs {
		tracked[i] = &trackedJob{Def: j, State: StatePending}
	}
	return &Queue{jobs: tracked}
}

// Next returns the next pending job, moving it to running state.
// Returns false if no pending jobs remain.
func (q *Queue) Next() (*JobDef, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, j := range q.jobs {
		if j.State == StatePending {
			j.State = StateRunning
			def := j.Def // copy
			return &def, true
		}
	}
	return nil, false
}

// Complete marks a running job as completed.
func (q *Queue) Complete(name string, result *JobResult) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, j := range q.jobs {
		if j.Def.Name == name && j.State == StateRunning {
			j.State = StateCompleted
			j.Result = result
			return
		}
	}
}

// Fail marks a running job as failed.
func (q *Queue) Fail(name string, result *JobResult) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, j := range q.jobs {
		if j.Def.Name == name && j.State == StateRunning {
			j.State = StateFailed
			j.Result = result
			return
		}
	}
}

// PendingCount returns the number of pending jobs.
func (q *Queue) PendingCount() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.countState(StatePending)
}

// RunningCount returns the number of running jobs.
func (q *Queue) RunningCount() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.countState(StateRunning)
}

// CompletedCount returns the number of completed jobs.
func (q *Queue) CompletedCount() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.countState(StateCompleted)
}

// FailedCount returns the number of failed jobs.
func (q *Queue) FailedCount() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.countState(StateFailed)
}

// Snapshot returns a serializable snapshot of the current queue state.
func (q *Queue) Snapshot() *QueueSnapshot {
	q.mu.Lock()
	defer q.mu.Unlock()

	snap := &QueueSnapshot{}
	for _, j := range q.jobs {
		switch j.State {
		case StateCompleted:
			cj := CompletedJob{Name: j.Def.Name, Cost: j.Result.Cost, Duration: j.Result.Duration}
			if j.Result != nil {
				cj.SessionID = j.Result.SessionID
			}
			snap.Completed = append(snap.Completed, cj)
			snap.TotalCost += j.Result.Cost
		case StateFailed:
			errMsg := ""
			if j.Result != nil {
				errMsg = j.Result.Error
			}
			snap.Failed = append(snap.Failed, FailedJob{Name: j.Def.Name, Error: errMsg})
		case StatePending, StateRunning:
			// Running jobs go back to pending in checkpoints (will be retried)
			snap.Pending = append(snap.Pending, j.Def)
		}
	}
	return snap
}

func (q *Queue) countState(state JobState) int {
	n := 0
	for _, j := range q.jobs {
		if j.State == state {
			n++
		}
	}
	return n
}
```

**Step 4: Run test to verify it passes**

Run: `cd /Users/cliff/Desktop/_code/codegen_suite/rcodegen && go test ./pkg/batch/ -v -run TestQueue`
Expected: PASS

**Step 5: Commit**

```bash
git add pkg/batch/queue.go pkg/batch/queue_test.go
git commit -m "feat(rbatch): add job queue state machine"
```

---

### Task 3: Scheduler - Session Group Building

**Files:**
- Create: `pkg/batch/scheduler.go`
- Test: `pkg/batch/scheduler_test.go`

**Step 1: Write the failing test**

```go
package batch

import (
	"testing"
)

func TestBuildSessionGroups(t *testing.T) {
	jobs := []JobDef{
		{Name: "a1", Task: "audit", Session: "chain-1"},
		{Name: "a2", Task: "fix", Session: "chain-1"},
		{Name: "b1", Task: "audit", Session: "chain-2"},
		{Name: "c1", Task: "lint"},
		{Name: "d1", Task: "update"},
	}

	groups := BuildSessionGroups(jobs)

	// Should have 4 groups: chain-1, chain-2, c1-standalone, d1-standalone
	if len(groups) != 4 {
		t.Fatalf("groups = %d, want 4", len(groups))
	}

	// Find chain-1 group
	var chain1 *SessionGroup
	for i := range groups {
		if groups[i].Session == "chain-1" {
			chain1 = &groups[i]
			break
		}
	}
	if chain1 == nil {
		t.Fatal("chain-1 group not found")
	}
	if len(chain1.Jobs) != 2 {
		t.Errorf("chain-1 jobs = %d, want 2", len(chain1.Jobs))
	}
	if chain1.Jobs[0].Name != "a1" || chain1.Jobs[1].Name != "a2" {
		t.Errorf("chain-1 order wrong: %s, %s", chain1.Jobs[0].Name, chain1.Jobs[1].Name)
	}
}

func TestBuildSessionGroupsPreservesOrder(t *testing.T) {
	jobs := []JobDef{
		{Name: "step3", Task: "c", Session: "s"},
		{Name: "step1", Task: "a", Session: "s"},
		{Name: "step2", Task: "b", Session: "s"},
	}

	groups := BuildSessionGroups(jobs)
	if len(groups) != 1 {
		t.Fatalf("groups = %d, want 1", len(groups))
	}
	// Order should match manifest order
	if groups[0].Jobs[0].Name != "step3" {
		t.Errorf("first job = %q, want %q", groups[0].Jobs[0].Name, "step3")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `cd /Users/cliff/Desktop/_code/codegen_suite/rcodegen && go test ./pkg/batch/ -v -run TestBuildSession`
Expected: FAIL - undefined: BuildSessionGroups

**Step 3: Write minimal implementation**

```go
package batch

import (
	"crypto/rand"
	"fmt"
)

// SessionGroup is a set of jobs that share a session and must run sequentially.
// Groups without a session name contain a single standalone job.
type SessionGroup struct {
	ID      string   // Unique group ID
	Session string   // Session name (empty for standalone)
	Jobs    []JobDef // Jobs in execution order
}

// BuildSessionGroups partitions jobs into session groups.
// Jobs with the same session value are grouped together in manifest order.
// Jobs without a session become standalone groups of one.
func BuildSessionGroups(jobs []JobDef) []SessionGroup {
	// Preserve insertion order of sessions
	sessionOrder := []string{}
	sessionJobs := map[string][]JobDef{}

	var standalones []JobDef

	for _, j := range jobs {
		if j.Session == "" {
			standalones = append(standalones, j)
		} else {
			if _, seen := sessionJobs[j.Session]; !seen {
				sessionOrder = append(sessionOrder, j.Session)
			}
			sessionJobs[j.Session] = append(sessionJobs[j.Session], j)
		}
	}

	var groups []SessionGroup

	// Add session chains in order
	for _, session := range sessionOrder {
		groups = append(groups, SessionGroup{
			ID:      generateGroupID(),
			Session: session,
			Jobs:    sessionJobs[session],
		})
	}

	// Add standalone jobs
	for _, j := range standalones {
		groups = append(groups, SessionGroup{
			ID:   generateGroupID(),
			Jobs: []JobDef{j},
		})
	}

	return groups
}

func generateGroupID() string {
	b := make([]byte, 4)
	rand.Read(b)
	return fmt.Sprintf("g-%x", b)
}
```

**Step 4: Run test to verify it passes**

Run: `cd /Users/cliff/Desktop/_code/codegen_suite/rcodegen && go test ./pkg/batch/ -v -run TestBuildSession`
Expected: PASS

**Step 5: Commit**

```bash
git add pkg/batch/scheduler.go pkg/batch/scheduler_test.go
git commit -m "feat(rbatch): add session group builder"
```

---

### Task 4: Executor Interface and Local Executor

**Files:**
- Create: `pkg/batch/executor.go`
- Create: `pkg/batch/executor_local.go`
- Test: `pkg/batch/executor_test.go`

**Step 1: Write the failing test**

```go
package batch

import (
	"context"
	"testing"
)

// mockExecutor implements Executor for testing
type mockExecutor struct {
	results map[string]*JobResult
}

func (m *mockExecutor) Execute(ctx context.Context, job *JobDef, sessionID string) (*JobResult, error) {
	if r, ok := m.results[job.Name]; ok {
		return r, nil
	}
	return &JobResult{ExitCode: 0, Cost: 0.01, Duration: "1s", SessionID: "sess-mock"}, nil
}

func (m *mockExecutor) CheckBudget(ctx context.Context) (remainingPct int, err error) {
	return 80, nil
}

func TestExecutorInterface(t *testing.T) {
	var e Executor = &mockExecutor{results: map[string]*JobResult{}}
	job := &JobDef{Name: "test", Task: "hello", Tool: "claude", Dir: "/tmp"}
	result, err := e.Execute(context.Background(), job, "")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if result.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0", result.ExitCode)
	}
	if result.SessionID != "sess-mock" {
		t.Errorf("session_id = %q, want %q", result.SessionID, "sess-mock")
	}
}

func TestExecutorBudgetCheck(t *testing.T) {
	var e Executor = &mockExecutor{}
	pct, err := e.CheckBudget(context.Background())
	if err != nil {
		t.Fatalf("check budget: %v", err)
	}
	if pct != 80 {
		t.Errorf("remaining = %d, want 80", pct)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `cd /Users/cliff/Desktop/_code/codegen_suite/rcodegen && go test ./pkg/batch/ -v -run TestExecutor`
Expected: FAIL - undefined: Executor

**Step 3: Write the executor interface**

`pkg/batch/executor.go`:
```go
package batch

import "context"

// Executor runs a single job and returns its result.
// Implementations: LocalExecutor (in-process via pkg/runner) and RemoteExecutor (gRPC to rserve).
type Executor interface {
	// Execute runs a job, optionally resuming a session.
	// sessionID is empty for new sessions, or a previous session's ID for chains.
	Execute(ctx context.Context, job *JobDef, sessionID string) (*JobResult, error)

	// CheckBudget returns the remaining usage percentage (0-100).
	// Returns -1 if budget checking is not supported by the tool.
	CheckBudget(ctx context.Context) (remainingPct int, err error)
}
```

**Step 4: Write the local executor**

`pkg/batch/executor_local.go`:
```go
package batch

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"time"

	"rcodegen/pkg/runner"
	"rcodegen/pkg/settings"
	"rcodegen/pkg/tools/claude"
	"rcodegen/pkg/tools/codex"
	"rcodegen/pkg/tools/gemini"
	"rcodegen/pkg/tracking"

	"github.com/ai8future/chassis-go/v7/logz"
)

// ToolFactory creates a fresh tool instance.
type ToolFactory func() runner.Tool

// LocalExecutor runs jobs in-process via pkg/runner.
type LocalExecutor struct {
	Settings      *settings.Settings
	ToolFactories map[string]ToolFactory
}

// NewLocalExecutor creates a local executor with default tool factories.
func NewLocalExecutor(s *settings.Settings) *LocalExecutor {
	return &LocalExecutor{
		Settings: s,
		ToolFactories: map[string]ToolFactory{
			"claude": func() runner.Tool { return claude.New() },
			"codex":  func() runner.Tool { return codex.New() },
			"gemini": func() runner.Tool { return gemini.New() },
		},
	}
}

// Execute runs a single job using the runner framework.
func (e *LocalExecutor) Execute(ctx context.Context, job *JobDef, sessionID string) (*JobResult, error) {
	factory, ok := e.ToolFactories[job.Tool]
	if !ok {
		return nil, fmt.Errorf("unknown tool: %s", job.Tool)
	}

	tool := factory()

	// Inject settings
	if sa, ok := tool.(runner.SettingsAware); ok && e.Settings != nil {
		sa.SetSettings(e.Settings)
	}

	cfg := runner.NewConfig()
	cfg.Task = job.Task
	if job.Dir != "" {
		cfg.WorkDirs = []string{job.Dir}
	}
	cfg.Output = io.Discard
	cfg.Logger = logz.New("warn")
	cfg.Stderr = &bytes.Buffer{}

	// Apply tool defaults then job overrides
	tool.ApplyToolDefaults(cfg)
	if job.Model != "" {
		cfg.Model = job.Model
	}
	if cfg.Model == "" {
		cfg.Model = tool.DefaultModel()
	}
	if job.Effort != "" {
		cfg.Effort = job.Effort
	}
	if job.MaxBudget != "" {
		cfg.MaxBudget = job.MaxBudget
	}
	if sessionID != "" {
		cfg.SessionID = sessionID
	}

	start := time.Now()
	r := &runner.Runner{Tool: tool, Settings: e.Settings}
	result := r.RunWithContext(ctx, cfg)
	duration := time.Since(start)

	jr := &JobResult{
		ExitCode: result.ExitCode,
		Cost:     result.TotalCostUSD,
		Duration: duration.Round(time.Second).String(),
	}
	if result.Error != nil {
		jr.Error = result.Error.Error()
	}

	// Extract session ID from token usage or stream events
	// The session ID is captured during execution and stored on the config
	if cfg.SessionID != "" && sessionID == "" {
		// New session was created - capture the ID for future jobs in the chain
		jr.SessionID = cfg.SessionID
	} else if sessionID != "" {
		// Reused existing session
		jr.SessionID = sessionID
	}

	return jr, nil
}

// CheckBudget uses the status-only tracking to check remaining credits.
func (e *LocalExecutor) CheckBudget(ctx context.Context) (int, error) {
	status := tracking.GetClaudeStatus()
	if status.Error != "" {
		return -1, nil // Budget checking not available
	}

	// Use session remaining as the primary metric
	if status.SessionLeft != nil {
		return *status.SessionLeft, nil
	}
	if status.WeeklyAllLeft != nil {
		return *status.WeeklyAllLeft, nil
	}
	return -1, nil // No metrics available
}
```

**Step 5: Run test to verify it passes**

Run: `cd /Users/cliff/Desktop/_code/codegen_suite/rcodegen && go test ./pkg/batch/ -v -run TestExecutor`
Expected: PASS

**Step 6: Commit**

```bash
git add pkg/batch/executor.go pkg/batch/executor_local.go pkg/batch/executor_test.go
git commit -m "feat(rbatch): add executor interface and local executor"
```

---

### Task 5: Checkpoint Save and Resume

**Files:**
- Create: `pkg/batch/checkpoint.go`
- Test: `pkg/batch/checkpoint_test.go`

**Step 1: Write the failing test**

```go
package batch

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckpointSaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	batchDir := filepath.Join(dir, "test-batch")

	snap := &QueueSnapshot{
		Completed: []CompletedJob{
			{Name: "a", Cost: 0.12, Duration: "45s", SessionID: "sess-abc"},
		},
		Pending: []JobDef{
			{Name: "b", Task: "fix", Tool: "claude", Session: "chain-1"},
		},
		TotalCost: 0.12,
	}

	cp := &Checkpoint{
		Batch:  "test-batch",
		Reason: "budget_threshold",
	}

	err := cp.Save(batchDir, snap)
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	// Verify file exists
	statePath := filepath.Join(batchDir, "state.json")
	if _, err := os.Stat(statePath); err != nil {
		t.Fatalf("state.json not found: %v", err)
	}

	// Load it back
	loaded, err := LoadCheckpoint(statePath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.Batch != "test-batch" {
		t.Errorf("batch = %q, want %q", loaded.Batch, "test-batch")
	}
	if loaded.Reason != "budget_threshold" {
		t.Errorf("reason = %q, want %q", loaded.Reason, "budget_threshold")
	}
	if len(loaded.Snapshot.Completed) != 1 {
		t.Errorf("completed = %d, want 1", len(loaded.Snapshot.Completed))
	}
	if len(loaded.Snapshot.Pending) != 1 {
		t.Errorf("pending = %d, want 1", len(loaded.Snapshot.Pending))
	}
	if loaded.Snapshot.Completed[0].SessionID != "sess-abc" {
		t.Errorf("session_id = %q, want %q", loaded.Snapshot.Completed[0].SessionID, "sess-abc")
	}
}

func TestCheckpointLatest(t *testing.T) {
	dir := t.TempDir()

	// No batches yet
	_, err := FindLatestCheckpoint(dir)
	if err == nil {
		t.Error("expected error for empty dir")
	}

	// Create a batch dir with state
	batchDir := filepath.Join(dir, "my-batch")
	cp := &Checkpoint{Batch: "my-batch", Reason: "user_stop"}
	cp.Save(batchDir, &QueueSnapshot{TotalCost: 1.0})

	path, err := FindLatestCheckpoint(dir)
	if err != nil {
		t.Fatalf("find latest: %v", err)
	}
	if filepath.Base(filepath.Dir(path)) != "my-batch" {
		t.Errorf("latest dir = %q, want my-batch", filepath.Base(filepath.Dir(path)))
	}
}
```

**Step 2: Run test to verify it fails**

Run: `cd /Users/cliff/Desktop/_code/codegen_suite/rcodegen && go test ./pkg/batch/ -v -run TestCheckpoint`
Expected: FAIL - undefined: Checkpoint

**Step 3: Write minimal implementation**

```go
package batch

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Checkpoint holds the saved state of a batch run.
type Checkpoint struct {
	Batch        string         `json:"batch"`
	CheckpointAt string         `json:"checkpoint_at"`
	Reason       string         `json:"reason"`
	Snapshot     *QueueSnapshot `json:"snapshot"`
}

// Save writes the checkpoint to batchDir/state.json.
func (c *Checkpoint) Save(batchDir string, snap *QueueSnapshot) error {
	if err := os.MkdirAll(batchDir, 0755); err != nil {
		return fmt.Errorf("create batch dir: %w", err)
	}

	c.CheckpointAt = time.Now().UTC().Format(time.RFC3339)
	c.Snapshot = snap

	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal checkpoint: %w", err)
	}

	path := filepath.Join(batchDir, "state.json")
	return os.WriteFile(path, data, 0644)
}

// LoadCheckpoint reads a checkpoint from a state.json file.
func LoadCheckpoint(path string) (*Checkpoint, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read checkpoint: %w", err)
	}

	var cp Checkpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		return nil, fmt.Errorf("parse checkpoint: %w", err)
	}
	return &cp, nil
}

// FindLatestCheckpoint finds the most recently modified state.json under baseDir.
func FindLatestCheckpoint(baseDir string) (string, error) {
	var latest string
	var latestTime time.Time

	entries, err := os.ReadDir(baseDir)
	if err != nil {
		return "", fmt.Errorf("read batches dir: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		statePath := filepath.Join(baseDir, entry.Name(), "state.json")
		info, err := os.Stat(statePath)
		if err != nil {
			continue
		}
		if latest == "" || info.ModTime().After(latestTime) {
			latest = statePath
			latestTime = info.ModTime()
		}
	}

	if latest == "" {
		return "", fmt.Errorf("no checkpoints found in %s", baseDir)
	}
	return latest, nil
}

// BatchDir returns the standard batch directory for a given batch name.
func BatchDir(batchName string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".rcodegen", "batches", batchName), nil
}
```

**Step 4: Run test to verify it passes**

Run: `cd /Users/cliff/Desktop/_code/codegen_suite/rcodegen && go test ./pkg/batch/ -v -run TestCheckpoint`
Expected: PASS

**Step 5: Commit**

```bash
git add pkg/batch/checkpoint.go pkg/batch/checkpoint_test.go
git commit -m "feat(rbatch): add checkpoint save and resume"
```

---

### Task 6: Budget Checker

**Files:**
- Create: `pkg/batch/budget.go`
- Test: `pkg/batch/budget_test.go`

**Step 1: Write the failing test**

```go
package batch

import (
	"context"
	"testing"
)

type fakeBudgetExecutor struct {
	remaining int
}

func (f *fakeBudgetExecutor) Execute(ctx context.Context, job *JobDef, sessionID string) (*JobResult, error) {
	return &JobResult{}, nil
}
func (f *fakeBudgetExecutor) CheckBudget(ctx context.Context) (int, error) {
	return f.remaining, nil
}

func TestBudgetCheckerOK(t *testing.T) {
	bc := &BudgetChecker{
		Config:   BudgetConfig{ThresholdPct: 5, OnBudget: "stop"},
		Executor: &fakeBudgetExecutor{remaining: 80},
	}
	action, err := bc.Check(context.Background())
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if action != BudgetContinue {
		t.Errorf("action = %v, want Continue", action)
	}
}

func TestBudgetCheckerThresholdHit(t *testing.T) {
	bc := &BudgetChecker{
		Config:   BudgetConfig{ThresholdPct: 5, OnBudget: "stop"},
		Executor: &fakeBudgetExecutor{remaining: 3},
	}
	action, err := bc.Check(context.Background())
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if action != BudgetStop {
		t.Errorf("action = %v, want Stop", action)
	}
}

func TestBudgetCheckerWait(t *testing.T) {
	bc := &BudgetChecker{
		Config:   BudgetConfig{ThresholdPct: 5, OnBudget: "wait"},
		Executor: &fakeBudgetExecutor{remaining: 2},
	}
	action, err := bc.Check(context.Background())
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if action != BudgetWait {
		t.Errorf("action = %v, want Wait", action)
	}
}

func TestBudgetCheckerUnavailable(t *testing.T) {
	bc := &BudgetChecker{
		Config:   BudgetConfig{ThresholdPct: 5, OnBudget: "stop"},
		Executor: &fakeBudgetExecutor{remaining: -1},
	}
	action, err := bc.Check(context.Background())
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if action != BudgetContinue {
		t.Errorf("action = %v, want Continue (unavailable should not block)", action)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `cd /Users/cliff/Desktop/_code/codegen_suite/rcodegen && go test ./pkg/batch/ -v -run TestBudgetChecker`
Expected: FAIL

**Step 3: Write minimal implementation**

```go
package batch

import "context"

// BudgetAction is the action to take when budget is checked.
type BudgetAction int

const (
	BudgetContinue BudgetAction = iota
	BudgetStop
	BudgetWait
	BudgetAsk
)

// BudgetChecker evaluates whether the batch should continue based on remaining credits.
type BudgetChecker struct {
	Config   BudgetConfig
	Executor Executor
}

// Check evaluates the current budget and returns the recommended action.
func (bc *BudgetChecker) Check(ctx context.Context) (BudgetAction, error) {
	if bc.Config.ThresholdPct <= 0 {
		return BudgetContinue, nil
	}

	remaining, err := bc.Executor.CheckBudget(ctx)
	if err != nil {
		return BudgetContinue, err // Don't block on errors
	}

	// -1 means budget checking not available
	if remaining < 0 {
		return BudgetContinue, nil
	}

	if remaining <= bc.Config.ThresholdPct {
		switch bc.Config.OnBudget {
		case "wait":
			return BudgetWait, nil
		case "ask":
			return BudgetAsk, nil
		default:
			return BudgetStop, nil
		}
	}

	return BudgetContinue, nil
}
```

**Step 4: Run test to verify it passes**

Run: `cd /Users/cliff/Desktop/_code/codegen_suite/rcodegen && go test ./pkg/batch/ -v -run TestBudgetChecker`
Expected: PASS

**Step 5: Commit**

```bash
git add pkg/batch/budget.go pkg/batch/budget_test.go
git commit -m "feat(rbatch): add budget checker"
```

---

### Task 7: Batch Runner (Scheduler + Concurrency + Budget Integration)

**Files:**
- Create: `pkg/batch/runner.go`
- Test: `pkg/batch/runner_test.go`

**Step 1: Write the failing test**

```go
package batch

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

type countingExecutor struct {
	calls     atomic.Int32
	remaining int
	delay     time.Duration
}

func (c *countingExecutor) Execute(ctx context.Context, job *JobDef, sessionID string) (*JobResult, error) {
	c.calls.Add(1)
	if c.delay > 0 {
		time.Sleep(c.delay)
	}
	return &JobResult{ExitCode: 0, Cost: 0.01, Duration: "1s", SessionID: "sess-" + job.Name}, nil
}

func (c *countingExecutor) CheckBudget(ctx context.Context) (int, error) {
	return c.remaining, nil
}

func TestBatchRunnerBasic(t *testing.T) {
	exec := &countingExecutor{remaining: 100}
	m := &Manifest{
		Name:        "test",
		Concurrency: 2,
		Budget:      BudgetConfig{OnBudget: "stop"},
		Jobs: []JobDef{
			{Name: "a", Task: "hello", Tool: "claude"},
			{Name: "b", Task: "world", Tool: "claude"},
		},
	}

	br := NewBatchRunner(m, exec)
	result := br.Run(context.Background())

	if result.JobsTotal != 2 {
		t.Errorf("total = %d, want 2", result.JobsTotal)
	}
	if result.JobsSucceeded != 2 {
		t.Errorf("succeeded = %d, want 2", result.JobsSucceeded)
	}
	if exec.calls.Load() != 2 {
		t.Errorf("executor calls = %d, want 2", exec.calls.Load())
	}
}

func TestBatchRunnerSessionChain(t *testing.T) {
	exec := &countingExecutor{remaining: 100}
	m := &Manifest{
		Name:        "chain-test",
		Concurrency: 4,
		Budget:      BudgetConfig{OnBudget: "stop"},
		Jobs: []JobDef{
			{Name: "a1", Task: "audit", Tool: "claude", Session: "chain"},
			{Name: "a2", Task: "fix", Tool: "claude", Session: "chain"},
		},
	}

	br := NewBatchRunner(m, exec)
	result := br.Run(context.Background())

	if result.JobsSucceeded != 2 {
		t.Errorf("succeeded = %d, want 2", result.JobsSucceeded)
	}
}

func TestBatchRunnerCancellation(t *testing.T) {
	exec := &countingExecutor{remaining: 100, delay: 100 * time.Millisecond}
	m := &Manifest{
		Name:        "cancel-test",
		Concurrency: 1,
		Budget:      BudgetConfig{OnBudget: "stop"},
		Jobs: []JobDef{
			{Name: "a", Task: "slow1", Tool: "claude"},
			{Name: "b", Task: "slow2", Tool: "claude"},
			{Name: "c", Task: "slow3", Tool: "claude"},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	br := NewBatchRunner(m, exec)
	result := br.Run(ctx)

	// Should have completed fewer than all 3 due to cancellation
	if result.JobsSucceeded >= 3 {
		t.Errorf("succeeded = %d, expected fewer than 3 due to cancellation", result.JobsSucceeded)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `cd /Users/cliff/Desktop/_code/codegen_suite/rcodegen && go test ./pkg/batch/ -v -run TestBatchRunner -timeout 10s`
Expected: FAIL

**Step 3: Write minimal implementation**

```go
package batch

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"
)

// BatchResult is the final summary of a batch run.
type BatchResult struct {
	Name          string  `json:"name"`
	Status        string  `json:"status"` // completed, stopped, cancelled, failed
	JobsTotal     int     `json:"jobs_total"`
	JobsSucceeded int     `json:"jobs_succeeded"`
	JobsFailed    int     `json:"jobs_failed"`
	TotalCost     float64 `json:"total_cost"`
	TotalDuration string  `json:"total_duration"`
	StopReason    string  `json:"stop_reason,omitempty"`
}

// BatchRunner orchestrates execution of a batch manifest.
type BatchRunner struct {
	Manifest *Manifest
	Executor Executor
	Budget   *BudgetChecker
	OnEvent  func(event BatchEvent) // Optional callback for live display
}

// BatchEvent represents a state change during batch execution.
type BatchEvent struct {
	Type      string   // "job_start", "job_complete", "job_fail", "budget_check", "group_start"
	JobName   string
	GroupID   string
	Result    *JobResult
	Remaining int // Budget remaining pct
}

// NewBatchRunner creates a runner for the given manifest and executor.
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

// Run executes the batch. Blocks until complete, stopped, or cancelled.
func (br *BatchRunner) Run(ctx context.Context) *BatchResult {
	start := time.Now()
	groups := BuildSessionGroups(br.Manifest.Jobs)

	result := &BatchResult{
		Name:      br.Manifest.Name,
		JobsTotal: len(br.Manifest.Jobs),
	}

	// Semaphore for concurrency control
	sem := make(chan struct{}, br.Manifest.Concurrency)
	var wg sync.WaitGroup
	var mu sync.Mutex

	for _, group := range groups {
		// Check for cancellation
		if ctx.Err() != nil {
			result.Status = "cancelled"
			result.StopReason = "context cancelled"
			break
		}

		// Budget check at group boundaries
		if br.Budget.Config.ThresholdPct > 0 {
			action, _ := br.Budget.Check(ctx)
			switch action {
			case BudgetStop:
				result.Status = "stopped"
				result.StopReason = "budget_threshold"
				// Save checkpoint
				br.saveCheckpoint(result)
				goto done
			case BudgetWait:
				if !br.waitForBudget(ctx) {
					result.Status = "stopped"
					result.StopReason = "budget_wait_timeout"
					br.saveCheckpoint(result)
					goto done
				}
			case BudgetAsk:
				fmt.Fprintf(os.Stderr, "Budget threshold reached. Continue? (not implemented in batch mode, stopping)\n")
				result.Status = "stopped"
				result.StopReason = "budget_ask"
				br.saveCheckpoint(result)
				goto done
			}
		}

		// Acquire semaphore slot
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			result.Status = "cancelled"
			result.StopReason = "context cancelled"
			goto done
		}

		wg.Add(1)
		go func(g SessionGroup) {
			defer wg.Done()
			defer func() { <-sem }()

			br.emit(BatchEvent{Type: "group_start", GroupID: g.ID})
			br.runGroup(ctx, g, &mu, result)
		}(group)
	}

done:
	wg.Wait()

	result.TotalDuration = time.Since(start).Round(time.Second).String()
	if result.Status == "" {
		if result.JobsFailed > 0 {
			result.Status = "completed_with_failures"
		} else {
			result.Status = "completed"
		}
	}

	return result
}

// runGroup executes a session group's jobs sequentially.
func (br *BatchRunner) runGroup(ctx context.Context, group SessionGroup, mu *sync.Mutex, result *BatchResult) {
	var sessionID string

	for _, job := range group.Jobs {
		if ctx.Err() != nil {
			return
		}

		br.emit(BatchEvent{Type: "job_start", JobName: job.Name, GroupID: group.ID})

		jr, err := br.Executor.Execute(ctx, &job, sessionID)
		if err != nil || (jr != nil && jr.ExitCode != 0) {
			mu.Lock()
			result.JobsFailed++
			if jr != nil {
				result.TotalCost += jr.Cost
			}
			mu.Unlock()
			br.emit(BatchEvent{Type: "job_fail", JobName: job.Name, GroupID: group.ID, Result: jr})
			return // Stop chain on failure
		}

		// Carry session forward in chain
		if jr.SessionID != "" {
			sessionID = jr.SessionID
		}

		mu.Lock()
		result.JobsSucceeded++
		result.TotalCost += jr.Cost
		mu.Unlock()

		br.emit(BatchEvent{Type: "job_complete", JobName: job.Name, GroupID: group.ID, Result: jr})
	}
}

// waitForBudget polls until budget recovers or timeout.
func (br *BatchRunner) waitForBudget(ctx context.Context) bool {
	interval := br.Budget.Config.CheckIntervalDuration()
	maxWait := br.Budget.Config.MaxWaitDuration()
	deadline := time.Now().Add(maxWait)

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(interval):
			action, _ := br.Budget.Check(ctx)
			if action == BudgetContinue {
				return true
			}
		}
	}
	return false
}

func (br *BatchRunner) emit(event BatchEvent) {
	if br.OnEvent != nil {
		br.OnEvent(event)
	}
}

func (br *BatchRunner) saveCheckpoint(result *BatchResult) {
	batchDir, err := BatchDir(br.Manifest.Name)
	if err != nil {
		return
	}
	cp := &Checkpoint{
		Batch:  br.Manifest.Name,
		Reason: result.StopReason,
	}
	// Build a minimal snapshot from the result
	// Full snapshot integration happens when Queue is wired in
	snap := &QueueSnapshot{TotalCost: result.TotalCost}
	cp.Save(batchDir, snap)
}
```

**Step 4: Run test to verify it passes**

Run: `cd /Users/cliff/Desktop/_code/codegen_suite/rcodegen && go test ./pkg/batch/ -v -run TestBatchRunner -timeout 10s`
Expected: PASS

**Step 5: Commit**

```bash
git add pkg/batch/runner.go pkg/batch/runner_test.go
git commit -m "feat(rbatch): add batch runner with concurrency and budget integration"
```

---

### Task 8: Reporter - Summary and Per-Job Results

**Files:**
- Create: `pkg/batch/reporter.go`
- Test: `pkg/batch/reporter_test.go`

**Step 1: Write the failing test**

```go
package batch

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteSummary(t *testing.T) {
	dir := t.TempDir()

	result := &BatchResult{
		Name:          "test-batch",
		Status:        "completed",
		JobsTotal:     5,
		JobsSucceeded: 4,
		JobsFailed:    1,
		TotalCost:     2.50,
		TotalDuration: "5m30s",
	}

	err := WriteSummary(dir, result)
	if err != nil {
		t.Fatalf("write summary: %v", err)
	}

	path := filepath.Join(dir, "summary.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("summary.json not found: %v", err)
	}
}

func TestWriteJobResult(t *testing.T) {
	dir := t.TempDir()

	jr := &JobResult{ExitCode: 0, Cost: 0.15, Duration: "30s", SessionID: "sess-123"}
	err := WriteJobResult(dir, "audit-project-a", jr)
	if err != nil {
		t.Fatalf("write job result: %v", err)
	}

	path := filepath.Join(dir, "results", "audit-project-a.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("result file not found: %v", err)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `cd /Users/cliff/Desktop/_code/codegen_suite/rcodegen && go test ./pkg/batch/ -v -run TestWrite`
Expected: FAIL

**Step 3: Write minimal implementation**

```go
package batch

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"rcodegen/pkg/colors"
)

// WriteSummary writes the batch result as summary.json.
func WriteSummary(batchDir string, result *BatchResult) error {
	if err := os.MkdirAll(batchDir, 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(batchDir, "summary.json"), data, 0644)
}

// WriteJobResult writes a single job's result to results/<name>.json.
func WriteJobResult(batchDir, jobName string, result *JobResult) error {
	resultsDir := filepath.Join(batchDir, "results")
	if err := os.MkdirAll(resultsDir, 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(resultsDir, jobName+".json"), data, 0644)
}

// PrintLiveStatus prints a one-line status update to stderr.
func PrintLiveStatus(name string, completed, failed, total, active int, cost float64, elapsed time.Duration) {
	pct := 0
	if total > 0 {
		pct = completed * 100 / total
	}
	bar := progressBar(pct, 20)
	fmt.Fprintf(os.Stderr, "\r%srbatch: %s%s  %s  %d/%d jobs  |  %d active  |  $%.2f  |  %s",
		colors.Bold, name, colors.Reset,
		bar, completed, total, active, cost, elapsed.Round(time.Second))
}

// PrintBatchSummary prints the final batch summary to stdout.
func PrintBatchSummary(result *BatchResult) {
	fmt.Printf("\n%s%srbatch: %s%s\n", colors.Bold, colors.Cyan, result.Name, colors.Reset)
	fmt.Printf("  Status:    %s\n", colorizeStatus(result.Status))
	fmt.Printf("  Jobs:      %d total, %s%d succeeded%s, %s%d failed%s\n",
		result.JobsTotal,
		colors.Green, result.JobsSucceeded, colors.Reset,
		colors.Red, result.JobsFailed, colors.Reset)
	fmt.Printf("  Cost:      %s$%.2f%s\n", colors.Green+colors.Bold, result.TotalCost, colors.Reset)
	fmt.Printf("  Duration:  %s%s%s\n", colors.Yellow, result.TotalDuration, colors.Reset)
	if result.StopReason != "" {
		fmt.Printf("  Stopped:   %s\n", result.StopReason)
	}
	fmt.Println()
}

func progressBar(pct, width int) string {
	filled := pct * width / 100
	if filled > width {
		filled = width
	}
	return "[" + strings.Repeat("=", filled) + strings.Repeat(".", width-filled) + "]"
}

func colorizeStatus(status string) string {
	switch status {
	case "completed":
		return colors.Green + status + colors.Reset
	case "completed_with_failures":
		return colors.Yellow + status + colors.Reset
	case "stopped", "cancelled":
		return colors.Red + status + colors.Reset
	default:
		return status
	}
}
```

**Step 4: Run test to verify it passes**

Run: `cd /Users/cliff/Desktop/_code/codegen_suite/rcodegen && go test ./pkg/batch/ -v -run TestWrite`
Expected: PASS

**Step 5: Commit**

```bash
git add pkg/batch/reporter.go pkg/batch/reporter_test.go
git commit -m "feat(rbatch): add reporter for summary and per-job results"
```

---

### Task 9: Spool Directory Support

**Files:**
- Create: `pkg/batch/spool.go`
- Test: `pkg/batch/spool_test.go`

**Step 1: Write the failing test**

```go
package batch

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSpoolScan(t *testing.T) {
	dir := t.TempDir()
	pending := filepath.Join(dir, "pending")
	os.MkdirAll(pending, 0755)

	// Write two job files
	job1 := `{"name": "lint-a", "jobs": [{"task": "lint", "tool": "claude", "dir": "/tmp"}]}`
	job2 := `{"name": "audit-b", "jobs": [{"task": "audit", "tool": "codex", "dir": "/tmp"}]}`
	os.WriteFile(filepath.Join(pending, "01-lint.json"), []byte(job1), 0644)
	os.WriteFile(filepath.Join(pending, "02-audit.json"), []byte(job2), 0644)

	spool := NewSpool(dir)
	manifests, err := spool.Scan()
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(manifests) != 2 {
		t.Fatalf("manifests = %d, want 2", len(manifests))
	}
}

func TestSpoolMoveToRunning(t *testing.T) {
	dir := t.TempDir()
	pending := filepath.Join(dir, "pending")
	running := filepath.Join(dir, "running")
	os.MkdirAll(pending, 0755)

	os.WriteFile(filepath.Join(pending, "test.json"), []byte(`{"name":"t","jobs":[{"task":"x"}]}`), 0644)

	spool := NewSpool(dir)
	err := spool.MarkRunning("test.json")
	if err != nil {
		t.Fatalf("mark running: %v", err)
	}

	// Should be gone from pending
	if _, err := os.Stat(filepath.Join(pending, "test.json")); err == nil {
		t.Error("file still in pending/")
	}
	// Should be in running
	if _, err := os.Stat(filepath.Join(running, "test.json")); err != nil {
		t.Error("file not in running/")
	}
}

func TestSpoolMoveToDone(t *testing.T) {
	dir := t.TempDir()
	running := filepath.Join(dir, "running")
	done := filepath.Join(dir, "done")
	os.MkdirAll(running, 0755)

	os.WriteFile(filepath.Join(running, "test.json"), []byte(`{}`), 0644)

	spool := NewSpool(dir)
	err := spool.MarkDone("test.json")
	if err != nil {
		t.Fatalf("mark done: %v", err)
	}
	if _, err := os.Stat(filepath.Join(done, "test.json")); err != nil {
		t.Error("file not in done/")
	}
}

func TestSpoolMoveToFailed(t *testing.T) {
	dir := t.TempDir()
	running := filepath.Join(dir, "running")
	failed := filepath.Join(dir, "failed")
	os.MkdirAll(running, 0755)

	os.WriteFile(filepath.Join(running, "test.json"), []byte(`{}`), 0644)

	spool := NewSpool(dir)
	err := spool.MarkFailed("test.json")
	if err != nil {
		t.Fatalf("mark failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(failed, "test.json")); err != nil {
		t.Error("file not in failed/")
	}
}

func TestSpoolInit(t *testing.T) {
	dir := t.TempDir()
	spoolDir := filepath.Join(dir, "myspool")

	spool := NewSpool(spoolDir)
	err := spool.Init()
	if err != nil {
		t.Fatalf("init: %v", err)
	}

	for _, sub := range []string{"pending", "running", "done", "failed"} {
		path := filepath.Join(spoolDir, sub)
		if info, err := os.Stat(path); err != nil || !info.IsDir() {
			t.Errorf("%s dir not created", sub)
		}
	}
}
```

**Step 2: Run test to verify it fails**

Run: `cd /Users/cliff/Desktop/_code/codegen_suite/rcodegen && go test ./pkg/batch/ -v -run TestSpool`
Expected: FAIL

**Step 3: Write minimal implementation**

```go
package batch

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Spool manages a spool directory with pending/running/done/failed subdirectories.
type Spool struct {
	Dir string
}

// NewSpool creates a spool manager for the given directory.
func NewSpool(dir string) *Spool {
	return &Spool{Dir: dir}
}

// Init creates the spool subdirectories if they don't exist.
func (s *Spool) Init() error {
	for _, sub := range []string{"pending", "running", "done", "failed"} {
		if err := os.MkdirAll(filepath.Join(s.Dir, sub), 0755); err != nil {
			return fmt.Errorf("create %s: %w", sub, err)
		}
	}
	return nil
}

// Scan reads all .json files from pending/ and parses them as manifests.
// Files are returned sorted by name (for deterministic ordering of session chains).
func (s *Spool) Scan() ([]*Manifest, error) {
	pendingDir := filepath.Join(s.Dir, "pending")
	entries, err := os.ReadDir(pendingDir)
	if err != nil {
		return nil, fmt.Errorf("read pending dir: %w", err)
	}

	// Sort by name for deterministic ordering
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	var manifests []*Manifest
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(pendingDir, entry.Name())
		m, err := LoadManifest(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: skipping %s: %v\n", entry.Name(), err)
			continue
		}
		manifests = append(manifests, m)
	}
	return manifests, nil
}

// MarkRunning moves a file from pending/ to running/.
func (s *Spool) MarkRunning(filename string) error {
	return s.moveFile("pending", "running", filename)
}

// MarkDone moves a file from running/ to done/.
func (s *Spool) MarkDone(filename string) error {
	return s.moveFile("running", "done", filename)
}

// MarkFailed moves a file from running/ to failed/.
func (s *Spool) MarkFailed(filename string) error {
	return s.moveFile("running", "failed", filename)
}

func (s *Spool) moveFile(from, to, filename string) error {
	destDir := filepath.Join(s.Dir, to)
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return err
	}
	src := filepath.Join(s.Dir, from, filename)
	dst := filepath.Join(destDir, filename)
	return os.Rename(src, dst)
}
```

**Step 4: Run test to verify it passes**

Run: `cd /Users/cliff/Desktop/_code/codegen_suite/rcodegen && go test ./pkg/batch/ -v -run TestSpool`
Expected: PASS

**Step 5: Commit**

```bash
git add pkg/batch/spool.go pkg/batch/spool_test.go
git commit -m "feat(rbatch): add spool directory support"
```

---

### Task 10: Remote Executor (gRPC to rserve)

**Files:**
- Create: `pkg/batch/executor_remote.go`

**Step 1: Write implementation**

```go
package batch

import (
	"context"
	"fmt"
	"io"
	"time"

	"rcodegen/pkg/server/pb"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// RemoteExecutor submits jobs to rserve via gRPC.
type RemoteExecutor struct {
	Addr string // e.g. "127.0.0.1:12345"
	conn *grpc.ClientConn
}

// NewRemoteExecutor creates an executor that delegates to rserve.
func NewRemoteExecutor(addr string) (*RemoteExecutor, error) {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("connect to rserve at %s: %w", addr, err)
	}
	return &RemoteExecutor{Addr: addr, conn: conn}, nil
}

// Close shuts down the gRPC connection.
func (r *RemoteExecutor) Close() error {
	if r.conn != nil {
		return r.conn.Close()
	}
	return nil
}

// Execute sends a job to rserve and waits for the result.
func (r *RemoteExecutor) Execute(ctx context.Context, job *JobDef, sessionID string) (*JobResult, error) {
	client := pb.NewRServeClient(r.conn)

	start := time.Now()
	stream, err := client.RunTask(ctx, &pb.RunTaskRequest{
		Tool:      job.Tool,
		Task:      job.Task,
		Model:     job.Model,
		MaxBudget: job.MaxBudget,
		WorkDirs:  []string{job.Dir},
	})
	if err != nil {
		return nil, fmt.Errorf("rserve RunTask: %w", err)
	}

	var totalCost float64
	var exitCode int32

	for {
		event, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return &JobResult{ExitCode: 1, Error: err.Error(), Duration: time.Since(start).Round(time.Second).String()}, nil
		}

		if result := event.GetResult(); result != nil {
			totalCost = result.TotalCostUsd
			exitCode = result.ExitCode
		}
	}

	return &JobResult{
		ExitCode: int(exitCode),
		Cost:     totalCost,
		Duration: time.Since(start).Round(time.Second).String(),
	}, nil
}

// CheckBudget is not supported via remote executor (rserve doesn't expose this).
func (r *RemoteExecutor) CheckBudget(ctx context.Context) (int, error) {
	return -1, nil
}
```

**Step 2: Commit**

No test for remote executor (requires running rserve instance). Integration testing only.

```bash
git add pkg/batch/executor_remote.go
git commit -m "feat(rbatch): add remote executor for gRPC delegation to rserve"
```

---

### Task 11: CLI Entrypoint

**Files:**
- Create: `cmd/rbatch/main.go`

**Step 1: Write the CLI**

```go
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"

	"rcodegen/pkg/batch"
	"rcodegen/pkg/runner"
	"rcodegen/pkg/settings"

	chassis "github.com/ai8future/chassis-go/v7"
	"github.com/ai8future/chassis-go/v7/registry"
)

func main() {
	chassis.RequireMajor(7)
	if err := registry.InitCLI(chassis.Version); err != nil {
		log.Fatalf("registry: %v", err)
	}

	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	showVersion := flag.Bool("v", false, "show version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Printf("rbatch %s\n", runner.GetVersion())
		os.Exit(0)
	}

	subcmd := os.Args[1]
	args := os.Args[2:]

	switch subcmd {
	case "run":
		cmdRun(args)
	case "spool":
		cmdSpool(args)
	case "watch":
		cmdWatch(args)
	case "resume":
		cmdResume(args)
	case "status":
		cmdStatus(args)
	case "-v", "--version":
		fmt.Printf("rbatch %s\n", runner.GetVersion())
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", subcmd)
		printUsage()
		os.Exit(1)
	}

	registry.ShutdownCLI(0)
}

func cmdRun(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	server := fs.String("server", "", "delegate to rserve at this address")
	concurrency := fs.Int("concurrency", 0, "override manifest concurrency")
	threshold := fs.Int("threshold", 0, "override budget threshold percentage")
	onBudget := fs.String("on-budget", "", "override budget action (stop|wait|ask)")
	maxWait := fs.String("max-wait", "", "override max wait duration")
	dryRun := fs.Bool("dry-run", false, "show execution plan without running")
	verbose := fs.Bool("v", false, "verbose logging")
	fs.Parse(args)

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: rbatch run <manifest.json> [flags]")
		os.Exit(1)
	}

	manifestPath := fs.Arg(0)
	m, err := batch.LoadManifest(manifestPath)
	if err != nil {
		log.Fatalf("load manifest: %v", err)
	}

	// Apply CLI overrides
	if *concurrency > 0 {
		m.Concurrency = *concurrency
	}
	if *threshold > 0 {
		m.Budget.ThresholdPct = *threshold
	}
	if *onBudget != "" {
		m.Budget.OnBudget = *onBudget
	}
	if *maxWait != "" {
		m.Budget.MaxWait = *maxWait
	}

	if *dryRun {
		printDryRun(m)
		return
	}

	exec, cleanup := createExecutor(*server, *verbose)
	if cleanup != nil {
		defer cleanup()
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	br := batch.NewBatchRunner(m, exec)
	br.OnEvent = func(event batch.BatchEvent) {
		switch event.Type {
		case "job_start":
			fmt.Fprintf(os.Stderr, "  starting: %s\n", event.JobName)
		case "job_complete":
			cost := 0.0
			if event.Result != nil {
				cost = event.Result.Cost
			}
			fmt.Fprintf(os.Stderr, "  complete: %s ($%.2f)\n", event.JobName, cost)
		case "job_fail":
			errMsg := ""
			if event.Result != nil {
				errMsg = event.Result.Error
			}
			fmt.Fprintf(os.Stderr, "  FAILED:   %s (%s)\n", event.JobName, errMsg)
		}
	}

	result := br.Run(ctx)

	// Write results
	batchDir, err := batch.BatchDir(m.Name)
	if err == nil {
		batch.WriteSummary(batchDir, result)
	}

	batch.PrintBatchSummary(result)

	if result.JobsFailed > 0 {
		os.Exit(1)
	}
}

func cmdSpool(args []string) {
	fs := flag.NewFlagSet("spool", flag.ExitOnError)
	server := fs.String("server", "", "delegate to rserve")
	verbose := fs.Bool("v", false, "verbose logging")
	fs.Parse(args)

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: rbatch spool <directory>")
		os.Exit(1)
	}

	spool := batch.NewSpool(fs.Arg(0))
	if err := spool.Init(); err != nil {
		log.Fatalf("init spool: %v", err)
	}

	manifests, err := spool.Scan()
	if err != nil {
		log.Fatalf("scan spool: %v", err)
	}
	if len(manifests) == 0 {
		fmt.Println("no pending jobs found")
		return
	}

	exec, cleanup := createExecutor(*server, *verbose)
	if cleanup != nil {
		defer cleanup()
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	for _, m := range manifests {
		br := batch.NewBatchRunner(m, exec)
		result := br.Run(ctx)
		batch.PrintBatchSummary(result)
	}
}

func cmdWatch(args []string) {
	fmt.Fprintln(os.Stderr, "watch mode not yet implemented (requires fsnotify)")
	fmt.Fprintln(os.Stderr, "use 'rbatch spool' for one-shot processing")
	os.Exit(1)
}

func cmdResume(args []string) {
	fs := flag.NewFlagSet("resume", flag.ExitOnError)
	server := fs.String("server", "", "delegate to rserve")
	verbose := fs.Bool("v", false, "verbose logging")
	fs.Parse(args)

	var cpPath string
	if fs.NArg() > 0 {
		cpPath = fs.Arg(0)
	} else {
		home, err := os.UserHomeDir()
		if err != nil {
			log.Fatalf("home dir: %v", err)
		}
		batchesDir := filepath.Join(home, ".rcodegen", "batches")
		cpPath, err = batch.FindLatestCheckpoint(batchesDir)
		if err != nil {
			log.Fatalf("no checkpoint found: %v", err)
		}
	}

	cp, err := batch.LoadCheckpoint(cpPath)
	if err != nil {
		log.Fatalf("load checkpoint: %v", err)
	}

	fmt.Printf("Resuming batch %q (stopped: %s, %d jobs remaining)\n",
		cp.Batch, cp.Reason, len(cp.Snapshot.Pending))

	// Build manifest from pending jobs
	m := &batch.Manifest{
		Name:        cp.Batch,
		Concurrency: 2,
		Jobs:        cp.Snapshot.Pending,
	}

	exec, cleanup := createExecutor(*server, *verbose)
	if cleanup != nil {
		defer cleanup()
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	br := batch.NewBatchRunner(m, exec)
	result := br.Run(ctx)
	batch.PrintBatchSummary(result)
}

func cmdStatus(args []string) {
	home, err := os.UserHomeDir()
	if err != nil {
		log.Fatalf("home dir: %v", err)
	}
	batchesDir := filepath.Join(home, ".rcodegen", "batches")

	entries, err := os.ReadDir(batchesDir)
	if err != nil {
		fmt.Println("no batch history found")
		return
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		statePath := filepath.Join(batchesDir, entry.Name(), "state.json")
		if cp, err := batch.LoadCheckpoint(statePath); err == nil {
			fmt.Printf("  %s: stopped=%s, cost=$%.2f, pending=%d\n",
				cp.Batch, cp.Reason, cp.Snapshot.TotalCost, len(cp.Snapshot.Pending))
		}
		summaryPath := filepath.Join(batchesDir, entry.Name(), "summary.json")
		if data, err := os.ReadFile(summaryPath); err == nil {
			fmt.Printf("  %s: %s\n", entry.Name(), string(data[:min(len(data), 200)]))
		}
	}
}

func createExecutor(serverAddr string, verbose bool) (batch.Executor, func()) {
	if serverAddr != "" {
		exec, err := batch.NewRemoteExecutor(serverAddr)
		if err != nil {
			log.Fatalf("connect to rserve: %v", err)
		}
		return exec, func() { exec.Close() }
	}

	s, _, err := settings.LoadWithFallback()
	if err != nil {
		log.Fatalf("settings: %v", err)
	}
	return batch.NewLocalExecutor(s), nil
}

func printDryRun(m *batch.Manifest) {
	groups := batch.BuildSessionGroups(m.Jobs)
	fmt.Printf("Batch: %s\n", m.Name)
	fmt.Printf("Concurrency: %d\n", m.Concurrency)
	fmt.Printf("Jobs: %d\n", len(m.Jobs))
	fmt.Printf("Session groups: %d\n\n", len(groups))

	for _, g := range groups {
		label := g.Session
		if label == "" {
			label = "(standalone)"
		}
		fmt.Printf("  Group %s [%s]:\n", g.ID, label)
		for _, j := range g.Jobs {
			fmt.Printf("    %s: %s (%s)\n", j.Name, j.Task, j.Tool)
		}
	}
}

func printUsage() {
	fmt.Println("Usage: rbatch <command> [args]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  run <manifest.json>    Run a batch manifest")
	fmt.Println("  spool <directory>      Process pending jobs from a spool directory")
	fmt.Println("  watch <directory>      Watch a spool directory for new jobs")
	fmt.Println("  resume [state.json]    Resume from checkpoint")
	fmt.Println("  status [batch-name]    Show batch status")
	fmt.Println()
	fmt.Println("Flags:")
	fmt.Println("  -v    Show version")
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
```

**Step 2: Verify it compiles**

Run: `cd /Users/cliff/Desktop/_code/codegen_suite/rcodegen && go build ./cmd/rbatch/`
Expected: Compiles without errors

**Step 3: Commit**

```bash
git add cmd/rbatch/main.go
git commit -m "feat(rbatch): add CLI entrypoint with run, spool, resume, status subcommands"
```

---

### Task 12: Update Makefile and Build

**Files:**
- Modify: `Makefile`

**Step 1: Add rbatch to Makefile**

Add `rbatch` to the `all` target, add its build rule, add it to `clean`, and add the linux cross-compile target. The changes needed:

- Line 5: Add `rbatch` to `.PHONY`
- Line 7: Change `all: rclaude rcodex rgemini rcodegen rserve` to `all: rclaude rcodex rgemini rcodegen rserve rbatch`
- After rserve target (line 22), add:
```makefile
rbatch:
	go build -ldflags "$(LDFLAGS)" -o $(BINDIR)/rbatch ./cmd/rbatch
```
- Line 25: Add `rbatch_linux` to `linux` target
- After rserve_linux (line 40), add:
```makefile
rbatch_linux:
	GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(BINDIR)/rbatch_linux ./cmd/rbatch
```
- Update `clean` to include rbatch binaries

**Step 2: Build all**

Run: `cd /Users/cliff/Desktop/_code/codegen_suite/rcodegen && make`
Expected: All binaries build including `bin/rbatch`

**Step 3: Verify rbatch runs**

Run: `cd /Users/cliff/Desktop/_code/codegen_suite/rcodegen && ./bin/rbatch -v`
Expected: Shows version

**Step 4: Run all tests**

Run: `cd /Users/cliff/Desktop/_code/codegen_suite/rcodegen && make test`
Expected: All tests pass including new pkg/batch/ tests

**Step 5: Commit**

```bash
git add Makefile
git commit -m "feat(rbatch): add rbatch to Makefile build targets"
```

---

### Task 13: Update AGENTS.md

**Files:**
- Modify: `AGENTS.md`

**Step 1: Update compile docs**

Change line 18 to mention 5 binaries instead of 4:
```
make          # builds all 5 binaries (rclaude, rcodex, rgemini, rcodegen, rserve, rbatch) into bin/
```

Add `make rbatch  # build just rbatch` to the examples.

**Step 2: Commit**

```bash
git add AGENTS.md
git commit -m "docs: update AGENTS.md for rbatch binary"
```

---

### Task 14: Integration Test - Dry Run

**Files:**
- Create: `pkg/batch/integration_test.go`

**Step 1: Write integration test with mock executor**

```go
package batch

import (
	"context"
	"testing"
)

func TestIntegrationFullBatchRun(t *testing.T) {
	exec := &countingExecutor{remaining: 100}
	m := &Manifest{
		Name:        "integration-test",
		Concurrency: 2,
		Budget:      BudgetConfig{ThresholdPct: 1, OnBudget: "stop", CheckInterval: "1s", MaxWait: "5s"},
		Jobs: []JobDef{
			{Name: "a1", Task: "audit", Tool: "claude", Dir: "/tmp", Session: "chain-1"},
			{Name: "a2", Task: "fix", Tool: "claude", Dir: "/tmp", Session: "chain-1"},
			{Name: "b1", Task: "audit", Tool: "codex", Dir: "/tmp", Session: "chain-2"},
			{Name: "c1", Task: "lint", Tool: "gemini", Dir: "/tmp"},
			{Name: "d1", Task: "update", Tool: "claude", Dir: "/tmp"},
		},
	}

	br := NewBatchRunner(m, exec)

	var events []string
	br.OnEvent = func(event BatchEvent) {
		events = append(events, event.Type+":"+event.JobName)
	}

	result := br.Run(context.Background())

	if result.Status != "completed" {
		t.Errorf("status = %q, want completed", result.Status)
	}
	if result.JobsTotal != 5 {
		t.Errorf("total = %d, want 5", result.JobsTotal)
	}
	if result.JobsSucceeded != 5 {
		t.Errorf("succeeded = %d, want 5", result.JobsSucceeded)
	}
	if result.TotalCost == 0 {
		t.Error("expected non-zero total cost")
	}

	// Verify events were emitted
	if len(events) == 0 {
		t.Error("no events emitted")
	}

	// Verify chain ordering: a1 must complete before a2 starts
	a1Complete := -1
	a2Start := -1
	for i, e := range events {
		if e == "job_complete:a1" {
			a1Complete = i
		}
		if e == "job_start:a2" {
			a2Start = i
		}
	}
	if a1Complete >= 0 && a2Start >= 0 && a2Start < a1Complete {
		t.Error("a2 started before a1 completed — session chain violated")
	}
}

func TestIntegrationBudgetStop(t *testing.T) {
	exec := &countingExecutor{remaining: 2} // Below threshold
	m := &Manifest{
		Name:        "budget-test",
		Concurrency: 1,
		Budget:      BudgetConfig{ThresholdPct: 5, OnBudget: "stop"},
		Jobs: []JobDef{
			{Name: "a", Task: "task", Tool: "claude"},
			{Name: "b", Task: "task", Tool: "claude"},
		},
	}

	br := NewBatchRunner(m, exec)
	result := br.Run(context.Background())

	if result.Status != "stopped" {
		t.Errorf("status = %q, want stopped", result.Status)
	}
	if result.StopReason != "budget_threshold" {
		t.Errorf("reason = %q, want budget_threshold", result.StopReason)
	}
}
```

**Step 2: Run integration tests**

Run: `cd /Users/cliff/Desktop/_code/codegen_suite/rcodegen && go test ./pkg/batch/ -v -run TestIntegration -timeout 30s`
Expected: PASS

**Step 3: Commit**

```bash
git add pkg/batch/integration_test.go
git commit -m "test(rbatch): add integration tests for full batch run and budget stop"
```

---

### Task 15: Version Bump, CHANGELOG, Final Build

**Step 1: Read VERSION**

Run: `cat VERSION`

**Step 2: Increment version and update CHANGELOG**

Add entry describing the new `rbatch` binary and `pkg/batch` package.

**Step 3: Build all**

Run: `make clean && make`

**Step 4: Run all tests**

Run: `make test`

**Step 5: Commit and push**

```bash
git add -A
git commit -m "feat: add rbatch batch job runner with session chains, budget checking, and spool support

- New cmd/rbatch binary with run, spool, watch, resume, status subcommands
- pkg/batch: manifest parsing, job queue, scheduler, checkpoint, budget, reporter, spool
- Local executor (in-process via pkg/runner) and remote executor (gRPC to rserve)
- Session-aware concurrency: chains run sequentially, independent groups in parallel
- Budget checking via existing status-only tracking with configurable stop/wait/ask
- Checkpoint and resume for crash recovery and budget pauses

Claude:Opus 4.6"
git push
```
