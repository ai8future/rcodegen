// Package settings handles loading and managing user configuration
// from ~/.rcodegen/settings.json, including interactive setup.
package settings

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	ConfigDirName  = ".rcodegen"
	ConfigFileName = "settings.json"
)

// TaskDef defines a task shortcut with its prompt
type TaskDef struct {
	Prompt string `json:"prompt"` // The prompt text to send to the AI
}

// CodexDefaults holds default settings for rcodex
type CodexDefaults struct {
	Model  string `json:"model"`  // Default model (e.g., "gpt-5.2-codex")
	Effort string `json:"effort"` // Default effort level (low, medium, high, xhigh)
}

// ClaudeDefaults holds default settings for rclaude
type ClaudeDefaults struct {
	Model  string `json:"model"`  // Default model (sonnet, opus, haiku)
	Budget string `json:"budget"` // Default max budget in USD
}

// GeminiDefaults holds default settings for rgemini
type GeminiDefaults struct {
	Model string `json:"model,omitempty"` // Default model (gemini-2.5-pro, etc.)
}

// Defaults holds default settings for all tools
type Defaults struct {
	Codex  CodexDefaults  `json:"codex"`
	Claude ClaudeDefaults `json:"claude"`
	Gemini GeminiDefaults `json:"gemini,omitempty"`
}

// Settings holds all configuration for rcodegen tools
type Settings struct {
	CodeDir         string             `json:"code_dir"`                    // Default code directory (supports ~ expansion)
	OutputDir       string             `json:"output_dir,omitempty"`        // Custom output directory (replaces _rcodegen)
	DefaultBuildDir string             `json:"default_build_dir,omitempty"` // Default output directory for build bundles
	Defaults        Defaults           `json:"defaults"`                    // Default settings for each tool
	Tasks           map[string]TaskDef `json:"tasks"`                       // Task shortcuts
}

// TaskConfig is the legacy format used by the rest of the codebase
type TaskConfig struct {
	Tasks          map[string]string
	ReportPatterns map[string]string
}

// GetConfigDir returns the path to the config directory (~/.rcodegen)
func GetConfigDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.Getenv("HOME") // fallback for legacy systems
	}
	return filepath.Join(home, ConfigDirName)
}

// GetConfigPath returns the full path to settings.json
func GetConfigPath() string {
	return filepath.Join(GetConfigDir(), ConfigFileName)
}

// expandTilde expands ~ to the user's home directory
func expandTilde(path string) string {
	if path == "" {
		return path
	}
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			home = os.Getenv("HOME") // fallback for legacy systems
			if home == "" {
				return path
			}
		}
		return filepath.Join(home, path[2:])
	}
	if path == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = os.Getenv("HOME")
			if home == "" {
				return path
			}
		}
		return home
	}
	return path
}

// Load reads settings from ~/.rcodegen/settings.json
// Returns nil and an error if the file doesn't exist or is invalid
func Load() (*Settings, error) {
	configPath := GetConfigPath()

	// Check file permissions for security
	info, err := os.Stat(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("settings file not found: %s", configPath)
		}
		return nil, fmt.Errorf("failed to stat settings file: %w", err)
	}

	// Warn if settings file is world-writable (security risk)
	mode := info.Mode().Perm()
	if mode&0002 != 0 { // world-writable
		fmt.Fprintf(os.Stderr, "Warning: settings file %s is world-writable (mode %o). This is a security risk.\n", configPath, mode)
		fmt.Fprintf(os.Stderr, "Run: chmod 600 %s\n", configPath)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %w", configPath, err)
	}

	var settings Settings
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, fmt.Errorf("invalid JSON in %s: %w", configPath, err)
	}

	// Expand tilde in paths
	settings.CodeDir = expandTilde(settings.CodeDir)
	settings.OutputDir = expandTilde(settings.OutputDir)
	settings.DefaultBuildDir = expandTilde(settings.DefaultBuildDir)

	return &settings, nil
}

