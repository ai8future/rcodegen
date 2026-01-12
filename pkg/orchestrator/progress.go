package orchestrator

import (
	"fmt"
	"strings"
	"time"

	"rcodegen/pkg/bundle"
)

// ANSI color codes
const (
	colorReset   = "\033[0m"
	colorBold    = "\033[1m"
	colorDim     = "\033[2m"
	colorCyan    = "\033[36m"
	colorGreen   = "\033[32m"
	colorRed     = "\033[31m"
	colorYellow  = "\033[33m"
	colorBlue    = "\033[34m"
	colorMagenta = "\033[35m"
	colorWhite   = "\033[37m"
)

// Box drawing characters (rounded)
const (
	boxTopLeft     = "╭"
	boxTopRight    = "╮"
	boxBottomLeft  = "╰"
	boxBottomRight = "╯"
	boxHorizontal  = "─"
	boxVertical    = "│"
)

// Status icons
const (
	iconPending  = "○"
	iconRunning  = "●"
	iconSuccess  = "✓"
	iconFailure  = "✗"
	iconSkipped  = "◌"
)

// StepState represents the execution state of a step
type StepState int

const (
	StepPending StepState = iota
	StepRunning
	StepSuccess
	StepFailure
	StepSkipped
)

// StepProgress tracks progress for a single step
type StepProgress struct {
	Name     string
	Tool     string
	State    StepState
	Cost     float64
	Duration time.Duration
	Tokens   int
}

// ProgressDisplay handles the visual output
type ProgressDisplay struct {
	bundleName  string
	jobID       string
	projectName string
	task        string
	outputDir   string
	steps       []StepProgress
	startTime   time.Time
	width       int
}

// NewProgressDisplay creates a new progress display
func NewProgressDisplay(b *bundle.Bundle, jobID string, inputs map[string]string) *ProgressDisplay {
	steps := make([]StepProgress, len(b.Steps))
	for i, step := range b.Steps {
		tool := step.Tool
		if tool == "" && len(step.Parallel) > 0 {
			// For parallel steps, show "parallel" or combine tool names
			tool = "parallel"
		}
		steps[i] = StepProgress{
			Name:  step.Name,
			Tool:  tool,
			State: StepPending,
		}
	}

	task := inputs["task"]
	if task == "" {
		task = inputs["topic"] // Fallback to topic for article bundles
	}
	if len(task) > 60 {
		task = task[:57] + "..."
	}

	return &ProgressDisplay{
		bundleName:  b.Name,
		jobID:       jobID,
		projectName: inputs["project_name"],
		task:        task,
		outputDir:   inputs["output_dir"],
		steps:       steps,
		startTime:   time.Now(),
		width:       72,
	}
}

// toolColor returns the appropriate color for a tool
func toolColor(tool string) string {
	switch tool {
	case "claude":
		return colorMagenta
	case "gemini":
		return colorYellow
	case "codex":
		return colorBlue
	case "parallel":
		return colorCyan
	default:
		return colorWhite
	}
}

// stateIcon returns the icon for a step state
func stateIcon(state StepState) string {
	switch state {
	case StepPending:
		return iconPending
	case StepRunning:
		return iconRunning
	case StepSuccess:
		return iconSuccess
	case StepFailure:
		return iconFailure
	case StepSkipped:
		return iconSkipped
	default:
		return "?"
	}
}

// stateColor returns the color for a step state
func stateColor(state StepState) string {
	switch state {
	case StepPending:
		return colorDim
	case StepRunning:
		return colorCyan
	case StepSuccess:
		return colorGreen
	case StepFailure:
		return colorRed
	case StepSkipped:
		return colorDim
	default:
		return colorReset
	}
}

// Start implements Display interface - prints the header
func (p *ProgressDisplay) Start() {
	p.PrintHeader()
}

// Stop implements Display interface - no-op for static display
func (p *ProgressDisplay) Stop() {}

// SetStepRunning implements Display interface
func (p *ProgressDisplay) SetStepRunning(stepIndex int) {
	p.PrintStepStart(stepIndex)
}

