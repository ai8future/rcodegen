package orchestrator

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"rcodegen/pkg/bundle"
)

// ANSI cursor control codes
const (
	cursorHide    = "\033[?25l"
	cursorShow    = "\033[?25h"
	cursorHome    = "\033[H"
	clearScreen   = "\033[2J"
	clearLine     = "\033[K"
	cursorUp      = "\033[%dA"
	cursorDown    = "\033[%dB"
	saveCursor    = "\033[s"
	restoreCursor = "\033[u"
)

// Spinner frames for animation
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// LiveDisplay handles animated terminal output
type LiveDisplay struct {
	mu sync.Mutex

	bundleName  string
	jobID       string
	projectName string
	task        string
	outputDir   string
	logDir      string // Directory where step logs are written
	steps       []LiveStep
	startTime   time.Time
	width       int

	// Live state
	currentStep    int
	spinnerFrame   int
	liveOutput     string // Single line of current activity
	maxOutputLines int
	totalCost      float64
	totalTokens    int

	// Control
	done     chan struct{}
	stopOnce sync.Once
}

// LiveStep tracks progress for a single step
type LiveStep struct {
	Name      string
	Tool      string
	State     StepState
	Cost      float64
	Duration  time.Duration
	Tokens    int
	StartTime time.Time
}

// NewLiveDisplay creates a new animated display
func NewLiveDisplay(b *bundle.Bundle, jobID string, inputs map[string]string) *LiveDisplay {
	steps := make([]LiveStep, len(b.Steps))
	for i, step := range b.Steps {
		tool := step.Tool
		if tool == "" && len(step.Parallel) > 0 {
			tool = "parallel"
		}
		steps[i] = LiveStep{
			Name:  step.Name,
			Tool:  tool,
			State: StepPending,
		}
	}

	task := inputs["task"]
	if task == "" {
		task = inputs["topic"]
	}
	if len(task) > 55 {
		task = task[:52] + "..."
	}

	return &LiveDisplay{
		bundleName:     b.Name,
		jobID:          jobID,
		projectName:    inputs["project_name"],
		task:           task,
		outputDir:      inputs["output_dir"],
		steps:          steps,
		startTime:      time.Now(),
		width:          72,
		currentStep:    -1,
		maxOutputLines: 1,
		liveOutput:     "",
		done:           make(chan struct{}),
	}
}

// SetLogDir sets the directory where step logs are written
func (d *LiveDisplay) SetLogDir(dir string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.logDir = dir
}

// Start begins the animated display
func (d *LiveDisplay) Start() {
	fmt.Print(cursorHide)
	fmt.Print(clearScreen)
	fmt.Print(cursorHome)

	// Start the animation loop
	go d.animationLoop()
}

// Stop ends the animated display
func (d *LiveDisplay) Stop() {
	d.stopOnce.Do(func() {
		close(d.done)
		fmt.Print(cursorShow)
	})
}

// animationLoop updates the display periodically
func (d *LiveDisplay) animationLoop() {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-d.done:
			return
		case <-ticker.C:
			d.mu.Lock()
			d.spinnerFrame = (d.spinnerFrame + 1) % len(spinnerFrames)
			// Read latest line from current step's log
			if d.currentStep >= 0 && d.currentStep < len(d.steps) && d.logDir != "" {
				stepName := d.steps[d.currentStep].Name
				d.liveOutput = d.readLastMeaningfulLine(stepName)
			}
			d.render()
			d.mu.Unlock()
		}
	}
}

// readLastMeaningfulLine reads the last non-empty, meaningful line from a step's log
func (d *LiveDisplay) readLastMeaningfulLine(stepName string) string {
	logPath := filepath.Join(d.logDir, stepName+".log")
	f, err := os.Open(logPath)
	if err != nil {
		return ""
	}
	defer f.Close()

	var lastLine string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		// Skip empty lines and JSON-only lines
		if line == "" || line == "{" || line == "}" {
			continue
		}
		// Extract meaningful content from stream-json format
		if meaningful := extractMeaningfulContent(line); meaningful != "" {
			lastLine = meaningful
		}
	}
	return lastLine
}