// GetDefaultSettings returns settings with sensible defaults
// Note: CodeDir is left empty - user should configure this in settings.json
func GetDefaultSettings() *Settings {
	return &Settings{
		CodeDir:         "", // User must configure this
		DefaultBuildDir: "", // Optional, will use CodeDir if not set
		Defaults: Defaults{
			Codex: CodexDefaults{
				Model:  "gpt-5.2-codex",
				Effort: "xhigh",
			},
			Claude: ClaudeDefaults{
				Model:  "sonnet",
				Budget: "10.00",
			},
			Gemini: GeminiDefaults{
				Model: "gemini-3",
			},
		},
		Tasks: make(map[string]TaskDef),
	}
}

// LoadWithFallback tries to load settings, falling back to defaults if not found
// Returns the settings (possibly with defaults) and whether the config file existed
func LoadWithFallback() (*Settings, bool) {
	settings, err := Load()
	if err != nil {
		return GetDefaultSettings(), false
	}
	// Fill in any missing defaults
	if settings.Defaults.Codex.Model == "" {
		settings.Defaults.Codex.Model = "gpt-5.2-codex"
	}
	if settings.Defaults.Codex.Effort == "" {
		settings.Defaults.Codex.Effort = "xhigh"
	}
	if settings.Defaults.Claude.Model == "" {
		settings.Defaults.Claude.Model = "sonnet"
	}
	if settings.Defaults.Claude.Budget == "" {
		settings.Defaults.Claude.Budget = "10.00"
	}
	if settings.Defaults.Gemini.Model == "" {
		settings.Defaults.Gemini.Model = "gemini-3-pro-preview"
	}
	if settings.DefaultBuildDir == "" {
		settings.DefaultBuildDir = settings.CodeDir // Default to code_dir if not set
	}
	// Merge default tasks - user tasks override defaults
	if settings.Tasks == nil {
		settings.Tasks = make(map[string]TaskDef)
	}
	for name, task := range GetDefaultTasks() {
		if _, exists := settings.Tasks[name]; !exists {
			settings.Tasks[name] = task
		}
	}
	return settings, true
}

// ToTaskConfig converts Settings to the legacy TaskConfig format
// Auto-generates report filename patterns based on task name
// toolPrefix is the tool-specific prefix (e.g., "claude-", "codex-", "gemini-")
// Also handles {report_file} and {codebase} placeholder substitution in prompts
func (s *Settings) ToTaskConfig(codebaseName, toolPrefix string) *TaskConfig {
	cfg := &TaskConfig{
		Tasks:          make(map[string]string),
		ReportPatterns: make(map[string]string),
	}

	for name, task := range s.Tasks {
		// Auto-generate pattern: {codebase}-{tool}{taskname}-
		// Example: myproject-claude-audit-
		pattern := codebaseName + "-" + toolPrefix + name + "-"

		// Replace {report_file} and {codebase} placeholders in prompt
		prompt := task.Prompt
		prompt = strings.ReplaceAll(prompt, "{codebase}", codebaseName)
		if pattern != "" {
			reportFile := pattern + "[date].md"
			prompt = strings.ReplaceAll(prompt, "{report_file}", reportFile)
		}

		cfg.Tasks[name] = prompt
		cfg.ReportPatterns[name] = pattern
	}

	return cfg
}

// GetCodeDir returns the configured code directory with ~ expanded
func (s *Settings) GetCodeDir() string {
	return s.CodeDir
}

// IsCodeDirConfigured returns true if code_dir is set in settings
func (s *Settings) IsCodeDirConfigured() bool {
	return s.CodeDir != ""
}

