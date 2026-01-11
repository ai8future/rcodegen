package runner

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// PrintStartupBanner displays the run configuration at startup
func PrintStartupBanner(tool Tool, cfg *Config) {
	fmt.Printf("\n%s%s╔════════════════════════════════════════════════════════════════╗%s\n", Bold, Cyan, Reset)
	fmt.Printf("%s%s║  %s%s\n", Bold, Cyan, tool.BannerTitle(), Reset)
	fmt.Printf("%s%s╚════════════════════════════════════════════════════════════════╝%s\n\n", Bold, Cyan, Reset)

	// Task
	fmt.Printf("  %s%sTask:%s          ", Bold, Green, Reset)
	if cfg.TaskShortcut != "" {
		fmt.Printf("%s%s%s", Yellow, cfg.TaskShortcut, Reset)
		if cfg.TaskShortcut == "suite" {
			fmt.Printf(" %s(audit → test → fix → refactor)%s", Dim, Reset)
		}
	} else {
		// Truncate custom task if too long
		task := cfg.Task
		if len(task) > MaxDisplayTaskLen {
			task = task[:TruncatedTaskLen] + "..."
		}
		fmt.Printf("%s\"%s\"%s", Yellow, task, Reset)
	}
	fmt.Println()

	// Model (common to all tools)
	fmt.Printf("  %s%sModel:%s         %s%s%s\n", Bold, Green, Reset, Magenta, cfg.Model, Reset)

	// Tool-specific fields (budget, effort, etc.)
	tool.PrintToolSpecificBannerFields(cfg)

	// Codebases
	fmt.Printf("  %s%sCodebases:%s     ", Bold, Green, Reset)
	if len(cfg.WorkDirs) == 0 {
		cwd, _ := os.Getwd()
		fmt.Printf("%s%s%s %s(current dir)%s\n", Magenta, filepath.Base(cwd), Reset, Dim, Reset)
	} else if len(cfg.WorkDirs) == 1 {
		fmt.Printf("%s%s%s\n", Magenta, cfg.WorkDirs[0], Reset)
	} else {
		fmt.Printf("%s%d projects%s\n", Yellow, len(cfg.WorkDirs), Reset)
		for i, dir := range cfg.WorkDirs {
			fmt.Printf("                 %s%d.%s %s%s%s\n", Dim, i+1, Reset, Magenta, filepath.Base(dir), Reset)
		}
	}

	// Options
	var enabledOpts []string
	if cfg.UseLock {
		enabledOpts = append(enabledOpts, fmt.Sprintf("%s--lock%s", Green, Reset))
	}
	if cfg.DeleteOld {
		enabledOpts = append(enabledOpts, fmt.Sprintf("%s--delete-old%s", Green, Reset))
	}
	if cfg.RequireReview {
		enabledOpts = append(enabledOpts, fmt.Sprintf("%s--require-review%s", Green, Reset))
	}
	if cfg.OutputJSON {
		enabledOpts = append(enabledOpts, fmt.Sprintf("%s--json%s", Green, Reset))
	}
	if cfg.StatsJSON {
		enabledOpts = append(enabledOpts, fmt.Sprintf("%s--stats-json%s", Green, Reset))
	}
	if cfg.TrackStatus {
		enabledOpts = append(enabledOpts, fmt.Sprintf("%s--status%s", Green, Reset))
	}

	if len(enabledOpts) > 0 {
		fmt.Printf("  %s%sOptions:%s       %s\n", Bold, Green, Reset, strings.Join(enabledOpts, ", "))
	}

	// Variables
	if len(cfg.Vars) > 0 {
		fmt.Printf("  %s%sVariables:%s     ", Bold, Green, Reset)
		var varPairs []string
		for k, v := range cfg.Vars {
			varPairs = append(varPairs, fmt.Sprintf("%s%s%s=%s\"%s\"%s", Green, k, Reset, Yellow, v, Reset))
		}
		fmt.Printf("%s\n", strings.Join(varPairs, ", "))
	}

	fmt.Printf("\n%s%s────────────────────────────────────────────────────────────────%s\n\n", Dim, Cyan, Reset)
}

