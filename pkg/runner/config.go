package runner

// ANSI color codes for terminal output
const (
	Bold    = "\033[1m"
	Dim     = "\033[2m"
	Red     = "\033[31m"
	Green   = "\033[32m"
	Yellow  = "\033[33m"
	Magenta = "\033[35m"
	Cyan    = "\033[36m"
	White   = "\033[37m"
	Reset   = "\033[0m"
)

// Config holds the runtime configuration for any tool
type Config struct {
	// Common fields
	Task          string            // The task/prompt to execute
	TaskShortcut  string            // The shortcut name if a shortcut was used
	WorkDirs      []string          // Working directories (supports multiple codebases)
	Codebase      string            // Codebase name from -c flag (used in report filenames)
	OutputDir     string            // Custom output directory (replaces _claude/_codex)
	Model         string            // Model to use
	OutputJSON    bool              // Output as newline-delimited JSON
	StatsJSON     bool              // Output run statistics as JSON at completion
	StatusOnly    bool              // Just show status and exit
	UseLock       bool              // Use file lock to queue instances
	DeleteOld     bool              // Delete previous reports after run
	RequireReview bool              // Skip if previous report unreviewed
	OriginalCmd   string            // Original command string for display
	Vars          map[string]string // User-defined variables from -x flags

	// Tool-specific fields (only some tools use these)
	MaxBudget   string // Claude: max budget in USD
	Effort      string // Codex: reasoning effort level
	TrackStatus bool   // Codex: track credit usage before/after
	SessionID   string // Session ID for resuming previous session

	// Execution control
	DryRun bool // If true, show what would be executed without running

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