// SetStepComplete implements Display interface
func (p *ProgressDisplay) SetStepComplete(stepIndex int, cost float64, duration time.Duration, tokens int, success bool) {
	p.PrintStepComplete(stepIndex, cost, duration, tokens, success)
	// Print remaining pending steps
	if stepIndex < len(p.steps)-1 {
		p.PrintPendingSteps(stepIndex + 1)
	}
}

// SetStepSkipped implements Display interface
func (p *ProgressDisplay) SetStepSkipped(stepIndex int) {
	p.PrintStepSkipped(stepIndex)
}

// PrintFinalSummary implements Display interface
func (p *ProgressDisplay) PrintFinalSummary(totalCost float64, totalInputTokens, totalOutputTokens int, cacheRead, cacheWrite int) {
	p.PrintSummary(totalCost, totalInputTokens, totalOutputTokens, cacheRead, cacheWrite)
}

// PrintHeader prints the initial header
func (p *ProgressDisplay) PrintHeader() {
	w := p.width

	// Top border
	fmt.Printf("%s%s%s%s%s\n",
		colorCyan, boxTopLeft,
		strings.Repeat(boxHorizontal, w-2),
		boxTopRight, colorReset)

	// Bundle name and job ID
	title := fmt.Sprintf("  rcodegen · %s", p.bundleName)
	padding := w - 2 - len(title)
	fmt.Printf("%s%s%s%s%s%s%s\n",
		colorCyan, boxVertical, colorReset,
		colorBold, title, strings.Repeat(" ", padding),
		colorCyan+boxVertical+colorReset)

	// Job ID line
	jobLine := fmt.Sprintf("  Job: %s", p.jobID)
	padding = w - 2 - len(jobLine)
	fmt.Printf("%s%s%s%s%s%s\n",
		colorCyan, boxVertical, colorReset,
		colorDim+jobLine+colorReset,
		strings.Repeat(" ", padding),
		colorCyan+boxVertical+colorReset)

	// Bottom border
	fmt.Printf("%s%s%s%s%s\n",
		colorCyan, boxBottomLeft,
		strings.Repeat(boxHorizontal, w-2),
		boxBottomRight, colorReset)

	// Project and task info
	if p.projectName != "" {
		fmt.Printf("\n  %sProject:%s %s\n", colorDim, colorReset, p.projectName)
	}
	if p.task != "" {
		fmt.Printf("  %sTask:%s %s\"%s\"%s\n", colorDim, colorReset, colorDim, p.task, colorReset)
	}
	if p.outputDir != "" {
		fmt.Printf("  %sOutput:%s %s\n", colorDim, colorReset, p.outputDir)
	}
	fmt.Println()
}

// PrintStepStart prints the start of a step
func (p *ProgressDisplay) PrintStepStart(stepIndex int) {
	if stepIndex < 0 || stepIndex >= len(p.steps) {
		return
	}

	step := &p.steps[stepIndex]
	step.State = StepRunning

	// Print step box
	w := p.width
	stepNum := fmt.Sprintf("Step %d/%d", stepIndex+1, len(p.steps))
	toolName := strings.Title(step.Tool)
	stepHeader := fmt.Sprintf("  %s · %s", stepNum, step.Name)

	// Calculate padding for right-aligned tool name
	headerLen := len(stepHeader)
	toolLen := len(toolName) + 3 // 3 for spacing
	padding := w - 4 - headerLen - toolLen
	if padding < 0 {
		padding = 0
	}

	// Top border
	fmt.Printf("  %s┌%s┐%s\n",
		colorCyan,
		strings.Repeat("─", w-4),
		colorReset)

	// Header line with step info and tool
	fmt.Printf("  %s│%s%s%s%s%s%s %s│%s\n",
		colorCyan, colorReset,
		colorBold+stepHeader+colorReset,
		strings.Repeat(" ", padding),
		toolColor(step.Tool), toolName, colorReset,
		colorCyan, colorReset)

	// Bottom border
	fmt.Printf("  %s└%s┘%s\n",
		colorCyan,
		strings.Repeat("─", w-4),
		colorReset)
}

