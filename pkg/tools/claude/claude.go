// Package claude provides the Claude Code tool implementation for the runner framework.
package claude

import (
	"fmt"
	"os/exec"
	"strconv"
	"sync"

	"rcodegen/pkg/runner"
	"rcodegen/pkg/settings"
	"rcodegen/pkg/tracking"
)

// Tool implements the runner.Tool interface for Claude Code CLI
type Tool struct {
	settings     *settings.Settings
	currentModel string // Track current model for status calculations

	// Thread-safe status caching using sync.Once
	checkOnce    sync.Once
	isClaudeMax  bool                   // True if user has Claude Max subscription
	cachedStatus *tracking.ClaudeStatus // Cached status from initial check
}

// New creates a new Claude tool
func New() *Tool {
	return &Tool{}
}

// checkClaudeMax checks if user has Claude Max subscription and caches the result
func (t *Tool) checkClaudeMax() {
	t.checkOnce.Do(func() {
		// Try to get status - if successful, user has Claude Max
		status := tracking.GetClaudeStatus()
		if status.Error == "" && (status.SessionLeft != nil || status.WeeklyAllLeft != nil) {
			t.isClaudeMax = true
			t.cachedStatus = status
		}
	})
}

// IsClaudeMax returns true if user has Claude Max subscription
func (t *Tool) IsClaudeMax() bool {
	t.checkClaudeMax()
	return t.isClaudeMax
}

// SetSettings sets the settings (called by runner after loading)
func (t *Tool) SetSettings(s *settings.Settings) {
	t.settings = s
}

// Name returns the tool's name
func (t *Tool) Name() string {
	return "rclaude"
}

// BinaryName returns the CLI binary name
func (t *Tool) BinaryName() string {
	return "claude"
}

// ReportDir returns the directory name for reports
func (t *Tool) ReportDir() string {
	return "_rcodegen"
}

// ReportPrefix returns the tool-specific prefix for report filenames
func (t *Tool) ReportPrefix() string {
	return "claude-"
}

// ValidModels returns the list of valid model names
func (t *Tool) ValidModels() []string {
	return []string{"sonnet", "opus", "haiku"}
}

// DefaultModel returns the default model name
func (t *Tool) DefaultModel() string {
	return "sonnet"
}

// DefaultModelSetting returns the default model from settings
func (t *Tool) DefaultModelSetting() string {
	if t.settings != nil && t.settings.Defaults.Claude.Model != "" {
		return t.settings.Defaults.Claude.Model
	}
	return t.DefaultModel()
}

// BuildCommand constructs the exec.Cmd for running a task
func (t *Tool) BuildCommand(cfg *runner.Config, workDir, task string) *exec.Cmd {
	// Store model for status tracking
	t.currentModel = cfg.Model

	var args []string

	// If we have a session ID, resume it instead of starting new
	if cfg.SessionID != "" {
		args = []string{
			"--resume", cfg.SessionID,
			"-p", task,
			"--dangerously-skip-permissions",
			"--model", cfg.Model,
			"--max-budget-usd", cfg.MaxBudget,
		}
	} else {
		// Don't use --no-session-persistence so sessions can be resumed
		args = []string{
			"-p", task,
			"--dangerously-skip-permissions",
			"--model", cfg.Model,
			"--max-budget-usd", cfg.MaxBudget,
		}
	}

	// Claude CLI requires stream-json output format for non-TTY environments
	// to show realtime output. Without this, output is buffered until completion.
	if cfg.OutputJSON {
		args = append(args, "--output-format", "json")
	} else {
		// Use stream-json for realtime output in non-TTY environments
		// stream-json requires --verbose
		args = append(args, "--output-format", "stream-json", "--verbose")
	}

	cmd := exec.Command("claude", args...)

	// Set working directory (Claude has no -C flag)
	if workDir != "" {
		cmd.Dir = workDir
	}

	return cmd
}

// ShowStatus displays Claude Max credit status
func (t *Tool) ShowStatus() {
	tracking.ShowClaudeStatusOnly()
}

// SupportsStatusTracking returns true - Claude supports before/after tracking via iTerm2
func (t *Tool) SupportsStatusTracking() bool {
	return true
}

// CaptureStatusBefore captures Claude Max credit status before tasks
func (t *Tool) CaptureStatusBefore() interface{} {
	// Use cached status if we already checked for Claude Max
	if t.cachedStatus != nil {
		status := t.cachedStatus
		t.cachedStatus = nil // Clear cache so we fetch fresh after
		return status
	}

	status := tracking.GetClaudeStatus()
	if status.Error != "" {
		// Show helpful message for iTerm2-related errors
		if status.IsITerm2Error() {
			fmt.Printf("  %sNote:%s Credit tracking requires iTerm2 with Python API\n", runner.Dim, runner.Reset)
		} else {
			fmt.Printf("  %sNote:%s Could not capture status before task\n", runner.Dim, runner.Reset)
		}
		return nil
	}
	return status
}

