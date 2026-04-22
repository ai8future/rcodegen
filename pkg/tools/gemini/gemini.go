// Package gemini provides the Gemini CLI tool implementation for the runner framework.
package gemini

import (
	"fmt"
	"os/exec"

	"rcodegen/pkg/runner"
	"rcodegen/pkg/settings"
	"rcodegen/pkg/tracking"
)

// Compile-time interface satisfaction check
var _ runner.Tool = (*Tool)(nil)

// Tool implements the runner.Tool interface for Gemini CLI
type Tool struct {
	settings     *settings.Settings
	currentModel string // Track current model for status display
}

// New creates a new Gemini tool
func New() *Tool {
	return &Tool{}
}

// SetSettings sets the settings (called by runner after loading)
func (t *Tool) SetSettings(s *settings.Settings) {
	t.settings = s
}

// Name returns the tool's name
func (t *Tool) Name() string {
	return "rgemini"
}

// BinaryName returns the CLI binary name
func (t *Tool) BinaryName() string {
	return "gemini"
}

// ReportDir returns the directory name for reports
func (t *Tool) ReportDir() string {
	return "_rcodegen"
}

// ReportPrefix returns the tool-specific prefix for report filenames
func (t *Tool) ReportPrefix() string {
	return "gemini-"
}

// ValidModels returns the list of valid model names
func (t *Tool) ValidModels() []string {
	return []string{"gemini-2.5-pro", "gemini-2.5-flash", "gemini-2.5-flash-lite", "gemini-3-flash-preview", "gemini-3.1-pro-preview", "gemini-3.1-flash-image-preview", "banana"}
}

// DefaultModel returns the default model name
func (t *Tool) DefaultModel() string {
	return "gemini-3.1-pro-preview"
}

// DefaultModelSetting returns the default model from settings
func (t *Tool) DefaultModelSetting() string {
	// Gemini doesn't have settings support yet, return default
	return t.DefaultModel()
}

// BuildCommand constructs the exec.Cmd for running a task
func (t *Tool) BuildCommand(cfg *runner.Config, workDir, task string) *exec.Cmd {
	// Store model for status tracking
	t.currentModel = cfg.Model

	var args []string

	// Resume existing session if available
	if cfg.SessionID != "" {
		args = []string{
			"--resume", cfg.SessionID,
			"-p", task,
			"--output-format", "stream-json",
			"--yolo",
		}
	} else {
		args = []string{
			"-p", task,
			"--output-format", "stream-json",
			"--yolo",
		}
	}

	// Always pass model explicitly — the gemini CLI's own default may differ from ours
	if cfg.Model != "" {
		args = append(args, "-m", cfg.Model)
	}

	cmd := exec.Command("gemini", args...)

	// Set working directory
	if workDir != "" {
		cmd.Dir = workDir
	}

	return cmd
}

// ShowStatus displays Gemini usage status
func (t *Tool) ShowStatus() {
	tracking.ShowGeminiStatusOnly()
}

// SupportsStatusTracking returns true - Gemini supports before/after tracking via iTerm2
func (t *Tool) SupportsStatusTracking() bool {
	return true
}

// CaptureStatusBefore captures Gemini usage status before tasks
func (t *Tool) CaptureStatusBefore() interface{} {
	status := tracking.GetGeminiStatus()
	if status.Error != "" {
		if status.IsITerm2Error() {
			fmt.Printf("  %sNote:%s Usage tracking requires iTerm2 with Python API\n", runner.Dim, runner.Reset)
		} else {
			fmt.Printf("  %sNote:%s Could not capture status before task\n", runner.Dim, runner.Reset)
		}
		return nil
	}
	tracking.PrintGeminiStatusBefore(status, t.currentModel)
	return status
}

// CaptureStatusAfter captures Gemini usage status after tasks
func (t *Tool) CaptureStatusAfter() interface{} {
	return tracking.GetGeminiStatus()
}

// PrintStatusSummary prints the Gemini usage comparison
func (t *Tool) PrintStatusSummary(before, after interface{}) {
	statusBefore, ok1 := before.(*tracking.GeminiStatus)
	statusAfter, ok2 := after.(*tracking.GeminiStatus)

	if !ok1 || !ok2 || statusBefore == nil || statusAfter == nil {
		return
	}

	if len(statusBefore.Models) == 0 || len(statusAfter.Models) == 0 {
		fmt.Printf("  %sUsage:%s        %sdata not available%s\n", runner.Dim, runner.Reset, runner.Yellow, runner.Reset)
		return
	}

	// Show status for the current model
	mBefore := statusBefore.GetModelStatus(t.currentModel)
	mAfter := statusAfter.GetModelStatus(t.currentModel)

	if mBefore == nil && len(statusBefore.Models) > 0 {
		mBefore = &statusBefore.Models[0]
	}
	if mAfter == nil && len(statusAfter.Models) > 0 {
		mAfter = &statusAfter.Models[0]
	}

	if mBefore != nil && mAfter != nil && mBefore.PctLeft != nil && mAfter.PctLeft != nil {
		used := *mBefore.PctLeft - *mAfter.PctLeft
		if used < 0 {
			used = 0
		}

		resets := ""
		if mAfter.ResetsISO != nil {
			countdown := tracking.FormatResetsIn(mAfter.ResetsISO)
			if countdown != "" {
				resets = fmt.Sprintf(" %sresets %s%s", runner.Dim, countdown, runner.Reset)
			}
		} else if mAfter.ResetsIn != nil {
			resets = fmt.Sprintf(" %sresets in %s%s", runner.Dim, *mAfter.ResetsIn, runner.Reset)
		}

		fmt.Printf("  %sUsage:%s        %.1f%% → %s%.1f%%%s%s\n", runner.Dim, runner.Reset,
			*mBefore.PctLeft, runner.Green, *mAfter.PctLeft, runner.Reset, resets)
		fmt.Printf("  %s───────────────────────────────────────%s\n", runner.Dim, runner.Reset)
		fmt.Printf("  %sRun cost:%s     %s%.1f%%%s\n",
			runner.Dim, runner.Reset, runner.Yellow, used, runner.Reset)
	} else {
		fmt.Printf("  %sUsage:%s        %sdata not available%s\n", runner.Dim, runner.Reset, runner.Yellow, runner.Reset)
	}
}

