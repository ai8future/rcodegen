package runner

import (
	"io"
	"log/slog"

	"rcodegen/pkg/colors"
)

// Re-export color constants from colors package for backwards compatibility.
// New code should import rcodegen/pkg/colors directly.
const (
	Bold    = colors.Bold
	Dim     = colors.Dim
	Red     = colors.Red
	Green   = colors.Green
	Yellow  = colors.Yellow
	Magenta = colors.Magenta
	Cyan    = colors.Cyan
	White   = colors.White
	Reset   = colors.Reset
)

// Display formatting constants
const (
	MaxDisplayTaskLen = 50 // Max length for task display in banner
	TruncatedTaskLen  = 47 // Length before adding "..."
	MaxDisplayDescLen = 50 // Max length for description display
	MaxDescPrefixLen  = 47 // Length before adding "..."
)

// Config holds the runtime configuration for any tool
type Config struct {
	// Common fields
	Task          string            // The task/prompt to execute
	TaskShortcut  string            // The shortcut name if a shortcut was used
	WorkDirs      []string          // Working directories (supports multiple codebases)
	Codebase      string            // Codebase name from -c flag (used in report filenames)
	OutputDir     string            // Custom output directory (replaces _rcodegen)
	Model         string            // Model to use
	OutputJSON    bool              // Output as newline-delimited JSON
	StatsJSON     bool              // Output run statistics as JSON at completion
	StatusOnly    bool              // Just show status and exit
	UseLock       bool              // Use file lock to queue instances
	DeleteOld     bool              // Delete previous reports after run
	RequireReview bool              // Skip if previous report unreviewed
	NoRunLog      bool              // Suppress .runlog.md file generation
	OriginalCmd   string            // Original command string for display
	Vars          map[string]string // User-defined variables from -x flags

	// Recursive directory scanning
	Recursive     bool   // Enable recursive directory scanning (-r)
	RecurseLevels int    // Depth of scan (--levels, default 1)
	DirList       string // Comma-separated subdirectory names (--list)
	DirAll        string // Comma-separated directories: run all git repos within (--dir-all)

	// Tool-specific fields (only some tools use these)
	MaxBudget     string // Claude: max budget in USD
	Effort        string // Claude/Codex: reasoning effort level
	TrackStatus   bool   // Codex: track credit usage before/after
	NoTrackStatus bool   // User explicitly disabled status tracking via -S flag
	SessionID     string // Session ID for resuming previous session
	Flash         bool   // Gemini: use flash model variant
	ImagePath     string // Gemini: input image file to include in request

	// Logging
	Verbose bool         // Enable verbose/debug logging
	Logger  *slog.Logger // Structured logger (nil-safe: callers should check before use)

	// Execution control
	DryRun bool // If true, show what would be executed without running
	Force  bool // If true, bypass VERSION state check and run anyway

	// Output destination (nil defaults to os.Stdout)
	Output io.Writer

	// Stream event callback (used by gRPC server to forward events)
	OnStreamEvent StreamCallback

	// Stderr capture (if non-nil, stderr is written here instead of os.Stderr)
	Stderr io.Writer

	// Token usage (captured from stream output)
	TokenUsage   *TokenUsage // Token counts from run
	TotalCostUSD float64     // Total cost in USD
}

// NewConfig creates a new Config with default values
func NewConfig() *Config {
	return &Config{
		Vars: make(map[string]string),
	}
}