// PrintSummary displays the run summary
func PrintSummary(tool Tool, cfg *Config, workDir string, startTime time.Time, duration time.Duration, exitCode int) {
	fmt.Println()
	fmt.Printf("%s%s══════════════════════════════════════════%s\n", Bold, Cyan, Reset)
	fmt.Printf("%s%s  Run Summary%s\n", Bold, Cyan, Reset)
	fmt.Printf("%s%s══════════════════════════════════════════%s\n", Bold, Cyan, Reset)
	fmt.Printf("  %sCommand:%s      %s %s\n", Dim, Reset, tool.Name(), cfg.OriginalCmd)

	if cfg.TaskShortcut != "" {
		fmt.Printf("  %sShortcut:%s     %s%s%s\n", Dim, Reset, Magenta, cfg.TaskShortcut, Reset)
	}

	fmt.Printf("  %sStarted:%s      %s\n", Dim, Reset, startTime.Format("2006-01-02 15:04:05"))
	fmt.Printf("  %sDuration:%s     %s%s%s\n", Dim, Reset, Yellow, FormatDuration(duration), Reset)
	fmt.Printf("  %sModel:%s        %s\n", Dim, Reset, cfg.Model)

	// Tool-specific summary fields
	tool.PrintToolSpecificSummaryFields(cfg)

	displayDir := workDir
	if displayDir == "" {
		displayDir, _ = os.Getwd()
	}
	fmt.Printf("  %sDirectory:%s    %s\n", Dim, Reset, displayDir)

	if exitCode == 0 {
		fmt.Printf("  %sExit code:%s    %s%d%s\n", Dim, Reset, Green, exitCode, Reset)
	} else {
		fmt.Printf("  %sExit code:%s    %s%d%s\n", Dim, Reset, Yellow, exitCode, Reset)
	}

	fmt.Printf("%s%s══════════════════════════════════════════%s\n", Bold, Cyan, Reset)
}

// PrintMultiCodebaseSummary displays summary for multiple codebases
func PrintMultiCodebaseSummary(numCodebases int, duration time.Duration, exitCode int) {
	fmt.Println()
	fmt.Printf("%s%s════════════════════════════════════════════════════════════════%s\n", Bold, Cyan, Reset)
	fmt.Printf("%s%s  Overall Summary (%d codebases)%s\n", Bold, Cyan, numCodebases, Reset)
	fmt.Printf("%s%s════════════════════════════════════════════════════════════════%s\n", Bold, Cyan, Reset)
	fmt.Printf("  %sTotal duration:%s  %s%s%s\n", Dim, Reset, Yellow, FormatDuration(duration), Reset)
	if exitCode == 0 {
		fmt.Printf("  %sResult:%s          %sAll codebases completed successfully%s\n", Dim, Reset, Green, Reset)
	} else {
		fmt.Printf("  %sResult:%s          %sSome codebases had errors (exit code: %d)%s\n", Dim, Reset, Yellow, exitCode, Reset)
	}
	fmt.Printf("%s%s════════════════════════════════════════════════════════════════%s\n", Bold, Cyan, Reset)
}

// PrintCodebaseHeader displays header for a codebase when running multiple
func PrintCodebaseHeader(index, total int, workDir string) {
	fmt.Printf("\n%s%s════════════════════════════════════════════════════════════════%s\n", Bold, Cyan, Reset)
	fmt.Printf("%s%s  Codebase %d/%d: %s%s\n", Bold, Cyan, index, total, filepath.Base(workDir), Reset)
	fmt.Printf("%s%s════════════════════════════════════════════════════════════════%s\n\n", Bold, Cyan, Reset)
}

