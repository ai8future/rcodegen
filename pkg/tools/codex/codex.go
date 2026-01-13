// Package codex provides the OpenAI Codex tool implementation for the runner framework.
package codex

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"rcodegen/pkg/runner"
	"rcodegen/pkg/settings"
	"rcodegen/pkg/tracking"
)

// Tool implements the runner.Tool interface for OpenAI Codex CLI
type Tool struct {
	settings *settings.Settings
}

// New creates a new Codex tool
func New() *Tool {
	return &Tool{}
}

// SetSettings sets the settings (called by runner after loading)
func (t *Tool) SetSettings(s *settings.Settings) {
	t.settings = s
}

// Name returns the tool's name
func (t *Tool) Name() string {
	return "rcodex"
}

// BinaryName returns the CLI binary name
func (t *Tool) BinaryName() string {
	return "codex"
}

// ReportDir returns the directory name for reports
func (t *Tool) ReportDir() string {
	return "_rcodegen"
}

// ReportPrefix returns the tool-specific prefix for report filenames
func (t *Tool) ReportPrefix() string {
	return "codex-"
}

// ValidModels returns the list of valid model names
func (t *Tool) ValidModels() []string {
	return []string{"gpt-5.2-codex", "gpt-4.1-codex", "gpt-4o-codex"}
}

// DefaultModel returns the default model name
func (t *Tool) DefaultModel() string {
	return "gpt-5.2-codex"
}

// DefaultModelSetting returns the default model from settings
func (t *Tool) DefaultModelSetting() string {
	if t.settings != nil && t.settings.Defaults.Codex.Model != "" {
		return t.settings.Defaults.Codex.Model
	}
	return t.DefaultModel()
}

// BuildCommand constructs the exec.Cmd for running a task
func (t *Tool) BuildCommand(cfg *runner.Config, workDir, task string) *exec.Cmd {
	// Use resume with PTY wrapper if we have a session ID
	if cfg.SessionID != "" {
		// Use the Python PTY wrapper for resume (handles terminal emulation)
		wrapperPath := t.findWrapper()
		args := []string{
			wrapperPath,
			cfg.SessionID,
			task,
			"--dangerously-bypass-approvals-and-sandbox",
			"--model", cfg.Model,
			"-c", fmt.Sprintf("model_reasoning_effort=\"%s\"", cfg.Effort),
		}
		if workDir != "" {
			args = append(args, "-C", workDir)
		}
		return exec.Command("python3", args...)
	}

	// Regular exec for new sessions
	args := []string{
		"exec",
		"--dangerously-bypass-approvals-and-sandbox",
		"--skip-git-repo-check",
		"--model", cfg.Model,
		"-c", fmt.Sprintf("model_reasoning_effort=\"%s\"", cfg.Effort),
	}

	if workDir != "" {
		args = append(args, "-C", workDir)
	}

	if cfg.OutputJSON {
		args = append(args, "--json")
	}

	args = append(args, task)

	return exec.Command("codex", args...)
}

func (t *Tool) findWrapper() string {
	const wrapperName = "codex_pty_wrapper.py"

	// 1. Check same directory as executable
	exe, err := os.Executable()
	if err == nil {
		path := filepath.Join(filepath.Dir(exe), wrapperName)
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}

	// 2. Check current working directory
	if cwd, err := os.Getwd(); err == nil {
		path := filepath.Join(cwd, wrapperName)
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}

	// 3. Check ~/.rcodegen/
	home := os.Getenv("HOME")
	path := filepath.Join(home, ".rcodegen", wrapperName)
	if _, err := os.Stat(path); err == nil {
		return path
	}

	// Fallback (mostly for dev environment if CWD check failed)
	return wrapperName
}

// ShowStatus displays Codex credit status
func (t *Tool) ShowStatus() {
	tracking.ShowStatusOnly()
}

// SupportsStatusTracking returns true - Codex supports before/after tracking
func (t *Tool) SupportsStatusTracking() bool {
	return true
}

// CaptureStatusBefore captures Codex credit status before tasks
func (t *Tool) CaptureStatusBefore() interface{} {
	status := tracking.GetStatus()
	if status.Error != "" {
		fmt.Printf("Warning: Could not capture status before task\n  Error: %s\n", status.Error)
		return nil
	}
	tracking.PrintStatusBefore(status)
	return status
}

// CaptureStatusAfter captures Codex credit status after tasks
func (t *Tool) CaptureStatusAfter() interface{} {
	return tracking.GetStatus()
}

