package tracking

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// ClaudeStatus holds the parsed Claude Max credit status from Python script
type ClaudeStatus struct {
	SessionLeft      *int    `json:"session_left"`
	WeeklyAllLeft    *int    `json:"weekly_all_left"`
	WeeklySonnetLeft *int    `json:"weekly_sonnet_left"`
	SessionResets    *string `json:"session_resets"`
	WeeklyResets     *string `json:"weekly_resets"`
	Error            string  `json:"error"`
	Message          string  `json:"message"` // Human-readable error message
}

// IsITerm2Error returns true if the error is related to iTerm2 not being available
func (s *ClaudeStatus) IsITerm2Error() bool {
	return s.Error == "not_iterm2" || s.Error == "no_iterm2_package"
}

// GetClaudeStatus fetches the current Claude Max credit status using the Python script
func GetClaudeStatus() *ClaudeStatus {
	// Only look for scripts in trusted locations:
	// 1. Directory where executable lives
	// 2. ~/.rcodegen/scripts/ (user scripts directory)
	// Do NOT search current working directory - could be attacker-controlled

	var statusScript string

	// First, try executable directory
	scriptDir := GetScriptDir()
	if scriptDir != "" {
		statusScript = filepath.Join(scriptDir, "get_claude_status.py")
		if _, err := os.Stat(statusScript); err == nil {
			// Found in executable directory
			cmd := exec.Command(FindPython(), statusScript)
			return runClaudeStatusScript(cmd)
		}
	}

	// Second, try user scripts directory
	home, err := os.UserHomeDir()
	if err == nil {
		statusScript = filepath.Join(home, ".rcodegen", "scripts", "get_claude_status.py")
		if _, err := os.Stat(statusScript); err == nil {
			// Found in user scripts directory
			cmd := exec.Command(FindPython(), statusScript)
			return runClaudeStatusScript(cmd)
		}
	}

	return &ClaudeStatus{Error: "status script not found in trusted locations (executable dir or ~/.rcodegen/scripts/)"}
}

// ShowClaudeStatusOnly displays the current Claude Max credit status and exits
func ShowClaudeStatusOnly() {
	fmt.Printf("%sFetching Claude Max credit status...%s\n", Dim, Reset)
	status := GetClaudeStatus()

	if status.Error != "" {
		fmt.Println()
		fmt.Printf("%s%s══════════════════════════════════════════%s\n", Bold, Cyan, Reset)
		fmt.Printf("%s%s  Claude Max Credit Status%s\n", Bold, Cyan, Reset)
		fmt.Printf("%s%s══════════════════════════════════════════%s\n", Bold, Cyan, Reset)

		switch status.Error {
		case "not_iterm2":
			fmt.Printf("  %sCredit tracking unavailable%s\n\n", Yellow, Reset)
			fmt.Printf("  You're not running in iTerm2.\n")
			fmt.Printf("  Credit tracking requires iTerm2 with Python API.\n\n")
			fmt.Printf("  %sTo enable:%s\n", Dim, Reset)
			fmt.Printf("  1. Install iTerm2: %shttps://iterm2.com%s\n", Cyan, Reset)
			fmt.Printf("  2. Enable Python API in iTerm2:\n")
			fmt.Printf("     Preferences > General > Magic > Enable Python API\n")
			fmt.Printf("  3. Install Python package: %spip install iterm2%s\n", Green, Reset)
		case "no_iterm2_package":
			fmt.Printf("  %sCredit tracking unavailable%s\n\n", Yellow, Reset)
			fmt.Printf("  The iterm2 Python package is not installed.\n\n")
			fmt.Printf("  %sTo enable:%s\n", Dim, Reset)
			fmt.Printf("  1. Make sure iTerm2 Python API is enabled:\n")
			fmt.Printf("     Preferences > General > Magic > Enable Python API\n")
			fmt.Printf("  2. Install Python package: %spip install iterm2%s\n", Green, Reset)
		default:
			fmt.Printf("  %sError:%s Could not fetch status\n", Yellow, Reset)
			if status.Message != "" {
				fmt.Printf("  %s\n", status.Message)
			} else {
				fmt.Printf("  %s\n", status.Error)
			}
			fmt.Printf("\n  %sNote:%s This requires iTerm2 with Python API enabled.\n", Dim, Reset)
			fmt.Printf("  See /tmp/rclaude_status_debug.txt for details.\n")
		}
		fmt.Printf("%s%s══════════════════════════════════════════%s\n", Bold, Cyan, Reset)
		return
	}

	fmt.Println()
	fmt.Printf("%s%s══════════════════════════════════════════%s\n", Bold, Cyan, Reset)
	fmt.Printf("%s%s  Claude Max Credit Status%s\n", Bold, Cyan, Reset)
	fmt.Printf("%s%s══════════════════════════════════════════%s\n", Bold, Cyan, Reset)

	hasData := status.SessionLeft != nil || status.WeeklyAllLeft != nil || status.WeeklySonnetLeft != nil

	if hasData {
		// Session limit (5-hour rolling window)
		if status.SessionLeft != nil {
			resets := ""
			if status.SessionResets != nil {
				resets = fmt.Sprintf(" %sresets %s%s", Dim, *status.SessionResets, Reset)
			}
			fmt.Printf("  %sSession:%s      %s%s%%%s left%s\n", Dim, Reset, Green, FormatCredit(status.SessionLeft), Reset, resets)
		}

		// Weekly all models
		if status.WeeklyAllLeft != nil {
			resets := ""
			if status.WeeklyResets != nil {
				resets = fmt.Sprintf(" %sresets %s%s", Dim, *status.WeeklyResets, Reset)
			}
			fmt.Printf("  %sWeekly:%s       %s%s%%%s left%s\n", Dim, Reset, Green, FormatCredit(status.WeeklyAllLeft), Reset, resets)
		}

		// Weekly Sonnet only
		if status.WeeklySonnetLeft != nil {
			fmt.Printf("  %sSonnet only:%s  %s%s%%%s left\n", Dim, Reset, Green, FormatCredit(status.WeeklySonnetLeft), Reset)
		}
	} else {
		fmt.Printf("  %sCredit data not available%s\n", Yellow, Reset)
		fmt.Printf("  %sCheck /tmp/rclaude_status_debug.txt for raw output%s\n", Dim, Reset)
	}
	fmt.Printf("%s%s══════════════════════════════════════════%s\n", Bold, Cyan, Reset)
}

// PrintClaudeStatusBefore prints the credit status before a task
func PrintClaudeStatusBefore(status *ClaudeStatus) {
	hasData := status.SessionLeft != nil || status.WeeklyAllLeft != nil
	if hasData {
		if status.SessionLeft != nil {
			fmt.Printf("  %sSession:%s      %s%% left\n", Dim, Reset, FormatCredit(status.SessionLeft))
		}
		if status.WeeklyAllLeft != nil {
			fmt.Printf("  %sWeekly:%s       %s%% left\n", Dim, Reset, FormatCredit(status.WeeklyAllLeft))
		}
		fmt.Println()
	} else {
		fmt.Printf("  %sCredits:%s      %sdata not available yet%s\n\n", Dim, Reset, Yellow, Reset)
	}
}