// PrintCodeDirWarning prints a warning when code_dir is not configured
func PrintCodeDirWarning() {
	configPath := GetConfigPath()
	fmt.Fprintf(os.Stderr, "\n")
	fmt.Fprintf(os.Stderr, "\033[33mWarning:\033[0m No code directory configured.\n")
	fmt.Fprintf(os.Stderr, "\n")
	fmt.Fprintf(os.Stderr, "  The \033[36m-c/--codebase\033[0m flag requires \033[35mcode_dir\033[0m to be set in settings.\n")
	fmt.Fprintf(os.Stderr, "\n")
	fmt.Fprintf(os.Stderr, "  To configure, add to \033[35m%s\033[0m:\n", configPath)
	fmt.Fprintf(os.Stderr, "    \033[32m\"code_dir\": \"~/path/to/your/code\"\033[0m\n")
	fmt.Fprintf(os.Stderr, "\n")
	fmt.Fprintf(os.Stderr, "  Or use \033[36m-d/--dir\033[0m to specify an absolute path instead.\n")
	fmt.Fprintf(os.Stderr, "\n")
}

// PrintSetupInstructions prints helpful setup instructions when settings.json doesn't exist
func PrintSetupInstructions(toolName string) {
	configPath := GetConfigPath()
	configDir := GetConfigDir()

	fmt.Fprintf(os.Stderr, "\n")
	fmt.Fprintf(os.Stderr, "\033[1m\033[36mSetup Required:\033[0m\n")
	fmt.Fprintf(os.Stderr, "  No settings file found at: \033[35m%s\033[0m\n\n", configPath)
	fmt.Fprintf(os.Stderr, "  Create the settings file:\n")
	fmt.Fprintf(os.Stderr, "    \033[32mmkdir -p %s\033[0m\n", configDir)
	fmt.Fprintf(os.Stderr, "    \033[32mcp settings.json.example %s\033[0m\n\n", configPath)
	fmt.Fprintf(os.Stderr, "  Or create manually with your code directory:\n")
	fmt.Fprintf(os.Stderr, "    \033[33m{\n")
	fmt.Fprintf(os.Stderr, "      \"code_dir\": \"~/path/to/your/code\",\n")
	fmt.Fprintf(os.Stderr, "      \"tasks\": { ... }\n")
	fmt.Fprintf(os.Stderr, "    }\033[0m\n\n")
	fmt.Fprintf(os.Stderr, "  See settings.json.example for the full task definitions.\n")
	fmt.Fprintf(os.Stderr, "\n")
}

// ANSI color codes for interactive setup
const (
	bold    = "\033[1m"
	dim     = "\033[2m"
	green   = "\033[32m"
	cyan    = "\033[36m"
	yellow  = "\033[33m"
	magenta = "\033[35m"
	reset   = "\033[0m"
)