// PrintStatusSummary prints the Codex credit usage summary
func (t *Tool) PrintStatusSummary(before, after interface{}) {
	statusBefore, ok1 := before.(*tracking.CreditStatus)
	statusAfter, ok2 := after.(*tracking.CreditStatus)

	if !ok1 || !ok2 || statusBefore == nil || statusAfter == nil {
		return
	}

	hasBefore := statusBefore.FiveHourLeft != nil || statusBefore.WeeklyLeft != nil
	hasAfter := statusAfter.FiveHourLeft != nil || statusAfter.WeeklyLeft != nil

	if hasBefore && hasAfter {
		resets5h := ""
		resetsWeekly := ""
		if statusBefore.FiveHourResets != nil {
			resets5h = fmt.Sprintf(" %sresets %s%s", runner.Dim, *statusBefore.FiveHourResets, runner.Reset)
		}
		if statusBefore.WeeklyResets != nil {
			resetsWeekly = fmt.Sprintf(" %sresets %s%s", runner.Dim, *statusBefore.WeeklyResets, runner.Reset)
		}

		// Calculate usage if we have the values
		used5h := ""
		usedWeekly := ""
		if statusBefore.FiveHourLeft != nil && statusAfter.FiveHourLeft != nil {
			used5h = fmt.Sprintf(" %s(used %d%%)%s", runner.Dim, *statusBefore.FiveHourLeft-*statusAfter.FiveHourLeft, runner.Reset)
		}
		if statusBefore.WeeklyLeft != nil && statusAfter.WeeklyLeft != nil {
			usedWeekly = fmt.Sprintf(" %s(used %d%%)%s", runner.Dim, *statusBefore.WeeklyLeft-*statusAfter.WeeklyLeft, runner.Reset)
		}

		fmt.Printf("  %s5h limit:%s     %s%% → %s%s%%%s%s%s\n", runner.Dim, runner.Reset,
			tracking.FormatCredit(statusBefore.FiveHourLeft), runner.Green, tracking.FormatCredit(statusAfter.FiveHourLeft),
			runner.Reset, used5h, resets5h)
		fmt.Printf("  %sWeekly:%s       %s%% → %s%s%%%s%s%s\n", runner.Dim, runner.Reset,
			tracking.FormatCredit(statusBefore.WeeklyLeft), runner.Green, tracking.FormatCredit(statusAfter.WeeklyLeft),
			runner.Reset, usedWeekly, resetsWeekly)
	} else {
		fmt.Printf("  %sCredits:%s      %sdata not available%s\n", runner.Dim, runner.Reset, runner.Yellow, runner.Reset)
	}
}

// ToolSpecificFlags returns Codex-specific flag definitions
func (t *Tool) ToolSpecificFlags() []runner.FlagDef {
	return []runner.FlagDef{
		{
			Short:       "-e",
			Long:        "--effort",
			Description: "Reasoning effort: low, medium, high, xhigh",
			TakesArg:    true,
			Default:     "xhigh",
			Target:      "Effort",
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

// ApplyToolDefaults applies Codex-specific defaults from settings
func (t *Tool) ApplyToolDefaults(cfg *runner.Config) {
	// Set default effort
	cfg.Effort = "xhigh"
	cfg.TrackStatus = true

	// Apply settings defaults if available
	if t.settings != nil {
		if t.settings.Defaults.Codex.Model != "" {
			cfg.Model = t.settings.Defaults.Codex.Model
		}
		if t.settings.Defaults.Codex.Effort != "" {
			cfg.Effort = t.settings.Defaults.Codex.Effort
		}
	}
}

// PrepareForExecution does expensive setup after task validation
func (t *Tool) PrepareForExecution(cfg *runner.Config) {
	// Codex doesn't need any deferred setup
}

// ValidateConfig validates Codex-specific configuration
func (t *Tool) ValidateConfig(cfg *runner.Config) error {
	// Codex accepts any model name, so no validation needed
	return nil
}

// BannerTitle returns the title for the startup banner
func (t *Tool) BannerTitle() string {
	return "rcodex - OpenAI Codex Automation                              ║"
}

// BannerSubtitle returns the subtitle for the startup banner
func (t *Tool) BannerSubtitle() string {
	return "Codex CLI"
}

// PrintToolSpecificBannerFields prints Codex-specific fields in the banner
func (t *Tool) PrintToolSpecificBannerFields(cfg *runner.Config) {
	fmt.Printf("  %s%sEffort:%s        %s%s%s\n", runner.Bold, runner.Green, runner.Reset, runner.Yellow, cfg.Effort, runner.Reset)
}

// PrintToolSpecificSummaryFields prints Codex-specific fields in the summary
func (t *Tool) PrintToolSpecificSummaryFields(cfg *runner.Config) {
	fmt.Printf("  %sEffort:%s       %s\n", runner.Dim, runner.Reset, cfg.Effort)
}

// SecurityWarning returns the security warning text
func (t *Tool) SecurityWarning() []string {
	return []string{
		"This tool runs Codex with --dangerously-bypass-approvals-and-sandbox,",
		"which disables all approval prompts and sandbox restrictions.",
		"Use with caution and only on trusted codebases.",
	}
}

// ToolSpecificHelpSections returns Codex-specific help text sections
func (t *Tool) ToolSpecificHelpSections() []runner.HelpSection {
	return []runner.HelpSection{
		{
			Title: "Codex Options",
			Lines: []string{
				fmt.Sprintf("  %s-e%s, %s--effort%s %s<lvl>%s    Reasoning effort: low, medium, high, xhigh %s(default: xhigh)%s",
					runner.Green, runner.Reset, runner.Green, runner.Reset, runner.Yellow, runner.Reset, runner.Dim, runner.Reset),
			},
		},
		{
			Title: "Status Options",
			Lines: []string{
				fmt.Sprintf("  %s-s%s, %s--status%s          Track credit usage before/after task %s(default: on)%s",
					runner.Green, runner.Reset, runner.Green, runner.Reset, runner.Dim, runner.Reset),
				fmt.Sprintf("  %s-S%s, %s--no-status%s       Disable credit usage tracking",
					runner.Green, runner.Reset, runner.Green, runner.Reset),
			},
		},
	}
}

// StatsJSONFields returns Codex-specific fields for JSON stats output
func (t *Tool) StatsJSONFields(cfg *runner.Config) map[string]interface{} {
	return map[string]interface{}{
		"effort":       cfg.Effort,
		"track_status": cfg.TrackStatus,
	}
}

// UsesStreamOutput returns false - Codex uses regular terminal output
func (t *Tool) UsesStreamOutput() bool {
	return false
}

// RunLogFields returns Codex-specific fields for the .runlog file
func (t *Tool) RunLogFields(cfg *runner.Config) []string {
	return []string{
		"Model:  " + cfg.Model,
		"Effort: " + cfg.Effort,
	}
}