// extractMeaningfulContent pulls human-readable content from tool output
func extractMeaningfulContent(line string) string {
	// Remove ANSI codes first
	line = stripAnsi(line)

	// Check for tool use FIRST - this is the most useful status indicator
	// Claude uses "name":"ToolName" inside tool_use, Gemini may use "tool_name"
	if strings.Contains(line, `"tool_use"`) || strings.Contains(line, `"type":"tool_use"`) {
		if strings.Contains(line, `"name":"Read"`) {
			return "Reading files..."
		}
		if strings.Contains(line, `"name":"Write"`) {
			return "Writing code..."
		}
		if strings.Contains(line, `"name":"Bash"`) {
			return "Running command..."
		}
		if strings.Contains(line, `"name":"Edit"`) {
			return "Editing files..."
		}
		if strings.Contains(line, `"name":"Glob"`) {
			return "Searching files..."
		}
		if strings.Contains(line, `"name":"Grep"`) {
			return "Searching content..."
		}
		if strings.Contains(line, `"name":"TodoWrite"`) {
			return "Updating tasks..."
		}
		if strings.Contains(line, `"name":"Task"`) {
			return "Spawning agent..."
		}
		if strings.Contains(line, `"name":"WebFetch"`) {
			return "Fetching URL..."
		}
		if strings.Contains(line, `"name":"WebSearch"`) {
			return "Searching web..."
		}
		return "Using tools..."
	}

	// Check for tool_result (tool finished)
	if strings.Contains(line, `"tool_result"`) || strings.Contains(line, `"type":"tool_result"`) {
		return "Processing result..."
	}

	// Look for assistant text streaming
	if strings.Contains(line, `"type":"assistant"`) && strings.Contains(line, `"type":"text"`) {
		return "Thinking..."
	}

	// Skip system/init messages
	if strings.Contains(line, `"type":"system"`) || strings.Contains(line, `"type":"init"`) {
		return ""
	}

	// Skip result messages (end of run)
	if strings.Contains(line, `"type":"result"`) {
		return ""
	}

	// If line is short enough and looks like status, use it
	if len(line) > 5 && len(line) < 80 && !strings.HasPrefix(line, `"`) && !strings.HasPrefix(line, "{") {
		return line
	}

	return ""
}

// render draws the entire display
func (d *LiveDisplay) render() {
	fmt.Print(cursorHome)

	w := d.width
	elapsed := time.Since(d.startTime)

	// Header box
	fmt.Printf("%s%s%s%s%s%s\n",
		colorCyan, boxTopLeft,
		strings.Repeat(boxHorizontal, w-2),
		boxTopRight, colorReset, clearLine)

	title := fmt.Sprintf("  rcodegen · %s", d.bundleName)
	padding := w - 2 - utf8.RuneCountInString(title)
	if padding < 0 {
		padding = 0
	}
	fmt.Printf("%s%s%s%s%s%s%s%s\n",
		colorCyan, boxVertical, colorReset,
		colorBold, title, colorReset,
		strings.Repeat(" ", padding),
		colorCyan+boxVertical+colorReset+clearLine)

	// Elapsed time and cost in header
	elapsedStr := formatDuration(elapsed)
	costStr := fmt.Sprintf("$%.2f", d.totalCost)
	// Visual format: "  {elapsed}  ·  {cost}" = 2 + elapsed + 5 + cost
	infoLineVisualLen := 2 + len(elapsedStr) + 5 + len(costStr)
	infoPadding := w - 2 - infoLineVisualLen
	if infoPadding < 0 {
		infoPadding = 0
	}
	fmt.Printf("%s%s%s  %s%s%s  %s·%s  %s%s%s%s%s%s\n",
		colorCyan, boxVertical, colorReset,
		colorYellow, elapsedStr, colorReset,
		colorDim, colorReset,
		colorGreen, costStr, colorReset,
		strings.Repeat(" ", infoPadding),
		colorCyan+boxVertical+colorReset, clearLine)

	fmt.Printf("%s%s%s%s%s%s\n",
		colorCyan, boxBottomLeft,
		strings.Repeat(boxHorizontal, w-2),
		boxBottomRight, colorReset, clearLine)

	// Task info
	if d.task != "" {
		fmt.Printf("\n  %sTask:%s %s\"%s\"%s%s\n",
			colorDim, colorReset, colorDim, d.task, colorReset, clearLine)
	} else {
		fmt.Printf("\n%s\n", clearLine)
	}
	fmt.Printf("%s\n", clearLine)

	// Steps list
	for i, step := range d.steps {
		d.renderStep(i, &step)
	}

	// Live output section (if we have a running step)
	fmt.Printf("\n%s\n", clearLine)
	if d.currentStep >= 0 && d.currentStep < len(d.steps) && d.steps[d.currentStep].State == StepRunning {
		// Show single line of current activity
		activity := d.liveOutput
		if activity == "" {
			activity = "Working..."
		}
		if len(activity) > w-8 {
			activity = activity[:w-11] + "..."
		}
		fmt.Printf("  %s→%s %s%s%s%s\n",
			colorCyan, colorReset,
			colorWhite, activity, colorReset, clearLine)
	} else {
		// Empty line to maintain layout
		fmt.Printf("%s\n", clearLine)
	}

}