// GetDefaultTasks returns the default task definitions
// Report filename patterns are auto-generated from task name: {codebase}-{taskname}-[date].md
func GetDefaultTasks() map[string]TaskDef {
	return map[string]TaskDef{
		"audit": {
			Prompt: "You ARE allowed to write reports to the {report_dir} directory. Run a complete audit of this code (including security!). Don't spend a lot of time on this (less than 10% of work) but as you investigate, establish an overall 100-point grade. Write a detailed report you store in {report_dir}. INCLUDE PATCH-READY DIFFS. Save your file as {report_file} (use {timestamp} for [date]). IMPORTANT: At the very top of the report, include these two lines exactly:\nDate Created: [full timestamp]\nTOTAL_SCORE: [your grade]/100\nDO NOT EDIT CODE.",
		},
		"test": {
			Prompt: "You ARE allowed to write reports to the {report_dir} directory. Analyze the codebase and propose comprehensive unit tests for untested code. Don't spend a lot of time on this (less than 10% of work) but as you investigate, establish an overall 100-point grade. Write a detailed report you store in {report_dir}. INCLUDE PATCH-READY DIFFS. Save your file as {report_file} (use {timestamp} for [date]). IMPORTANT: At the very top of the report, include these two lines exactly:\nDate Created: [full timestamp]\nTOTAL_SCORE: [your grade]/100\nDO NOT EDIT CODE.",
		},
		"fix": {
			Prompt: "You ARE allowed to write reports to the {report_dir} directory. Analyze the codebase for bugs, issues, and code smells. Don't spend a lot of time on this (less than 10% of work) but as you investigate, establish an overall 100-point grade. Fix any problems found and explain what was changed. INCLUDE PATCH-READY DIFFS. Write a detailed report you store in {report_dir}. Save your file as {report_file} (use {timestamp} for [date]). IMPORTANT: At the very top of the report, include these two lines exactly:\nDate Created: [full timestamp]\nTOTAL_SCORE: [your grade]/100\nDO NOT EDIT CODE.",
		},
		"refactor": {
			Prompt: "You ARE allowed to write reports to the {report_dir} directory. Review the codebase for opportunities to improve code quality, reduce duplication, and improve maintainability. Don't spend a lot of time on this (less than 10% of work) but as you investigate, establish an overall 100-point grade. No need to include patch-ready diffs. Write a detailed report you store in {report_dir}. Save your file as {report_file} (use {timestamp} for [date]). IMPORTANT: At the very top of the report, include these two lines exactly:\nDate Created: [full timestamp]\nTOTAL_SCORE: [your grade]/100\nDO NOT EDIT CODE.",
		},
		"quick": {
			Prompt: "You ARE allowed to write reports to the {report_dir} directory. Run a quick but complete analysis of this codebase. Don't spend a lot of time on this (less than 10% of work) but as you investigate, establish an overall 100-point grade. Generate a SINGLE combined report in {report_dir} named {report_file} (use {timestamp} for [date]). The report should have 4 sections: (1) AUDIT - Security and code quality issues with PATCH-READY DIFFS, (2) TESTS - Proposed unit tests for untested code with PATCH-READY DIFFS, (3) FIXES - Bugs, issues, and code smells with fixes and PATCH-READY DIFFS, (4) REFACTOR - Opportunities to improve code quality (no diffs needed). IMPORTANT: At the very top of the report, include these two lines exactly:\nDate Created: [full timestamp]\nTOTAL_SCORE: [your grade]/100\nDO NOT EDIT CODE.",
		},
		"grade": {
			Prompt: "You ARE allowed to write reports to the {report_dir} directory. Grade the developer who wrote this code and assign a grade (100 being perfect). Then assign grades for all of the following categories and weights: Architecture & Design (25%), Security Practices (20%), Error Handling (15%), Testing (15%), Idioms & Style (15%), and Documentation (10%). Created a final combined score and call it: TOTAL_SCORE. Write a detailed report you store in {report_dir}. Save your file as {report_file} (use {timestamp} for [date]). At the very top of the report, below title, add \"Date Created:\" with the full timestamp of when the report was written. DO NOT EDIT CODE.",
		},
		"generate": {
			Prompt: "Generate {number} blog post ideas about {topic}. For each idea, provide a title and brief description.",
		},
		"study": {
			Prompt: "You ARE allowed to write reports to the {report_dir} directory. Run a complete study of this code - analyzing how it works, what it does, as well as how it interacts with other services and interacts with outside codebases. Look for motivations and try to understand notes in the code for why it does things certain ways. Write a detailed report you store in {report_dir}. Save your file as {report_file} (use {timestamp} for [date]). IMPORTANT: At the very top of the report, include this line exactly:\nDate Created: [full timestamp]\nDO NOT EDIT CODE.",
		},
	}
}

