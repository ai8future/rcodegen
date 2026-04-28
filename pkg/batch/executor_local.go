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
	"rcodegen/pkg/tools/kilocode"
	"rcodegen/pkg/tools/opencode"
	"rcodegen/pkg/tracking"

	"github.com/ai8future/chassis-go/v11/logz"
)

// ToolFactory creates a fresh runner.Tool instance for each job execution.
type ToolFactory func() runner.Tool

// LocalExecutor runs jobs in-process using the runner package.
type LocalExecutor struct {
	Settings      *settings.Settings
	ToolFactories map[string]ToolFactory
}

// NewLocalExecutor creates a LocalExecutor with default factories for
// claude, codex, gemini, opencode, and kilocode tools.
func NewLocalExecutor(s *settings.Settings) *LocalExecutor {
	return &LocalExecutor{
		Settings: s,
		ToolFactories: map[string]ToolFactory{
			"claude":   func() runner.Tool { return claude.New() },
			"codex":    func() runner.Tool { return codex.New() },
			"gemini":   func() runner.Tool { return gemini.New() },
			"kilocode": func() runner.Tool { return kilocode.New() },
			"opencode": func() runner.Tool { return opencode.New() },
		},
	}
}

// Execute runs a single job and returns the result.
func (e *LocalExecutor) Execute(ctx context.Context, job *JobDef, sessionID string) (*JobResult, error) {
	// Look up the tool factory.
	factory, ok := e.ToolFactories[job.Tool]
	if !ok {
		return nil, fmt.Errorf("unknown tool %q", job.Tool)
	}

	// Create a fresh tool instance for this job.
	tool := factory()

	// Inject settings if the tool supports it.
	if sa, ok := tool.(runner.SettingsAware); ok {
		sa.SetSettings(e.Settings)
	}

	// Build the runner config.
	var workDirs []string
	if job.Dir != "" {
		workDirs = []string{job.Dir}
	}

	cfg := &runner.Config{
		Task:     job.Task,
		WorkDirs: workDirs,
		Output:   io.Discard,
		Logger:   logz.New("warn"),
		Stderr:   &bytes.Buffer{},
	}

	// Apply tool defaults first (model, effort, budget from settings).
	tool.ApplyToolDefaults(cfg)

	// Then apply job-level overrides.
	if job.Model != "" {
		cfg.Model = job.Model
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

	// Execute the job.
	start := time.Now()
	r := runner.NewRunner(tool)
	result := r.RunWithContext(ctx, cfg)
	elapsed := time.Since(start)

	// TODO: Extract new session ID from tool output for proper session chaining.
	// Currently returns the incoming sessionID unchanged because RunResult does not
	// expose the session ID from the tool's response. This means session chaining
	// (sequential jobs in the same conversation) starts a fresh session each time.
	return &JobResult{
		ExitCode:  result.ExitCode,
		Cost:      result.TotalCostUSD,
		Duration:  elapsed.Truncate(time.Millisecond).String(),
		SessionID: sessionID,
	}, nil
}

// CheckBudget queries the Claude Max credit status and returns the
// remaining percentage. Returns -1 if status data is unavailable.
func (e *LocalExecutor) CheckBudget(ctx context.Context) (int, error) {
	status := tracking.GetClaudeStatus()
	if status.Error != "" {
		return -1, nil
	}

	if status.SessionLeft != nil {
		return *status.SessionLeft, nil
	}
	if status.WeeklyAllLeft != nil {
		return *status.WeeklyAllLeft, nil
	}

	return -1, nil
}