// CaptureStatusAfter captures Claude Max credit status after tasks
func (t *Tool) CaptureStatusAfter() interface{} {
	return tracking.GetClaudeStatus()
}

// PrintStatusSummary prints the Claude credit usage summary
func (t *Tool) PrintStatusSummary(before, after interface{}) {
	statusBefore, ok1 := before.(*tracking.ClaudeStatus)
	statusAfter, ok2 := after.(*tracking.ClaudeStatus)

	if !ok1 || !ok2 || statusBefore == nil || statusAfter == nil {
		return
	}

	hasBefore := statusBefore.SessionLeft != nil || statusBefore.WeeklyAllLeft != nil
	hasAfter := statusAfter.SessionLeft != nil || statusAfter.WeeklyAllLeft != nil

	if hasBefore && hasAfter {
		// Calculate session usage
		var sessionCost int
		if statusBefore.SessionLeft != nil && statusAfter.SessionLeft != nil {
			sessionCost = *statusBefore.SessionLeft - *statusAfter.SessionLeft
			if sessionCost < 0 {
				sessionCost = 0 // Reset happened during run
			}
		}
		resetsSession := ""
		if statusAfter.SessionResets != nil {
			resetsSession = fmt.Sprintf(" %sresets %s%s", runner.Dim, *statusAfter.SessionResets, runner.Reset)
		}

		// Weekly usage - use Sonnet-specific if running Sonnet model
		var weeklyBefore, weeklyAfter *int
		weeklyLabel := "Weekly:"
		if t.currentModel == "sonnet" && statusBefore.WeeklySonnetLeft != nil && statusAfter.WeeklySonnetLeft != nil {
			weeklyBefore = statusBefore.WeeklySonnetLeft
			weeklyAfter = statusAfter.WeeklySonnetLeft
			weeklyLabel = "Sonnet:"
		} else {
			weeklyBefore = statusBefore.WeeklyAllLeft
			weeklyAfter = statusAfter.WeeklyAllLeft
		}

		var weeklyCost int
		if weeklyBefore != nil && weeklyAfter != nil {
			weeklyCost = *weeklyBefore - *weeklyAfter
			if weeklyCost < 0 {
				weeklyCost = 0 // Reset happened during run
			}
		}
		resetsWeekly := ""
		if statusAfter.WeeklyResets != nil {
			resetsWeekly = fmt.Sprintf(" %sresets %s%s", runner.Dim, *statusAfter.WeeklyResets, runner.Reset)
		}

		// Print status lines
		fmt.Printf("  %sSession:%s      %s%% → %s%s%%%s%s\n", runner.Dim, runner.Reset,
			tracking.FormatCredit(statusBefore.SessionLeft), runner.Green, tracking.FormatCredit(statusAfter.SessionLeft),
			runner.Reset, resetsSession)
		fmt.Printf("  %s%-7s%s       %s%% → %s%s%%%s%s\n", runner.Dim, weeklyLabel, runner.Reset,
			tracking.FormatCredit(weeklyBefore), runner.Green, tracking.FormatCredit(weeklyAfter),
			runner.Reset, resetsWeekly)

		// Print effective cost for this run
		costLabel := "weekly"
		if t.currentModel == "sonnet" {
			costLabel = "sonnet"
		}
		fmt.Printf("  %s───────────────────────────────────────%s\n", runner.Dim, runner.Reset)
		fmt.Printf("  %sRun cost:%s     %s%d%%%s %s %s(%d%% session)%s\n",
			runner.Dim, runner.Reset, runner.Yellow, weeklyCost, runner.Reset,
			costLabel, runner.Dim, sessionCost, runner.Reset)
	} else {
		fmt.Printf("  %sCredits:%s      %sdata not available%s\n", runner.Dim, runner.Reset, runner.Yellow, runner.Reset)
	}
}

// ToolSpecificFlags returns Claude-specific flag definitions
func (t *Tool) ToolSpecificFlags() []runner.FlagDef {
	return []runner.FlagDef{
		{
			Short:       "-b",
			Long:        "--budget",
			Description: "Max budget in USD per run",
			TakesArg:    true,
			Default:     "10.00",
			Target:      "MaxBudget",
		},
		{
			Short:       "-s",
			Long:        "--status",
			Description: "Track credit usage before/after task",
			TakesArg:    false,
			Target:      "TrackStatus",
		},
		{
			Short:       "-S",
			Long:        "--no-status",
			Description: "Disable credit usage tracking",
			TakesArg:    false,
			Target:      "NoTrackStatus",
		},
	}
}