// renderStep renders a single step line
func (d *LiveDisplay) renderStep(index int, step *LiveStep) {
	var icon string
	var iconColor string
	var statusInfo string

	switch step.State {
	case StepPending:
		icon = iconPending
		iconColor = colorDim
	case StepRunning:
		icon = spinnerFrames[d.spinnerFrame]
		iconColor = colorCyan
		elapsed := time.Since(step.StartTime)
		statusInfo = fmt.Sprintf(" %s%s%s", colorDim, formatDuration(elapsed), colorReset)
	case StepSuccess:
		icon = iconSuccess
		iconColor = colorGreen
		statusInfo = fmt.Sprintf(" %s$%.2f%s %s%s%s",
			colorGreen, step.Cost, colorReset,
			colorDim, formatDuration(step.Duration), colorReset)
	case StepFailure:
		icon = iconFailure
		iconColor = colorRed
	case StepSkipped:
		icon = iconSkipped
		iconColor = colorDim
		statusInfo = fmt.Sprintf(" %s(skipped)%s", colorDim, colorReset)
	}

	toolClr := toolColor(step.Tool)
	toolName := strings.Title(step.Tool)

	fmt.Printf("  %s%s%s  %-12s %s%-8s%s%s%s\n",
		iconColor, icon, colorReset,
		step.Name,
		toolClr, toolName, colorReset,
		statusInfo, clearLine)
}

// SetStepRunning marks a step as running
func (d *LiveDisplay) SetStepRunning(stepIndex int) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if stepIndex >= 0 && stepIndex < len(d.steps) {
		d.steps[stepIndex].State = StepRunning
		d.steps[stepIndex].StartTime = time.Now()
		d.currentStep = stepIndex
		d.liveOutput = "" // Clear live output for new step
	}
}

// SetStepComplete marks a step as complete
func (d *LiveDisplay) SetStepComplete(stepIndex int, cost float64, duration time.Duration, tokens int, success bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if stepIndex >= 0 && stepIndex < len(d.steps) {
		if success {
			d.steps[stepIndex].State = StepSuccess
		} else {
			d.steps[stepIndex].State = StepFailure
		}
		d.steps[stepIndex].Cost = cost
		d.steps[stepIndex].Duration = duration
		d.steps[stepIndex].Tokens = tokens
		d.totalCost += cost
		d.totalTokens += tokens
	}
}

// SetStepSkipped marks a step as skipped
func (d *LiveDisplay) SetStepSkipped(stepIndex int) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if stepIndex >= 0 && stepIndex < len(d.steps) {
		d.steps[stepIndex].State = StepSkipped
	}
}

// UpdateCost updates the total cost display
func (d *LiveDisplay) UpdateCost(cost float64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.totalCost = cost
}

// PrintFinalSummary prints the final summary after animation stops
func (d *LiveDisplay) PrintFinalSummary(totalCost float64, totalInputTokens, totalOutputTokens int, cacheRead, cacheWrite int) {
	duration := time.Since(d.startTime)

	// Count successes and failures
	successes := 0
	failures := 0
	for _, step := range d.steps {
		switch step.State {
		case StepSuccess:
			successes++
		case StepFailure:
			failures++
		}
	}

	fmt.Println()
	fmt.Printf("  %s%s%s\n", colorCyan, strings.Repeat("─", d.width-4), colorReset)
	fmt.Println()

	// Summary line
	durStr := formatDuration(duration)
	costStr := fmt.Sprintf("$%.2f", totalCost)

	status := fmt.Sprintf("%s%d/%d complete%s", colorGreen, successes, len(d.steps), colorReset)
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

// stripAnsi removes ANSI escape codes from a string
func stripAnsi(s string) string {
	var result strings.Builder
	inEscape := false

	for _, r := range s {
		if r == '\033' {
			inEscape = true
			continue
		}
		if inEscape {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				inEscape = false
			}
			continue
		}
		result.WriteRune(r)
	}

	return result.String()
}
