package tracking

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// GeminiModelStatus holds usage info for a single model
type GeminiModelStatus struct {
	Name      string   `json:"name"`
	PctLeft   *float64 `json:"pct_left"`
	ResetsIn  *string  `json:"resets_in"`
	ResetsISO *string  `json:"resets_iso"`
}

// GeminiStatus holds the parsed Gemini usage status from Python script
type GeminiStatus struct {
	Models  []GeminiModelStatus `json:"models"`
	Tier    *string             `json:"tier"`
	Error   string              `json:"error"`
	Message string              `json:"message"`
}

// IsITerm2Error returns true if the error is related to iTerm2 not being available
func (s *GeminiStatus) IsITerm2Error() bool {
	return s.Error == "not_iterm2" || s.Error == "no_iterm2_package"
}

// GetModelStatus returns the status for a specific model name, or nil
func (s *GeminiStatus) GetModelStatus(model string) *GeminiModelStatus {
	for i := range s.Models {
		if s.Models[i].Name == model {
			return &s.Models[i]
		}
	}
	return nil
}

// GetGeminiStatus fetches the current Gemini usage status using the Python script
func GetGeminiStatus() *GeminiStatus {
	var statusScript string

	// First, try executable directory
	scriptDir := GetScriptDir()
	if scriptDir != "" {
		statusScript = filepath.Join(scriptDir, "get_gemini_status.py")
		if _, err := os.Stat(statusScript); err == nil {
			cmd := exec.Command(FindPython(), statusScript)
			return runGeminiStatusScript(cmd)
		}
	}

	// Second, try user scripts directory
	home, err := os.UserHomeDir()
	if err == nil {
		statusScript = filepath.Join(home, ".rcodegen", "scripts", "get_gemini_status.py")
		if _, err := os.Stat(statusScript); err == nil {
			cmd := exec.Command(FindPython(), statusScript)
			return runGeminiStatusScript(cmd)
		}
	}

	return &GeminiStatus{Error: "status script not found in trusted locations (executable dir or ~/.rcodegen/scripts/)"}
}

// runGeminiStatusScript executes the Gemini status script and returns the parsed result
func runGeminiStatusScript(cmd *exec.Cmd) *GeminiStatus {
	output, err := cmd.Output()
	if err != nil {
		return &GeminiStatus{Error: fmt.Sprintf("failed to run status script: %v", err)}
	}

	var status GeminiStatus
	if err := json.Unmarshal(output, &status); err != nil {
		return &GeminiStatus{Error: fmt.Sprintf("failed to parse status JSON: %v", err)}
	}

	return &status
}

// ShowGeminiStatusOnly displays the current Gemini usage status and exits
func ShowGeminiStatusOnly() {
	fmt.Printf("%sFetching Gemini usage status...%s\n", Dim, Reset)
	status := GetGeminiStatus()

	if status.Error != "" {
		fmt.Println()
		fmt.Printf("%s%s══════════════════════════════════════════%s\n", Bold, Cyan, Reset)
		fmt.Printf("%s%s  Gemini Usage Status%s\n", Bold, Cyan, Reset)
		fmt.Printf("%s%s══════════════════════════════════════════%s\n", Bold, Cyan, Reset)

		switch status.Error {
		case "not_iterm2":
			fmt.Printf("  %sUsage tracking unavailable%s\n\n", Yellow, Reset)
			fmt.Printf("  You're not running in iTerm2.\n")
			fmt.Printf("  Usage tracking requires iTerm2 with Python API.\n\n")
			fmt.Printf("  %sTo enable:%s\n", Dim, Reset)
			fmt.Printf("  1. Install iTerm2: %shttps://iterm2.com%s\n", Cyan, Reset)
			fmt.Printf("  2. Enable Python API in iTerm2:\n")
			fmt.Printf("     Preferences > General > Magic > Enable Python API\n")
			fmt.Printf("  3. Install Python package: %spip install iterm2%s\n", Green, Reset)
		case "no_iterm2_package":
			fmt.Printf("  %sUsage tracking unavailable%s\n\n", Yellow, Reset)
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
		}
		fmt.Printf("%s%s══════════════════════════════════════════%s\n", Bold, Cyan, Reset)
		return
	}

	fmt.Println()
	fmt.Printf("%s%s══════════════════════════════════════════%s\n", Bold, Cyan, Reset)
	fmt.Printf("%s%s  Gemini Usage Status%s\n", Bold, Cyan, Reset)
	fmt.Printf("%s%s══════════════════════════════════════════%s\n", Bold, Cyan, Reset)

	if status.Tier != nil {
		fmt.Printf("  %sTier:%s         %s\n", Dim, Reset, *status.Tier)
	}

	if len(status.Models) > 0 {
		for _, m := range status.Models {
			resets := ""
			if m.ResetsISO != nil {
				countdown := FormatResetsIn(m.ResetsISO)
				if countdown != "" {
					if m.ResetsIn != nil {
						resets = fmt.Sprintf(" %sresets %s (%s)%s", Dim, countdown, *m.ResetsIn, Reset)
					} else {
						resets = fmt.Sprintf(" %sresets %s%s", Dim, countdown, Reset)
					}
				}
			} else if m.ResetsIn != nil {
				resets = fmt.Sprintf(" %sresets in %s%s", Dim, *m.ResetsIn, Reset)
			}

			pctStr := "N/A"
			if m.PctLeft != nil {
				pctStr = fmt.Sprintf("%.1f", *m.PctLeft)
			}
			fmt.Printf("  %s%-22s%s %s%s%%%s left%s\n", Dim, m.Name, Reset, Green, pctStr, Reset, resets)
		}
	} else {
		fmt.Printf("  %sUsage data not available%s\n", Yellow, Reset)
		fmt.Printf("  %sCheck with RGEMINI_DEBUG=1 for raw output%s\n", Dim, Reset)
	}
	fmt.Printf("%s%s══════════════════════════════════════════%s\n", Bold, Cyan, Reset)
}

// PrintGeminiStatusBefore prints the Gemini usage status before a task
func PrintGeminiStatusBefore(status *GeminiStatus, model string) {
	if len(status.Models) == 0 {
		fmt.Printf("  %sUsage:%s        %sdata not available yet%s\n\n", Dim, Reset, Yellow, Reset)
		return
	}

	// Show the specific model if found, otherwise show first model
	ms := status.GetModelStatus(model)
	if ms == nil && len(status.Models) > 0 {
		ms = &status.Models[0]
	}
	if ms != nil && ms.PctLeft != nil {
		fmt.Printf("  %sUsage:%s        %.1f%% left\n\n", Dim, Reset, *ms.PctLeft)
	} else {
		fmt.Printf("  %sUsage:%s        %sdata not available yet%s\n\n", Dim, Reset, Yellow, Reset)
	}
}