// ApplyToolDefaults applies Claude-specific defaults from settings
func (t *Tool) ApplyToolDefaults(cfg *runner.Config) {
	// Set default budget
	cfg.MaxBudget = "10.00"

	// Apply settings defaults if available
	if t.settings != nil {
		if t.settings.Defaults.Claude.Model != "" {
			cfg.Model = t.settings.Defaults.Claude.Model
		}
		if t.settings.Defaults.Claude.Budget != "" {
			cfg.MaxBudget = t.settings.Defaults.Claude.Budget
		}
	}

	// NOTE: Claude Max check is deferred to PrepareForExecution
	// to avoid slow iTerm2 API call during help/error display
}

// PrepareForExecution does expensive setup after task validation
func (t *Tool) PrepareForExecution(cfg *runner.Config) {
	// Default to tracking status for Claude Max users
	// This check also caches the status for later use
	if t.IsClaudeMax() {
		cfg.TrackStatus = true
	}
}

// ValidateConfig validates Claude-specific configuration
func (t *Tool) ValidateConfig(cfg *runner.Config) error {
	// Validate model
	validModels := map[string]bool{"opus": true, "sonnet": true, "haiku": true}
	if !validModels[cfg.Model] {
		return fmt.Errorf("invalid model '%s'. Valid options: opus, sonnet, haiku", cfg.Model)
	}

	// Validate budget is a positive number
	budget, err := strconv.ParseFloat(cfg.MaxBudget, 64)
	if err != nil {
		return fmt.Errorf("invalid budget '%s': must be a number (e.g., 10.00)", cfg.MaxBudget)
	}
	if budget <= 0 {
		return fmt.Errorf("invalid budget '%s': must be greater than 0", cfg.MaxBudget)
	}
	if budget > 1000 {
		return fmt.Errorf("invalid budget '%s': maximum is 1000.00", cfg.MaxBudget)
	}

	return nil
}

// BannerTitle returns the title for the startup banner
func (t *Tool) BannerTitle() string {
	return "rclaude - Claude Code Automation                              ║"
}

// BannerSubtitle returns the subtitle for the startup banner
func (t *Tool) BannerSubtitle() string {
	return "Claude Code CLI"
}

// PrintToolSpecificBannerFields prints Claude-specific fields in the banner
func (t *Tool) PrintToolSpecificBannerFields(cfg *runner.Config) {
	// Don't show budget for Claude Max users (subscription-based)
	if t.IsClaudeMax() {
		return
	}
	fmt.Printf("  %s%sBudget:%s        %s$%s%s per run\n", runner.Bold, runner.Green, runner.Reset, runner.Yellow, cfg.MaxBudget, runner.Reset)
}

// PrintToolSpecificSummaryFields prints Claude-specific fields in the summary
func (t *Tool) PrintToolSpecificSummaryFields(cfg *runner.Config) {
	// Don't show budget for Claude Max users (subscription-based)
	if t.IsClaudeMax() {
		return
	}
	fmt.Printf("  %sMax budget:%s   $%s\n", runner.Dim, runner.Reset, cfg.MaxBudget)
}

// SecurityWarning returns the security warning text
func (t *Tool) SecurityWarning() []string {
	return []string{
		"This tool runs Claude Code with --dangerously-skip-permissions,",
		"which disables all permission prompts.",
		"Use with caution and only on trusted codebases.",
	}
}

// ToolSpecificHelpSections returns Claude-specific help text sections
func (t *Tool) ToolSpecificHelpSections() []runner.HelpSection {
	return []runner.HelpSection{
		{
			Title: "Claude Options",
			Lines: []string{
				fmt.Sprintf("  %s-b%s, %s--budget%s %s<usd>%s    Max budget in USD per run %s(default: 10.00)%s",
					runner.Green, runner.Reset, runner.Green, runner.Reset, runner.Yellow, runner.Reset, runner.Dim, runner.Reset),
			},
		},
		{
			Title: "Status Options",
			Lines: []string{
				fmt.Sprintf("  %s-s%s, %s--status%s          Track credit usage before/after task %s(default: on for Claude Max)%s",
					runner.Green, runner.Reset, runner.Green, runner.Reset, runner.Dim, runner.Reset),
				fmt.Sprintf("  %s-S%s, %s--no-status%s       Disable credit usage tracking",
					runner.Green, runner.Reset, runner.Green, runner.Reset),
			},
		},
	}
}

// StatsJSONFields returns Claude-specific fields for JSON stats output
func (t *Tool) StatsJSONFields(cfg *runner.Config) map[string]interface{} {
	return map[string]interface{}{
		"max_budget": cfg.MaxBudget,
	}
}

// UsesStreamOutput returns true - Claude uses stream-json format
func (t *Tool) UsesStreamOutput() bool {
	return true
}

// RunLogFields returns Claude-specific fields for the .runlog file
func (t *Tool) RunLogFields(cfg *runner.Config) []string {
	fields := []string{
		"Model:  " + cfg.Model,
	}
	// Only include budget for non-Claude Max users
	if !t.IsClaudeMax() {
		fields = append(fields, "Budget: $"+cfg.MaxBudget)
	}
	return fields
}
