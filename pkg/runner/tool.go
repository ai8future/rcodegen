// Package runner provides a unified framework for running AI code automation tools.
package runner

import (
	"os/exec"
)

// Tool defines the interface that each AI tool (codex, claude, gemini) must implement.
type Tool interface {
	// Name returns the tool's name (e.g., "codex", "claude", "gemini")
	Name() string

	// BinaryName returns the CLI binary name to execute
	BinaryName() string

	// ReportDir returns the directory name for reports (always "_rcodegen")
	ReportDir() string

	// ReportPrefix returns the tool-specific prefix for report filenames (e.g., "claude-", "codex-")
	ReportPrefix() string

	// ValidModels returns the list of valid model names for this tool
	ValidModels() []string

	// DefaultModel returns the default model name
	DefaultModel() string

	// DefaultModelSetting returns the default model from settings (tool-specific)
	DefaultModelSetting() string

	// BuildCommand constructs the exec.Cmd for running a task
	BuildCommand(cfg *Config, workDir, task string) *exec.Cmd

	// ShowStatus displays the tool's credit/usage status
	ShowStatus()

	// SupportsStatusTracking returns true if this tool supports before/after status tracking
	SupportsStatusTracking() bool

	// CaptureStatusBefore captures status before running tasks (if supported)
	CaptureStatusBefore() interface{}

	// CaptureStatusAfter captures status after running tasks (if supported)
	CaptureStatusAfter() interface{}

	// PrintStatusSummary prints status comparison in summary (if supported)
	PrintStatusSummary(before, after interface{})

	// ToolSpecificFlags returns tool-specific flag definitions
	ToolSpecificFlags() []FlagDef

	// ApplyToolDefaults applies tool-specific defaults from settings to config
	ApplyToolDefaults(cfg *Config)

	// PrepareForExecution is called after task validation, before execution
	// Use for expensive initialization that should only happen when running a task
	PrepareForExecution(cfg *Config)

	// ValidateConfig validates tool-specific configuration
	ValidateConfig(cfg *Config) error

	// BannerTitle returns the title for the startup banner
	BannerTitle() string

	// BannerSubtitle returns the subtitle for the startup banner
	BannerSubtitle() string

	// PrintToolSpecificBannerFields prints tool-specific fields in the banner
	PrintToolSpecificBannerFields(cfg *Config)

	// PrintToolSpecificSummaryFields prints tool-specific fields in the summary
	PrintToolSpecificSummaryFields(cfg *Config)

	// SecurityWarning returns the security warning text for this tool
	SecurityWarning() []string

	// ToolSpecificHelpSections returns tool-specific help text sections
	ToolSpecificHelpSections() []HelpSection

	// StatsJSONFields returns tool-specific fields for JSON stats output
	StatsJSONFields(cfg *Config) map[string]interface{}

	// UsesStreamOutput returns true if this tool outputs stream-json format
	// that should be parsed and formatted nicely (vs raw terminal output)
	UsesStreamOutput() bool

	// RunLogFields returns tool-specific fields for the .runlog file
	// Returns slice of "Key: Value" strings
	RunLogFields(cfg *Config) []string
}

// FlagDef defines a command-line flag
type FlagDef struct {
	Short       string // Short flag (e.g., "-e")
	Long        string // Long flag (e.g., "--effort")
	Description string // Help description
	TakesArg    bool   // Whether the flag takes an argument
	Default     string // Default value (for display)
	Target      string // Config field name to set
}

// HelpSection defines a section of help text
type HelpSection struct {
	Title string   // Section title
	Lines []string // Lines of help text
}
