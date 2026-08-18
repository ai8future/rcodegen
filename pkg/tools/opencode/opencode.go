// Package opencode provides the opencode CLI tool implementation for the runner framework.
package opencode

import (
	"fmt"
	"os/exec"

	"rcodegen/pkg/runner"
	"rcodegen/pkg/settings"
)

var _ runner.Tool = (*Tool)(nil)

// Tool implements runner.Tool for the opencode CLI.
type Tool struct {
	settings *settings.Settings
}

// New creates a new opencode tool.
func New() *Tool {
	return &Tool{}
}

// SetSettings sets loaded settings on the tool.
func (t *Tool) SetSettings(s *settings.Settings) {
	t.settings = s
}

func (t *Tool) Name() string {
	return "ropencode"
}

func (t *Tool) BinaryName() string {
	return "opencode"
}

func (t *Tool) ReportDir() string {
	return "_rcodegen"
}

func (t *Tool) ReportPrefix() string {
	return "opencode-"
}

// ValidModels returns nil because opencode supports a dynamic provider/model namespace.
func (t *Tool) ValidModels() []string {
	return nil
}

// ValidEfforts returns nil: opencode has no reasoning-effort concept.
func (t *Tool) ValidEfforts() []string {
	return nil
}

func (t *Tool) DefaultModel() string {
	return settings.DefaultOpenCodeModel
}

func (t *Tool) DefaultModelSetting() string {
	if t.settings != nil && t.settings.Defaults.OpenCode.Model != "" {
		return t.settings.Defaults.OpenCode.Model
	}
	return t.DefaultModel()
}

// BuildCommand constructs the command for `opencode run`.
func (t *Tool) BuildCommand(cfg *runner.Config, workDir, task string) *exec.Cmd {
	args := []string{
		"run",
		"--dangerously-skip-permissions",
		"--format", "json",
	}

	if cfg.Model != "" {
		args = append(args, "-m", cfg.Model)
	}
	if workDir != "" {
		args = append(args, "--dir", workDir)
	}
	if cfg.SessionID != "" {
		args = append(args, "--session", cfg.SessionID)
	}
	args = append(args, task)

	return exec.Command("opencode", args...)
}

func (t *Tool) ShowStatus() {}

func (t *Tool) SupportsStatusTracking() bool {
	return false
}

func (t *Tool) CaptureStatusBefore() interface{} {
	return nil
}

func (t *Tool) CaptureStatusAfter() interface{} {
	return nil
}

func (t *Tool) PrintStatusSummary(before, after interface{}) {}

func (t *Tool) ToolSpecificFlags() []runner.FlagDef {
	return nil
}

func (t *Tool) ApplyToolDefaults(cfg *runner.Config) {
	if t.settings != nil && t.settings.Defaults.OpenCode.Model != "" {
		cfg.Model = t.settings.Defaults.OpenCode.Model
		return
	}
	if cfg.Model == "" {
		cfg.Model = t.DefaultModel()
	}
}

func (t *Tool) PrepareForExecution(cfg *runner.Config) {}

func (t *Tool) ValidateConfig(cfg *runner.Config) error {
	if cfg.Model == "" {
		return fmt.Errorf("model must be specified (use -m provider/model, e.g. %s)", settings.DefaultOpenCodeModel)
	}
	return nil
}

func (t *Tool) BannerTitle() string {
	return "ROPENCODE"
}

func (t *Tool) BannerSubtitle() string {
	return "opencode CLI"
}

func (t *Tool) PrintToolSpecificBannerFields(cfg *runner.Config) {}

func (t *Tool) PrintToolSpecificSummaryFields(cfg *runner.Config) {}

func (t *Tool) SecurityWarning() []string {
	return []string{
		"This tool runs opencode with --dangerously-skip-permissions,",
		"which auto-approves all tool operations.",
		"Use with caution and only on trusted codebases.",
	}
}

func (t *Tool) ToolSpecificHelpSections() []runner.HelpSection {
	return []runner.HelpSection{
		{
			Title: "OpenCode Options",
			Lines: []string{
				"  Models use opencode's " + runner.Green + "provider/model" + runner.Reset + " syntax.",
				"  Examples: " + runner.Yellow + settings.DefaultOpenCodeModel + runner.Reset,
				"            " + runner.Yellow + "deepinfra/zai-org/GLM-4.6" + runner.Reset,
				"            " + runner.Yellow + "openai/gpt-4.1" + runner.Reset,
				"  Run " + runner.Green + "opencode providers login" + runner.Reset + " once per provider.",
			},
		},
	}
}

func (t *Tool) StatsJSONFields(cfg *runner.Config) map[string]interface{} {
	return map[string]interface{}{
		"model": cfg.Model,
	}
}

func (t *Tool) UsesStreamOutput() bool {
	return false
}

// ReportedUsage always reports nothing: the OpenCode CLI writes plain stdout
// with no usage channel to read.
func (t *Tool) ReportedUsage(res *runner.RunResult) (runner.RunUsage, bool) {
	return runner.RunUsage{}, false
}

func (t *Tool) RunLogFields(cfg *runner.Config) []string {
	return []string{
		"Model: " + cfg.Model,
	}
}
