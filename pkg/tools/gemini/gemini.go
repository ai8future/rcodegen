// Package gemini provides the Gemini CLI tool implementation for the runner framework.
package gemini

import (
	"fmt"
	"os/exec"

	"rcodegen/pkg/runner"
	"rcodegen/pkg/settings"
)

// Compile-time interface satisfaction check
var _ runner.Tool = (*Tool)(nil)

// Tool implements the runner.Tool interface for Gemini CLI
type Tool struct {
	settings *settings.Settings
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
	return "_gemini"
}

// ValidModels returns the list of valid model names
func (t *Tool) ValidModels() []string {
	return []string{"gemini-2.5-pro", "gemini-2.5-flash", "gemini-2.0-pro", "gemini-2.0-flash", "gemini-3"}
}

// DefaultModel returns the default model name
func (t *Tool) DefaultModel() string {
	return "gemini-2.5-pro"
}

// DefaultModelSetting returns the default model from settings
func (t *Tool) DefaultModelSetting() string {
	// Gemini doesn't have settings support yet, return default
	return t.DefaultModel()
}

// BuildCommand constructs the exec.Cmd for running a task
func (t *Tool) BuildCommand(cfg *runner.Config, workDir, task string) *exec.Cmd {
	args := []string{
		"-p", task,
		"--output-format", "stream-json",
	}

	// Only add model flag if different from default
	if cfg.Model != "" && cfg.Model != t.DefaultModel() {
		args = append(args, "-m", cfg.Model)
	}

	cmd := exec.Command("gemini", args...)

	// Set working directory
	if workDir != "" {
		cmd.Dir = workDir
	}

	return cmd
}

// ShowStatus displays Gemini usage status (not implemented)
func (t *Tool) ShowStatus() {
	fmt.Printf("  %sStatus tracking not available for Gemini%s\n", runner.Dim, runner.Reset)
}

// SupportsStatusTracking returns false - Gemini doesn't support status tracking yet
func (t *Tool) SupportsStatusTracking() bool {
	return false
}

// CaptureStatusBefore captures status before running tasks (not supported)
func (t *Tool) CaptureStatusBefore() interface{} {
	return nil
}

// CaptureStatusAfter captures status after running tasks (not supported)
func (t *Tool) CaptureStatusAfter() interface{} {
	return nil
}

// PrintStatusSummary prints status comparison (not supported)
func (t *Tool) PrintStatusSummary(before, after interface{}) {
	// No-op: Gemini doesn't support status tracking
}

// ToolSpecificFlags returns Gemini-specific flag definitions
func (t *Tool) ToolSpecificFlags() []runner.FlagDef {
	// No tool-specific flags for now
	return nil
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
	// No expensive initialization needed for Gemini
}

// ValidateConfig validates Gemini-specific configuration
func (t *Tool) ValidateConfig(cfg *runner.Config) error {
	// Validate model
	validModels := map[string]bool{
		"gemini-2.5-pro":   true,
		"gemini-2.5-flash": true,
		"gemini-2.0-pro":   true,
		"gemini-2.0-flash": true,
		"gemini-3":         true,
	}
	if !validModels[cfg.Model] {
		return fmt.Errorf("invalid model '%s'. Valid options: gemini-2.5-pro, gemini-2.5-flash, gemini-2.0-pro, gemini-2.0-flash, gemini-3", cfg.Model)
	}

	return nil
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
		"This tool runs Gemini CLI with automated prompts.",
		"Use with caution and only on trusted codebases.",
	}
}

// ToolSpecificHelpSections returns Gemini-specific help text sections
func (t *Tool) ToolSpecificHelpSections() []runner.HelpSection {
	// No Gemini-specific help sections for now
	return nil
}

// StatsJSONFields returns Gemini-specific fields for JSON stats output
func (t *Tool) StatsJSONFields(cfg *runner.Config) map[string]interface{} {
	return map[string]interface{}{}
}

// UsesStreamOutput returns true - Gemini uses stream-json format
func (t *Tool) UsesStreamOutput() bool {
	return true
}

// RunLogFields returns Gemini-specific fields for the .runlog file
func (t *Tool) RunLogFields(cfg *runner.Config) []string {
	return []string{
		"Model: " + cfg.Model,
	}
}
