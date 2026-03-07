package batch

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"rcodegen/pkg/colors"
)

// WriteSummary writes a summary.json file into batchDir.
func WriteSummary(batchDir string, result *BatchResult) error {
	if err := os.MkdirAll(batchDir, 0o755); err != nil {
		return fmt.Errorf("creating batch dir: %w", err)
	}

	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling summary: %w", err)
	}

	path := filepath.Join(batchDir, "summary.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("writing summary: %w", err)
	}
	return nil
}

// WriteJobResult writes a per-job result file into batchDir/results/<name>.json.
func WriteJobResult(batchDir, jobName string, result *JobResult) error {
	dir := filepath.Join(batchDir, "results")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating results dir: %w", err)
	}

	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling job result: %w", err)
	}

	path := filepath.Join(dir, jobName+".json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("writing job result: %w", err)
	}
	return nil
}

// PrintLiveStatus writes a single-line progress update to stderr.
// The line begins with \r so repeated calls overwrite the same line.
func PrintLiveStatus(name string, completed, failed, total, active int, cost float64, elapsed time.Duration) {
	pct := 0
	if total > 0 {
		pct = (completed + failed) * 100 / total
	}
	bar := progressBar(pct, 20)
	fmt.Fprintf(os.Stderr,
		"\r%s%s%s %s %d/%d done  %s%d active%s  %s%d failed%s  $%.2f  %s",
		colors.Bold, name, colors.Reset,
		bar,
		completed+failed, total,
		colors.Cyan, active, colors.Reset,
		colors.Red, failed, colors.Reset,
		cost,
		formatElapsed(elapsed),
	)
}

// PrintBatchSummary prints a colored multi-line summary to stdout.
func PrintBatchSummary(result *BatchResult) {
	fmt.Println()
	fmt.Printf("%s%s Batch Summary%s\n", colors.Bold, colors.White, colors.Reset)
	fmt.Printf("  Name:      %s\n", result.Name)
	fmt.Printf("  Status:    %s\n", colorizeStatus(result.Status))
	fmt.Printf("  Jobs:      %d total, %s%d succeeded%s, %s%d failed%s\n",
		result.JobsTotal,
		colors.Green, result.JobsSucceeded, colors.Reset,
		colors.Red, result.JobsFailed, colors.Reset,
	)
	fmt.Printf("  Cost:      $%.2f\n", result.TotalCost)
	fmt.Printf("  Duration:  %s\n", result.TotalDuration)
	if result.StopReason != "" {
		fmt.Printf("  Stop:      %s%s%s\n", colors.Yellow, result.StopReason, colors.Reset)
	}
}

// progressBar returns a text progress bar like "[====........]".
func progressBar(pct, width int) string {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	filled := pct * width / 100
	empty := width - filled
	return "[" + strings.Repeat("=", filled) + strings.Repeat(".", empty) + "]"
}

// colorizeStatus wraps the status string in the appropriate ANSI colour.
func colorizeStatus(status string) string {
	switch status {
	case "completed":
		return colors.Green + status + colors.Reset
	case "completed_with_failures":
		return colors.Yellow + status + colors.Reset
	case "stopped", "cancelled":
		return colors.Red + status + colors.Reset
	default:
		return status
	}
}

// formatElapsed returns a compact elapsed-time string (e.g. "1m32s").
func formatElapsed(d time.Duration) string {
	d = d.Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	m := int(d.Minutes())
	s := int(d.Seconds()) % 60
	return fmt.Sprintf("%dm%02ds", m, s)
}