// RunStats holds statistics for JSON output
type RunStats struct {
	Tool          string                 `json:"tool"`
	Task          string                 `json:"task"`
	TaskShortcut  string                 `json:"task_shortcut,omitempty"`
	Model         string                 `json:"model"`
	Codebases     []string               `json:"codebases"`
	StartTime     string                 `json:"start_time"`
	EndTime       string                 `json:"end_time"`
	DurationSecs  float64                `json:"duration_secs"`
	ExitCode      int                    `json:"exit_code"`
	Success       bool                   `json:"success"`
	Options       map[string]bool        `json:"options"`
	Variables     map[string]string      `json:"variables,omitempty"`
	ToolSpecific  map[string]interface{} `json:"tool_specific,omitempty"`
}

// OutputStatsJSON outputs run statistics as JSON
func OutputStatsJSON(tool Tool, cfg *Config, startTime, endTime time.Time, exitCode int) {
	stats := RunStats{
		Tool:         tool.Name(),
		Task:         cfg.Task,
		TaskShortcut: cfg.TaskShortcut,
		Model:        cfg.Model,
		Codebases:    cfg.WorkDirs,
		StartTime:    startTime.Format(time.RFC3339),
		EndTime:      endTime.Format(time.RFC3339),
		DurationSecs: endTime.Sub(startTime).Seconds(),
		ExitCode:     exitCode,
		Success:      exitCode == 0,
		Options: map[string]bool{
			"lock":           cfg.UseLock,
			"delete_old":     cfg.DeleteOld,
			"require_review": cfg.RequireReview,
			"output_json":    cfg.OutputJSON,
		},
	}
	if len(cfg.Vars) > 0 {
		stats.Variables = cfg.Vars
	}
	if len(stats.Codebases) == 0 {
		cwd, _ := os.Getwd()
		stats.Codebases = []string{cwd}
	}

	// Add tool-specific fields
	toolFields := tool.StatsJSONFields(cfg)
	if len(toolFields) > 0 {
		stats.ToolSpecific = toolFields
	}

	jsonData, err := json.MarshalIndent(stats, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "%sError: Failed to encode JSON stats: %v%s\n", Yellow, err, Reset)
		return
	}
	fmt.Println(string(jsonData))
}

// FormatDuration formats a duration as "Xm Ys"
func FormatDuration(d time.Duration) string {
	mins := int(d.Minutes())
	secs := int(d.Seconds()) % 60
	return fmt.Sprintf("%dm %ds", mins, secs)
}

// PrintReportProgress prints progress for report completion
func PrintReportProgress(reportType string, duration time.Duration, exitCode int) {
	fmt.Println()
	if exitCode == 0 {
		fmt.Printf("%s✓ %s completed%s %s(%s)%s\n\n", Green, reportType, Reset, Dim, FormatDuration(duration), Reset)
	} else {
		fmt.Printf("%s✗ %s failed (exit code: %d)%s %s(%s)%s\n\n", Yellow, reportType, exitCode, Reset, Dim, FormatDuration(duration), Reset)
	}
}

// PrintReportHeader prints header when starting a report
func PrintReportHeader(reportType string) {
	fmt.Printf("%s%s━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━%s\n", Bold, Magenta, Reset)
	fmt.Printf("%s%s  Starting: %s%s\n", Bold, Magenta, reportType, Reset)
	fmt.Printf("%s%s━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━%s\n\n", Bold, Magenta, Reset)
}

// PrintPhaseHeader prints header for a phase (e.g., "Phase 1/2: all_small")
func PrintPhaseHeader(phase, description string) {
	fmt.Printf("%s%s━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━%s\n", Bold, Magenta, Reset)
	fmt.Printf("%s%s  %s: %s%s\n", Bold, Magenta, phase, description, Reset)
	fmt.Printf("%s%s━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━%s\n\n", Bold, Magenta, Reset)
}