// ToolSpecificFlags returns Gemini-specific flag definitions
func (t *Tool) ToolSpecificFlags() []runner.FlagDef {
	return []runner.FlagDef{
		{
			Long:        "--flash",
			Description: "Use gemini-3-flash-preview model",
			TakesArg:    false,
			Target:      "Flash",
		},
		{
			Short:       "-s",
			Long:        "--status",
			Description: "Track usage before/after task",
			TakesArg:    false,
			Target:      "TrackStatus",
		},
		{
			Short:       "-S",
			Long:        "--no-status",
			Description: "Disable usage tracking",
			TakesArg:    false,
			Target:      "NoTrackStatus",
		},
	}
}

// ApplyToolDefaults applies Gemini-specific defaults from settings
func (t *Tool) ApplyToolDefaults(cfg *runner.Config) {
	// Set default model if not specified
	if cfg.Model == "" {
		cfg.Model = t.DefaultModel()
	}
}

// PrepareForExecution does expensive setup after task validation
func (t *Tool) PrepareForExecution(cfg *runner.Config) {
	// Override model if --flash flag is set
	if cfg.Flash {
		cfg.Model = "gemini-3-flash-preview"
	}

	// Resolve model aliases
	if cfg.Model == "banana" {
		cfg.Model = "gemini-3.1-flash-image-preview"
	}

	// Store model early so CaptureStatusBefore can use it
	t.currentModel = cfg.Model
}

// ValidateConfig validates Gemini-specific configuration
func (t *Tool) ValidateConfig(cfg *runner.Config) error {
	// Validate model using shared helper
	return runner.ValidateModel(t, cfg.Model)
}

// BannerTitle returns the title for the startup banner
func (t *Tool) BannerTitle() string {
	return "RGEMINI"
}

// BannerSubtitle returns the subtitle for the startup banner
func (t *Tool) BannerSubtitle() string {
	return "Gemini Code Assistant"
}

// PrintToolSpecificBannerFields prints Gemini-specific fields in the banner
func (t *Tool) PrintToolSpecificBannerFields(cfg *runner.Config) {
	// No Gemini-specific banner fields for now
}

// PrintToolSpecificSummaryFields prints Gemini-specific fields in the summary
func (t *Tool) PrintToolSpecificSummaryFields(cfg *runner.Config) {
	// No Gemini-specific summary fields for now
}

// SecurityWarning returns the security warning text
func (t *Tool) SecurityWarning() []string {
	return []string{
		"This tool runs Gemini CLI with --yolo mode,",
		"which auto-approves all tool operations.",
		"Use with caution and only on trusted codebases.",
	}
}

// ToolSpecificHelpSections returns Gemini-specific help text sections
func (t *Tool) ToolSpecificHelpSections() []runner.HelpSection {
	return []runner.HelpSection{
		{
			Title: "Gemini Options",
			Lines: []string{
				"  " + runner.Green + "--flash" + runner.Reset + "            Use gemini-3-flash-preview instead of gemini-3.1-pro-preview",
			},
		},
		{
			Title: "Status Options",
			Lines: []string{
				fmt.Sprintf("  %s-s%s, %s--status%s          Track usage before/after task",
					runner.Green, runner.Reset, runner.Green, runner.Reset),
				fmt.Sprintf("  %s-S%s, %s--no-status%s       Disable usage tracking",
					runner.Green, runner.Reset, runner.Green, runner.Reset),
			},
		},
	}
}

// StatsJSONFields returns Gemini-specific fields for JSON stats output
func (t *Tool) StatsJSONFields(cfg *runner.Config) map[string]interface{} {
	return map[string]interface{}{}
}

// UsesStreamOutput returns true - Gemini uses stream-json format
func (t *Tool) UsesStreamOutput() bool {
	return true
}

// RunLogFields returns Gemini-specific fields for the .runlog.md file
func (t *Tool) RunLogFields(cfg *runner.Config) []string {
	return []string{
		"Model: " + cfg.Model,
	}
}
