package batch

import (
	"context"
	"fmt"
	"strings"
	"time"

	"rcodegen/pkg/runner"
	"rcodegen/pkg/settings"
	"rcodegen/pkg/tools/claude"
	"rcodegen/pkg/tools/codex"
	"rcodegen/pkg/tools/gemini"
	"rcodegen/pkg/tools/kilocode"
	"rcodegen/pkg/tools/localai"
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
			"ollama":   func() runner.Tool { return localai.NewOllama() },
			"lmstudio": func() runner.Tool { return localai.NewLMStudio() },
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

	output := runner.NewBoundedBuffer(64 << 10)
	diagnostics := runner.NewBoundedBuffer(64 << 10)
	cfg := &runner.Config{
		Task:     job.Task,
		WorkDirs: workDirs,
		Output:   output,
		Logger:   logz.New("warn"),
		Stderr:   diagnostics,
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
	if err := runner.ValidateModel(tool, cfg.Model); err != nil {
		return nil, err
	}
	if err := runner.ValidateEffort(tool, cfg.Model, cfg.Effort); err != nil {
		return nil, err
	}
	if err := tool.ValidateConfig(cfg); err != nil {
		return nil, err
	}

	// Execute the job.
	start := time.Now()
	r := runner.NewRunner(tool)
	result := r.RunWithContext(ctx, cfg)
	elapsed := time.Since(start)

	// Stream-capable tools update result.SessionID from their init event. Tools
	// that do not expose a new session ID leave it empty (or preserve a resumed
	// session), so the batch runner only chains sessions the tool actually reports.
	errorMessage := ""
	if result.ExitCode != 0 {
		errorMessage = strings.TrimSpace(diagnostics.String())
		if errorMessage == "" && result.Error != nil {
			errorMessage = result.Error.Error()
		}
	}
	return &JobResult{
		ExitCode:        result.ExitCode,
		Cost:            result.TotalCostUSD,
		Duration:        elapsed.Truncate(time.Millisecond).String(),
		SessionID:       result.SessionID,
		Error:           errorMessage,
		Output:          output.String(),
		OutputTruncated: output.Truncated(),
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