// PrintStepComplete prints the completion of a step
func (p *ProgressDisplay) PrintStepComplete(stepIndex int, cost float64, duration time.Duration, tokens int, success bool) {
	if stepIndex < 0 || stepIndex >= len(p.steps) {
		return
	}

	step := &p.steps[stepIndex]
	if success {
		step.State = StepSuccess
	} else {
		step.State = StepFailure
	}
	step.Cost = cost
	step.Duration = duration
	step.Tokens = tokens

	// Print step result line
	icon := stateIcon(step.State)
	iconClr := stateColor(step.State)
	toolClr := toolColor(step.Tool)

	// Format duration
	durStr := formatDuration(duration)

	// Format cost
	costStr := fmt.Sprintf("$%.2f", cost)

	fmt.Printf("\n  %s%s%s  %-12s %s%-8s%s  %s%8s%s  %s%s%s\n",
		iconClr, icon, colorReset,
		step.Name,
		toolClr, strings.Title(step.Tool), colorReset,
		colorGreen, costStr, colorReset,
		colorDim, durStr, colorReset)
}

// PrintStepSkipped prints a skipped step
func (p *ProgressDisplay) PrintStepSkipped(stepIndex int) {
	if stepIndex < 0 || stepIndex >= len(p.steps) {
		return
	}

	step := &p.steps[stepIndex]
	step.State = StepSkipped

	icon := stateIcon(step.State)
	iconClr := stateColor(step.State)

	fmt.Printf("  %s%s%s  %-12s %s(skipped)%s\n",
		iconClr, icon, colorReset,
		step.Name,
		colorDim, colorReset)
}

// PrintSummary prints the final summary
func (p *ProgressDisplay) PrintSummary(totalCost float64, totalInputTokens, totalOutputTokens int, cacheRead, cacheWrite int) {
	duration := time.Since(p.startTime)

	// Count successes and failures
	successes := 0
	failures := 0
	for _, step := range p.steps {
		switch step.State {
		case StepSuccess:
			successes++
		case StepFailure:
			failures++
		}
	}

	fmt.Println()
	fmt.Printf("  %s%s%s\n", colorCyan, strings.Repeat("─", p.width-4), colorReset)
	fmt.Println()

	// Summary line
	durStr := formatDuration(duration)
	costStr := fmt.Sprintf("$%.2f", totalCost)

	status := fmt.Sprintf("%s%d/%d complete%s", colorGreen, successes, len(p.steps), colorReset)
	if failures > 0 {
		status = fmt.Sprintf("%s%d failed%s", colorRed, failures, colorReset)
	}

	fmt.Printf("  %sElapsed:%s %s  %s·%s  %sCost:%s %s%s%s  %s·%s  %s\n",
		colorDim, colorReset, durStr,
		colorDim, colorReset,
		colorDim, colorReset, colorGreen, costStr, colorReset,
		colorDim, colorReset,
		status)

	// Token info
	fmt.Printf("  %sTokens:%s %s%d%s in, %s%d%s out",
		colorDim, colorReset,
		colorWhite, totalInputTokens, colorReset,
		colorWhite, totalOutputTokens, colorReset)
	if cacheRead > 0 || cacheWrite > 0 {
		fmt.Printf(" %s(cache: %d read, %d write)%s", colorDim, cacheRead, cacheWrite, colorReset)
	}
	fmt.Println()
	fmt.Println()
}

// PrintFailure prints a failure message
func (p *ProgressDisplay) PrintFailure(stepName string, err error) {
	fmt.Printf("\n  %s%s%s  Step '%s' failed: %v\n",
		colorRed, iconFailure, colorReset,
		stepName, err)
}

// formatDuration formats a duration nicely
func formatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	m := int(d.Minutes())
	s := int(d.Seconds()) % 60
	if s == 0 {
		return fmt.Sprintf("%dm", m)
	}
	return fmt.Sprintf("%dm %ds", m, s)
}

// PrintPendingSteps prints all remaining pending steps
func (p *ProgressDisplay) PrintPendingSteps(fromIndex int) {
	for i := fromIndex; i < len(p.steps); i++ {
		step := &p.steps[i]
		if step.State == StepPending {
			icon := stateIcon(step.State)
			iconClr := stateColor(step.State)
			toolClr := toolColor(step.Tool)

			fmt.Printf("  %s%s%s  %-12s %s%s%s\n",
				iconClr, icon, colorReset,
				step.Name,
				toolClr, strings.Title(step.Tool), colorReset)
		}
	}
}
