// Package kilocode provides the kilocode CLI tool implementation for the runner framework.
package kilocode

import (
	"fmt"
	"os/exec"

	"rcodegen/pkg/runner"
	"rcodegen/pkg/settings"
)

var _ runner.Tool = (*Tool)(nil)

// Tool implements runner.Tool for the kilocode CLI.
type Tool struct {
	settings *settings.Settings
}

// New creates a new kilocode tool.
func New() *Tool {
	return &Tool{}
}

// SetSettings sets loaded settings on the tool.
func (t *Tool) SetSettings(s *settings.Settings) {
	t.settings = s
}

func (t *Tool) Name() string {
	return "rkilo"
}

func (t *Tool) BinaryName() string {
	return "kilocode"
}

func (t *Tool) ReportDir() string {
	return "_rcodegen"
}

func (t *Tool) ReportPrefix() string {
	return "kilocode-"
}

// ValidModels returns nil because kilocode supports a dynamic provider/model namespace.
func (t *Tool) ValidModels() []string {
	return nil
}

// ValidEfforts returns nil: kilocode has no reasoning-effort concept.
func (t *Tool) ValidEfforts() []string {
	return nil
}

func (t *Tool) DefaultModel() string {
	return settings.DefaultKiloCodeModel
}

func (t *Tool) DefaultModelSetting() string {
	if t.settings != nil && t.settings.Defaults.KiloCode.Model != "" {
		return t.settings.Defaults.KiloCode.Model
	}
	return t.DefaultModel()
}

// BuildCommand constructs the command for `kilocode run`.
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

	return exec.Command("kilocode", args...)
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
	if t.settings != nil && t.settings.Defaults.KiloCode.Model != "" {
		cfg.Model = t.settings.Defaults.KiloCode.Model
		return
	}
	if cfg.Model == "" {
		cfg.Model = t.DefaultModel()
	}
}

func (t *Tool) PrepareForExecution(cfg *runner.Config) {}

func (t *Tool) ValidateConfig(cfg *runner.Config) error {
	if cfg.Model == "" {
		return fmt.Errorf("model must be specified (use -m provider/model, e.g. %s)", settings.DefaultKiloCodeModel)
	}
	return nil
}

func (t *Tool) BannerTitle() string {
	return "RKILO"
}

func (t *Tool) BannerSubtitle() string {
	return "kilocode CLI"
}

func (t *Tool) PrintToolSpecificBannerFields(cfg *runner.Config) {}

func (t *Tool) PrintToolSpecificSummaryFields(cfg *runner.Config) {}

func (t *Tool) SecurityWarning() []string {
	return []string{
		"This tool runs kilocode with --dangerously-skip-permissions,",
		"which auto-approves all tool operations.",
		"Use with caution and only on trusted codebases.",
	}
}

func (t *Tool) ToolSpecificHelpSections() []runner.HelpSection {
	return []runner.HelpSection{
		{
			Title: "KiloCode Options",
			Lines: []string{
				"  Models use kilocode's " + runner.Green + "provider/model" + runner.Reset + " syntax.",
				"  Examples: " + runner.Yellow + settings.DefaultKiloCodeModel + runner.Reset,
				"            " + runner.Yellow + "deepinfra/zai-org/GLM-4.6" + runner.Reset,
				"            " + runner.Yellow + "openai/gpt-4.1" + runner.Reset,
				"  Run " + runner.Green + "kilocode auth login" + runner.Reset + " once per provider.",
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

func (t *Tool) RunLogFields(cfg *runner.Config) []string {
	return []string{
		"Model: " + cfg.Model,
	}
}