// RunInteractiveSetup runs an interactive setup wizard to create the settings file
// Returns the created settings and true if successful, nil and false if cancelled/failed
func RunInteractiveSetup() (*Settings, bool) {
	// Check if stdin is a terminal
	stat, _ := os.Stdin.Stat()
	if (stat.Mode() & os.ModeCharDevice) == 0 {
		fmt.Fprintln(os.Stderr, "Error: Standard input is not a terminal. Interactive setup cannot run.")
		fmt.Fprintln(os.Stderr, "Please create ~/.rcodegen/settings.json manually.")
		return nil, false
	}

	reader := bufio.NewReader(os.Stdin)

	fmt.Printf("\n%s%s╔════════════════════════════════════════════════════════════════╗%s\n", bold, cyan, reset)
	fmt.Printf("%s%s║  rcodegen - First Time Setup                                   ║%s\n", bold, cyan, reset)
	fmt.Printf("%s%s╚════════════════════════════════════════════════════════════════╝%s\n\n", bold, cyan, reset)

	fmt.Printf("No settings file found. Let's set one up!\n\n")

	// Ask about code directory
	fmt.Printf("%s%sWhere do you keep your code projects?%s\n", bold, green, reset)
	fmt.Printf("%s(Enter the parent directory containing your projects)%s\n", dim, reset)

	// Suggest common locations
	home, _ := os.UserHomeDir()
	if home == "" {
		home = os.Getenv("HOME")
	}
	suggestions := []string{
		filepath.Join(home, "code"),
		filepath.Join(home, "projects"),
		filepath.Join(home, "dev"),
		filepath.Join(home, "src"),
		filepath.Join(home, "workspace"),
	}

	// Find existing directories to suggest
	var existingSuggestions []string
	for _, s := range suggestions {
		if info, err := os.Stat(s); err == nil && info.IsDir() {
			existingSuggestions = append(existingSuggestions, s)
		}
	}

	if len(existingSuggestions) > 0 {
		fmt.Printf("\n%sDetected directories:%s\n", dim, reset)
		for i, s := range existingSuggestions {
			// Convert to tilde format for display
			display := strings.Replace(s, home, "~", 1)
			fmt.Printf("  %s%d.%s %s%s%s\n", dim, i+1, reset, magenta, display, reset)
		}
		fmt.Println()
	}

	defaultPath := "~/code"
	if len(existingSuggestions) > 0 {
		defaultPath = strings.Replace(existingSuggestions[0], home, "~", 1)
	}

	fmt.Printf("%sCode directory%s [%s%s%s]: ", bold, reset, yellow, defaultPath, reset)

	input, err := reader.ReadString('\n')
	if err != nil {
		fmt.Fprintf(os.Stderr, "\n%sError:%s reading input: %v\n", yellow, reset, err)
		return nil, false
	}

	codeDir := strings.TrimSpace(input)
	if codeDir == "" {
		codeDir = defaultPath
	}

	// Handle numeric selection
	if len(codeDir) == 1 && codeDir[0] >= '1' && codeDir[0] <= '9' {
		idx := int(codeDir[0] - '1')
		if idx < len(existingSuggestions) {
			codeDir = strings.Replace(existingSuggestions[idx], home, "~", 1)
		}
	}

	// Expand and validate the path
	expandedPath := expandTilde(codeDir)
	if info, err := os.Stat(expandedPath); err != nil || !info.IsDir() {
		fmt.Printf("\n%sDirectory does not exist: %s%s\n", yellow, expandedPath, reset)
		fmt.Printf("%sCreate it? [Y/n]: %s", bold, reset)

		confirm, _ := reader.ReadString('\n')
		confirm = strings.TrimSpace(strings.ToLower(confirm))

		if confirm == "" || confirm == "y" || confirm == "yes" {
			if err := os.MkdirAll(expandedPath, 0755); err != nil {
				fmt.Fprintf(os.Stderr, "%sError:%s creating directory: %v\n", yellow, reset, err)
				return nil, false
			}
			fmt.Printf("%sCreated: %s%s\n", green, expandedPath, reset)
		} else {
			fmt.Printf("%sSetup cancelled.%s\n", yellow, reset)
			return nil, false
		}
	}

	// ═══════════════════════════════════════════════════════════════════════════
	// Ask about rclaude defaults
	// ═══════════════════════════════════════════════════════════════════════════
	fmt.Printf("\n%s%s── rclaude (Claude Code) Defaults ──%s\n\n", bold, cyan, reset)

	fmt.Printf("%s%sDefault model for rclaude?%s\n", bold, green, reset)
	fmt.Printf("%sTip: Sonnet is recommended to maximize your included API credits.%s\n", dim, reset)
	fmt.Printf("%sOpus is more capable but costs more. Haiku is faster but less capable.%s\n\n", dim, reset)
	fmt.Printf("  %s1.%s %ssonnet%s %s(recommended - best value)%s\n", dim, reset, magenta, reset, dim, reset)
	fmt.Printf("  %s2.%s %sopus%s %s(most capable)%s\n", dim, reset, magenta, reset, dim, reset)
	fmt.Printf("  %s3.%s %shaiku%s %s(fastest, least capable)%s\n\n", dim, reset, magenta, reset, dim, reset)

	fmt.Printf("%sClaude model%s [%s1%s]: ", bold, reset, yellow, reset)
	claudeModelInput, _ := reader.ReadString('\n')
	claudeModelInput = strings.TrimSpace(claudeModelInput)

	claudeModel := "sonnet" // default
	switch claudeModelInput {
	case "", "1", "sonnet":
		claudeModel = "sonnet"
	case "2", "opus":
		claudeModel = "opus"
	case "3", "haiku":
		claudeModel = "haiku"
	default:
		// Accept direct input if it's a valid model name
		if claudeModelInput == "sonnet" || claudeModelInput == "opus" || claudeModelInput == "haiku" {
			claudeModel = claudeModelInput
		}
	}

	fmt.Printf("\n%s%sDefault max budget per run (USD)?%s\n", bold, green, reset)
	fmt.Printf("%sThis limits how much a single task can spend.%s\n\n", dim, reset)
	fmt.Printf("%sBudget%s [%s$10.00%s]: $", bold, reset, yellow, reset)

	claudeBudgetInput, _ := reader.ReadString('\n')
	claudeBudgetInput = strings.TrimSpace(claudeBudgetInput)
	claudeBudget := "10.00"
	if claudeBudgetInput != "" {
		// Remove $ prefix if present
		claudeBudgetInput = strings.TrimPrefix(claudeBudgetInput, "$")
		claudeBudget = claudeBudgetInput
	}

	// ═══════════════════════════════════════════════════════════════════════════
	// Ask about rcodex defaults
	// ═══════════════════════════════════════════════════════════════════════════
	fmt.Printf("\n%s%s── rcodex (OpenAI Codex) Defaults ──%s\n\n", bold, cyan, reset)

	fmt.Printf("%s%sDefault model for rcodex?%s\n", bold, green, reset)
	fmt.Printf("%sThe model name used with OpenAI Codex CLI.%s\n\n", dim, reset)
	fmt.Printf("%sCodex model%s [%sgpt-5.2-codex%s]: ", bold, reset, yellow, reset)

	codexModelInput, _ := reader.ReadString('\n')
	codexModelInput = strings.TrimSpace(codexModelInput)
	codexModel := "gpt-5.2-codex"
	if codexModelInput != "" {
		codexModel = codexModelInput
	}

	fmt.Printf("\n%s%sDefault reasoning effort?%s\n", bold, green, reset)
	fmt.Printf("%sHigher effort = better results but slower and uses more credits.%s\n\n", dim, reset)
	fmt.Printf("  %s1.%s %sxhigh%s %s(recommended - most thorough)%s\n", dim, reset, magenta, reset, dim, reset)
	fmt.Printf("  %s2.%s %shigh%s\n", dim, reset, magenta, reset)
	fmt.Printf("  %s3.%s %smedium%s\n", dim, reset, magenta, reset)
	fmt.Printf("  %s4.%s %slow%s %s(fastest)%s\n\n", dim, reset, magenta, reset, dim, reset)

	fmt.Printf("%sEffort level%s [%s1%s]: ", bold, reset, yellow, reset)
	effortInput, _ := reader.ReadString('\n')
	effortInput = strings.TrimSpace(effortInput)

	codexEffort := "xhigh" // default
	switch effortInput {
	case "", "1", "xhigh":
		codexEffort = "xhigh"
	case "2", "high":
		codexEffort = "high"
	case "3", "medium":
		codexEffort = "medium"
	case "4", "low":
		codexEffort = "low"
	default:
		// Accept direct input if it's a valid effort level
		if effortInput == "xhigh" || effortInput == "high" || effortInput == "medium" || effortInput == "low" {
			codexEffort = effortInput
		}
	}

	// Create the settings
	settings := &Settings{
		CodeDir: codeDir, // Store with tilde for portability
		Defaults: Defaults{
			Codex: CodexDefaults{
				Model:  codexModel,
				Effort: codexEffort,
			},
			Claude: ClaudeDefaults{
				Model:  claudeModel,
				Budget: claudeBudget,
			},
		},
		Tasks: GetDefaultTasks(),
	}

	// Create config directory
	configDir := GetConfigDir()
	if err := os.MkdirAll(configDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "%sError:%s creating config directory: %v\n", yellow, reset, err)
		return nil, false
	}

	// Write settings file
	configPath := GetConfigPath()
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "%sError:%s encoding settings: %v\n", yellow, reset, err)
		return nil, false
	}

	if err := os.WriteFile(configPath, data, 0600); err != nil {
		fmt.Fprintf(os.Stderr, "%sError:%s writing settings: %v\n", yellow, reset, err)
		return nil, false
	}

	// Success message
	fmt.Printf("\n%s%s────────────────────────────────────────────────────────────────%s\n", dim, cyan, reset)
	fmt.Printf("%s%sSetup Complete!%s\n\n", bold, green, reset)
	fmt.Printf("  %sSettings saved to:%s  %s%s%s\n", dim, reset, magenta, configPath, reset)
	fmt.Printf("  %sCode directory:%s     %s%s%s\n\n", dim, reset, magenta, codeDir, reset)
	fmt.Printf("  %s%srclaude defaults:%s\n", bold, cyan, reset)
	fmt.Printf("    %sModel:%s   %s%s%s\n", dim, reset, magenta, claudeModel, reset)
	fmt.Printf("    %sBudget:%s  %s$%s%s\n\n", dim, reset, magenta, claudeBudget, reset)
	fmt.Printf("  %s%srcodex defaults:%s\n", bold, cyan, reset)
	fmt.Printf("    %sModel:%s   %s%s%s\n", dim, reset, magenta, codexModel, reset)
	fmt.Printf("    %sEffort:%s  %s%s%s\n", dim, reset, magenta, codexEffort, reset)
	fmt.Printf("  %sTasks configured:%s   %s%d default tasks%s\n", dim, reset, yellow, len(settings.Tasks), reset)
	fmt.Printf("\n%sYou can edit %s to customize tasks.%s\n", dim, configPath, reset)
	fmt.Printf("%s%s────────────────────────────────────────────────────────────────%s\n\n", dim, cyan, reset)

	// Return settings with expanded path for immediate use
	settings.CodeDir = expandedPath
	return settings, true
}

// LoadOrSetup tries to load settings, or runs interactive setup if not found
// Returns the settings and whether setup was successful
func LoadOrSetup() (*Settings, bool) {
	settings, err := Load()
	if err == nil {
		// Merge default tasks - user tasks override defaults
		if settings.Tasks == nil {
			settings.Tasks = make(map[string]TaskDef)
		}
		for name, task := range GetDefaultTasks() {
			if _, exists := settings.Tasks[name]; !exists {
				settings.Tasks[name] = task
			}
		}
		return settings, true
	}

	// Settings not found - run interactive setup
	return RunInteractiveSetup()
}
