package tracking

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// ANSI color codes
const (
	Bold   = "\033[1m"
	Dim    = "\033[2m"
	Green  = "\033[32m"
	Cyan   = "\033[36m"
	Yellow = "\033[33m"
	Reset  = "\033[0m"
)

// CreditStatus holds the parsed credit status from Python script
type CreditStatus struct {
	FiveHourLeft   *int    `json:"5h_left"`
	WeeklyLeft     *int    `json:"weekly_left"`
	FiveHourResets *string `json:"5h_resets"`
	WeeklyResets   *string `json:"weekly_resets"`
	Error          string  `json:"error"`
}

// FormatCredit formats a credit value for display
func FormatCredit(val *int) string {
	if val == nil {
		return "N/A"
	}
	return fmt.Sprintf("%d", *val)
}

// FindPython locates a working Python 3 interpreter
// Prioritizes specific versions where packages like iterm2 are likely installed
func FindPython() string {
	// Check specific Python versions first (where iterm2 is likely installed)
	// Then fall back to generic python3
	candidates := []string{
		// Specific versions first (homebrew)
		"/opt/homebrew/bin/python3.13",
		"/opt/homebrew/bin/python3.12",
		"/opt/homebrew/bin/python3.11",
		// Then check PATH
		"python3.13",
		"python3.12",
		"python3.11",
		"python3",
		// Common system locations
		"/usr/local/bin/python3",
		"/usr/bin/python3",
	}

	for _, candidate := range candidates {
		// If it's an absolute path, check if it exists
		if filepath.IsAbs(candidate) {
			if _, err := os.Stat(candidate); err == nil {
				return candidate
			}
		} else {
			// Check in PATH
			if path, err := exec.LookPath(candidate); err == nil {
				return path
			}
		}
	}

	// Fallback - will likely fail but gives a clear error
	return "python3"
}

// GetScriptDir returns the directory containing the executable
// Returns empty string if executable path cannot be determined
func GetScriptDir() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Dir(exe)
}

// GetStatus fetches the current Codex credit status using the Python script
func GetStatus() *CreditStatus {
	// Only look for scripts in trusted locations:
	// 1. Directory where executable lives
	// 2. ~/.rcodegen/scripts/ (user scripts directory)
	// Do NOT search current working directory - could be attacker-controlled

	var statusScript string

	// First, try executable directory
	scriptDir := GetScriptDir()
	if scriptDir != "" {
		statusScript = filepath.Join(scriptDir, "get_codex_status.py")
		if _, err := os.Stat(statusScript); err == nil {
			// Found in executable directory
			cmd := exec.Command(FindPython(), statusScript)
			return runStatusScript(cmd)
		}
	}

	// Second, try user scripts directory
	home, err := os.UserHomeDir()
	if err == nil {
		statusScript = filepath.Join(home, ".rcodegen", "scripts", "get_codex_status.py")
		if _, err := os.Stat(statusScript); err == nil {
			// Found in user scripts directory
			cmd := exec.Command(FindPython(), statusScript)
			return runStatusScript(cmd)
		}
	}

	return &CreditStatus{Error: "status script not found in trusted locations (executable dir or ~/.rcodegen/scripts/)"}
}

// runStatusScript executes the status script and returns the parsed result
func runStatusScript(cmd *exec.Cmd) *CreditStatus {
	output, err := cmd.Output()
	if err != nil {
		return &CreditStatus{Error: fmt.Sprintf("failed to run status script: %v", err)}
	}

	var status CreditStatus
	if err := json.Unmarshal(output, &status); err != nil {
		return &CreditStatus{Error: fmt.Sprintf("failed to parse status JSON: %v", err)}
	}

	return &status
}

// runClaudeStatusScript executes the Claude status script and returns the parsed result
func runClaudeStatusScript(cmd *exec.Cmd) *ClaudeStatus {
	output, err := cmd.Output()
	if err != nil {
		return &ClaudeStatus{Error: fmt.Sprintf("failed to run status script: %v", err)}
	}

	var status ClaudeStatus
	if err := json.Unmarshal(output, &status); err != nil {
		return &ClaudeStatus{Error: fmt.Sprintf("failed to parse status JSON: %v", err)}
	}

	return &status
}

// ShowStatusOnly displays the current Codex credit status and exits
func ShowStatusOnly() {
	fmt.Printf("%sFetching Codex credit status...%s\n", Dim, Reset)
	status := GetStatus()

	if status.Error != "" {
		fmt.Fprintf(os.Stderr, "%sError:%s Could not fetch status\n", Yellow, Reset)
		fmt.Fprintf(os.Stderr, "  %s\n", status.Error)
		os.Exit(1)
	}

	fmt.Println()
	fmt.Printf("%s%s══════════════════════════════════════════%s\n", Bold, Cyan, Reset)
	fmt.Printf("%s%s  Codex Credit Status%s\n", Bold, Cyan, Reset)
	fmt.Printf("%s%s══════════════════════════════════════════%s\n", Bold, Cyan, Reset)

	if status.FiveHourLeft != nil || status.WeeklyLeft != nil {
		resets5h := ""
		resetsWeekly := ""
		if status.FiveHourResets != nil {
			resets5h = fmt.Sprintf(" %sresets %s%s", Dim, *status.FiveHourResets, Reset)
		}
		if status.WeeklyResets != nil {
			resetsWeekly = fmt.Sprintf(" %sresets %s%s", Dim, *status.WeeklyResets, Reset)
		}
		fmt.Printf("  %s5h limit:%s     %s%s%%%s left%s\n", Dim, Reset, Green, FormatCredit(status.FiveHourLeft), Reset, resets5h)
		fmt.Printf("  %sWeekly:%s       %s%s%%%s left%s\n", Dim, Reset, Green, FormatCredit(status.WeeklyLeft), Reset, resetsWeekly)
	} else {
		fmt.Printf("  %sCredit data not available%s\n", Yellow, Reset)
	}
	fmt.Printf("%s%s══════════════════════════════════════════%s\n", Bold, Cyan, Reset)
}

// PrintStatusBefore prints the credit status before a task
func PrintStatusBefore(status *CreditStatus) {
	if status.FiveHourLeft != nil || status.WeeklyLeft != nil {
		fmt.Printf("  %s5h limit:%s     %s%% left\n", Dim, Reset, FormatCredit(status.FiveHourLeft))
		fmt.Printf("  %sWeekly limit:%s %s%% left\n\n", Dim, Reset, FormatCredit(status.WeeklyLeft))
	} else {
		fmt.Printf("  %sCredits:%s      %sdata not available yet%s\n\n", Dim, Reset, Yellow, Reset)
	}
}
