// Package orchestrator executes multi-step task bundles, coordinating
// between different AI tools and managing step dependencies.
package orchestrator

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"rcodegen/pkg/bundle"
	"rcodegen/pkg/envelope"
	"rcodegen/pkg/runner"
	"rcodegen/pkg/settings"
	"rcodegen/pkg/tools/claude"
	"rcodegen/pkg/tools/codex"
	"rcodegen/pkg/tools/gemini"
	"rcodegen/pkg/workspace"
)

// StepStats holds statistics for a single step
type StepStats struct {
	Name         string
	Tool         string
	Model        string
	Parallel     bool
	Cost         float64
	InputTokens  int
	OutputTokens int
	Duration     time.Duration
	Output       string
}

// StepExecutor is the interface for executing steps.
// This allows the orchestrator to use a dispatcher without circular imports.
type StepExecutor interface {
	Execute(step *bundle.Step, ctx *Context, ws *workspace.Workspace) (*envelope.Envelope, error)
}

// Display is the interface for progress display implementations
type Display interface {
	Start()
	Stop()
	SetStepRunning(stepIndex int)
	SetStepModel(stepIndex int, model string)
	SetStepComplete(stepIndex int, cost float64, duration time.Duration, tokens int, success bool)
	SetStepSkipped(stepIndex int)
	PrintFinalSummary(totalCost float64, totalInputTokens, totalOutputTokens int, cacheRead, cacheWrite int)
}

// DispatcherFactory creates a dispatcher from a tool registry.
// This is set by the executor package to break the circular dependency.
var DispatcherFactory func(tools map[string]runner.Tool) StepExecutor

type Orchestrator struct {
	settings   *settings.Settings
	dispatcher StepExecutor
	tools      map[string]runner.Tool
	liveMode   bool
	opusOnly   bool
	flashOnly  bool
}

// SetLiveMode enables or disables the animated live display
func (o *Orchestrator) SetLiveMode(enabled bool) {
	o.liveMode = enabled
}

// SetOpusOnly forces all Claude steps to use Opus model
func (o *Orchestrator) SetOpusOnly(enabled bool) {
	o.opusOnly = enabled
}

// SetFlashOnly forces all Gemini steps to use flash preview model
func (o *Orchestrator) SetFlashOnly(enabled bool) {
	o.flashOnly = enabled
}

func New(s *settings.Settings) *Orchestrator {
	// Build tool registry
	tools := map[string]runner.Tool{
		"claude": claude.New(),
		"codex":  codex.New(),
		"gemini": gemini.New(),
	}

	var dispatcher StepExecutor
	if DispatcherFactory != nil {
		dispatcher = DispatcherFactory(tools)
	}

	return &Orchestrator{
		settings:   s,
		dispatcher: dispatcher,
		tools:      tools,
	}
}

// getStepModel returns the model that will be used for a step
func (o *Orchestrator) getStepModel(toolName, stepModel string) string {
	// If opus-only is set and tool is claude, always use opus
	if o.opusOnly && toolName == "claude" {
		return "opus"
	}
	// If flash-only is set and tool is gemini, always use flash preview
	if o.flashOnly && toolName == "gemini" {
		return "gemini-3-flash-preview"
	}
	// Use step's model if specified
	if stepModel != "" {
		return stepModel
	}
	// Use tool's default model
	if tool, ok := o.tools[toolName]; ok {
		return tool.DefaultModel()
	}
	return ""
}

func (o *Orchestrator) Run(b *bundle.Bundle, inputs map[string]string) (*envelope.Envelope, error) {
	start := time.Now()

	// Validate required inputs and apply defaults
	for _, input := range b.Inputs {
		if _, ok := inputs[input.Name]; !ok {
			if input.Default != "" {
				inputs[input.Name] = input.Default
			} else if input.Required {
				return envelope.New().
					Failure("MISSING_INPUT", "Required input: "+input.Name).
					Build(), nil
			}
		}
	}

	// Apply settings-based defaults for output_dir if not specified
	if _, hasOutputDir := inputs["output_dir"]; !hasOutputDir {
		if o.settings != nil && o.settings.DefaultBuildDir != "" {
			inputs["output_dir"] = o.settings.DefaultBuildDir
		}
	}

	// Create workspace
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.Getenv("HOME")
	}
	wsDir := filepath.Join(home, ".rcodegen", "workspace")
	ws, err := workspace.New(wsDir)
	if err != nil {
		return envelope.New().Failure("WORKSPACE_ERROR", err.Error()).Build(), err
	}

	// For article bundles, create a timestamped output directory
	var outputDir string
	if strings.HasPrefix(b.Name, "article") {
		cwd := inputs["codebase"]
		if cwd == "" {
			cwd, _ = os.Getwd()
		}
		timestamp := time.Now().Format("2006-01-02-15-04-05")
		outputDir = filepath.Join(cwd, "docs", fmt.Sprintf("article-%s", timestamp))
		if err := os.MkdirAll(outputDir, 0755); err != nil {
			return envelope.New().Failure("OUTPUT_DIR_ERROR", err.Error()).Build(), err
		}
		// Add output_dir to inputs so prompts can reference it
		inputs["output_dir"] = outputDir
	}

	// Initialize display (live animated or static)
	var display Display
	if o.liveMode {
		ld := NewLiveDisplay(b, ws.JobID, inputs)
		ld.SetLogDir(filepath.Join(ws.JobDir, "logs"))
		display = ld
	} else {
		display = NewProgressDisplay(b, ws.JobID, inputs)
	}

	// Set models for ALL steps upfront so they show immediately
	for i, step := range b.Steps {
		display.SetStepModel(i, o.getStepModel(step.Tool, step.Model))
	}

	display.Start()
	defer display.Stop()

	// Create context
	ctx := NewContext(inputs)

	// Track costs
	var totalCost float64
	var totalInputTokens, totalOutputTokens int
	var totalCacheRead, totalCacheWrite int
	var stepStats []StepStats

	// Execute steps
	for i, step := range b.Steps {
		stepStart := time.Now()
		display.SetStepRunning(i)
		// Set model immediately so it shows while running
		display.SetStepModel(i, o.getStepModel(step.Tool, step.Model))

		// Check condition
		if step.If != "" && !EvaluateCondition(step.If, ctx) {
			display.SetStepSkipped(i)
			ctx.SetResult(step.Name, &envelope.Envelope{Status: envelope.StatusSkipped})
			continue
		}

		// Handle conditional step
		if step.Then != nil {
			if EvaluateCondition(step.If, ctx) {
				env, err := o.dispatcher.Execute(step.Then, ctx, ws)
				ctx.SetResult(step.Name, env)
				if err != nil {
					return env, err
				}
			} else if step.Else != nil {
				env, err := o.dispatcher.Execute(step.Else, ctx, ws)
				ctx.SetResult(step.Name, env)
				if err != nil {
					return env, err
				}
			}
			continue
		}

		// Apply model overrides
		execStep := &step
		if o.opusOnly && step.Tool == "claude" {
			// Create a copy with opus model
			stepCopy := step
			stepCopy.Model = "opus"
			execStep = &stepCopy
		}
		if o.flashOnly && step.Tool == "gemini" {
			// Create a copy with flash preview model
			stepCopy := step
			stepCopy.Model = "gemini-3-flash-preview"
			execStep = &stepCopy
		}

		// Execute step
		env, err := o.dispatcher.Execute(execStep, ctx, ws)
		if err != nil {
			return env, err
		}

		ctx.SetResult(step.Name, env)

		// Extract and display cost info
		stepCost := 0.0
		stepIn, stepOut := 0, 0
		if c, ok := env.Result["cost_usd"].(float64); ok {
			stepCost = c
			totalCost += c
		}
		if t, ok := env.Result["input_tokens"].(int); ok {
			stepIn = t
			totalInputTokens += t
		}
		if t, ok := env.Result["output_tokens"].(int); ok {
			stepOut = t
			totalOutputTokens += t
		}
		if t, ok := env.Result["cache_read_tokens"].(int); ok {
			totalCacheRead += t
		}
		if t, ok := env.Result["cache_write_tokens"].(int); ok {
			totalCacheWrite += t
		}

		// Extract model used
		stepModel := ""
		if m, ok := env.Result["model"].(string); ok {
			stepModel = m
		}

		// Track step stats for report
		stepDuration := time.Since(stepStart)
		isParallel := len(step.Parallel) > 0
		stepStats = append(stepStats, StepStats{
			Name:         step.Name,
			Tool:         step.Tool,
			Model:        stepModel,
			Parallel:     isParallel,
			Cost:         stepCost,
			InputTokens:  stepIn,
			OutputTokens: stepOut,
			Duration:     stepDuration,
		})

		// Update display
		display.SetStepModel(i, stepModel)
		success := env.Status != envelope.StatusFailure
		display.SetStepComplete(i, stepCost, stepDuration, stepIn+stepOut, success)

		if env.Status == envelope.StatusFailure {
			return env, fmt.Errorf("step %s failed", step.Name)
		}
	}

	duration := time.Since(start)

	// Print summary
	display.PrintFinalSummary(totalCost, totalInputTokens, totalOutputTokens, totalCacheRead, totalCacheWrite)
	fmt.Printf("  %sOutput:%s %s\n\n", colorDim, colorReset, ws.JobDir)

	// Generate run report for article bundles
	if strings.HasPrefix(b.Name, "article") && outputDir != "" {
		reportPath := filepath.Join(outputDir, "Run Report.md")
		generateRunReport(reportPath, ws.JobID, b.Name, duration, totalCost, stepStats, ctx, outputDir)

		// Print final summary box
		fmt.Printf("\n  %s╭─────────────────────────────────────────────────────────────────╮%s\n", colorMagenta, colorReset)
		fmt.Printf("  %s│%s  %s✎ ARTICLES COMPLETE%s                                            %s│%s\n",
			colorMagenta, colorReset, colorBold+colorMagenta, colorReset, colorMagenta, colorReset)
		fmt.Printf("  %s╰─────────────────────────────────────────────────────────────────╯%s\n\n", colorMagenta, colorReset)

		// Print generated articles
		articles := findArticleFilesInDir(outputDir)
		if len(articles) > 0 {
			fmt.Printf("  %sGenerated Articles:%s\n", colorCyan+colorBold, colorReset)
			for _, a := range articles {
				fmt.Printf("    %s✓%s %s%s%s\n", colorGreen, colorReset, colorWhite, filepath.Base(a), colorReset)
			}
			fmt.Println()
		}

		// Print cost and time with colors
		fmt.Printf("  %sCost:%s        %s$%.2f%s\n",
			colorCyan, colorReset,
			colorGreen+colorBold, totalCost, colorReset)
		fmt.Printf("  %sTime:%s        %s%s%s\n",
			colorCyan, colorReset,
			colorYellow, duration.Round(time.Second), colorReset)
		fmt.Printf("  %sOutput:%s      %s%s%s\n\n",
			colorCyan, colorReset,
			colorBlue, outputDir, colorReset)
	}

	// Generate final-report.json and copy bundle for build bundles
	if projectName, hasProject := inputs["project_name"]; hasProject {
		outputDir := inputs["output_dir"]
		if outputDir != "" {
			projectDir := filepath.Join(outputDir, projectName)

			// Copy bundle to output directory
			if b.SourcePath != "" {
				bundleDest := filepath.Join(projectDir, "bundle-used.json")
				if bundleData, err := os.ReadFile(b.SourcePath); err == nil {
					if err := os.WriteFile(bundleDest, bundleData, 0644); err != nil {
						fmt.Fprintf(os.Stderr, "Warning: failed to copy bundle to %s: %v\n", bundleDest, err)
					}
				}
			}

			// Generate final-report.json
			generateFinalReportJSON(
				projectDir,
				ws.JobID,
				b,
				start,
				duration,
				totalCost,
				totalInputTokens,
				totalOutputTokens,
				totalCacheRead,
				totalCacheWrite,
				stepStats,
				inputs,
				ctx,
			)

			// Print final summary box
			fmt.Printf("\n  %s╭─────────────────────────────────────────────────────────────────╮%s\n", colorGreen, colorReset)
			fmt.Printf("  %s│%s  %s✓ BUILD COMPLETE%s                                               %s│%s\n",
				colorGreen, colorReset, colorBold+colorGreen, colorReset, colorGreen, colorReset)
			fmt.Printf("  %s╰─────────────────────────────────────────────────────────────────╯%s\n\n", colorGreen, colorReset)

			// Extract and print overview from IMPLEMENTATION_SUMMARY.md
			overview := extractOverviewFromSummary(filepath.Join(projectDir, "IMPLEMENTATION_SUMMARY.md"))
			if overview != "" {
				fmt.Printf("  %sOverview:%s\n", colorCyan+colorBold, colorReset)
				fmt.Printf("  %s%s%s\n\n", colorWhite, overview, colorReset)
			}

			// Print grade if available
			grade := extractGradeFromReport(filepath.Join(projectDir, "final-report.md"))
			if grade != nil {
				gradeColor := colorGreen
				if grade.Score < 70 {
					gradeColor = colorRed
				} else if grade.Score < 85 {
					gradeColor = colorYellow
				}
				fmt.Printf("  %sGrade:%s       %s%s%s %s(%d/100)%s\n",
					colorCyan, colorReset,
					gradeColor+colorBold, grade.Letter, colorReset,
					colorDim, grade.Score, colorReset)
			}

			// Print cost and time with colors
			fmt.Printf("  %sCost:%s        %s$%.2f%s\n",
				colorCyan, colorReset,
				colorGreen+colorBold, totalCost, colorReset)
			fmt.Printf("  %sTime:%s        %s%s%s\n",
				colorCyan, colorReset,
				colorYellow, duration.Round(time.Second), colorReset)
			fmt.Printf("  %sOutput:%s      %s%s%s\n\n",
				colorCyan, colorReset,
				colorBlue, projectDir, colorReset)
		}
	}

	return envelope.New().
		Success().
		WithResult("steps", len(b.Steps)).
		WithResult("job_id", ws.JobID).
		WithResult("total_cost_usd", totalCost).
		WithResult("input_tokens", totalInputTokens).
		WithResult("output_tokens", totalOutputTokens).
		WithResult("cache_read_tokens", totalCacheRead).
		WithResult("cache_write_tokens", totalCacheWrite).
		WithDuration(duration.Milliseconds()).
		Build(), nil
}

// generateRunReport creates a markdown report for article runs
func generateRunReport(path, jobID, bundleName string, duration time.Duration, totalCost float64, stats []StepStats, ctx *Context, outputDir string) {
	var sb strings.Builder

	sb.WriteString("# Run Report\n\n")
	sb.WriteString(fmt.Sprintf("**Job ID:** %s  \n", jobID))
	sb.WriteString(fmt.Sprintf("**Bundle:** %s  \n", bundleName))
	sb.WriteString(fmt.Sprintf("**Duration:** %s  \n", duration.Round(time.Second)))
	sb.WriteString(fmt.Sprintf("**Total Cost:** $%.4f\n\n", totalCost))

	// Build expanded step list (expand parallel steps into substeps)
	type ExpandedStep struct {
		StepNum  int
		Name     string
		Tool     string
		Parallel string
		Cost     float64
		Output   string
	}
	var expanded []ExpandedStep

	articles := findArticleFilesInDir(outputDir)
	articleIdx := 0

	for i, s := range stats {
		stepNum := i + 1
		if s.Parallel {
			// This is a parallel container - expand into substeps
			// Look up actual substep costs from context
			if s.Name == "drafts" {
				codexCost := getSubstepCost(ctx, "draft-codex")
				geminiCost := getSubstepCost(ctx, "draft-gemini")
				expanded = append(expanded, ExpandedStep{stepNum, "Draft", "Codex", "✓", codexCost, "docs/draft-codex.md"})
				expanded = append(expanded, ExpandedStep{stepNum, "Draft", "Gemini", "✓", geminiCost, "docs/draft-gemini.md"})
			} else if s.Name == "edits" {
				// Get article filenames and costs - search case-insensitively
				codexPath := findArticleByTool(articles, "codex")
				geminiPath := findArticleByTool(articles, "gemini")
				codexOut := "docs/(title) - Codex.md"
				geminiOut := "docs/(title) - Gemini.md"
				if codexPath != "" {
					codexOut = "docs/" + filepath.Base(codexPath)
				}
				if geminiPath != "" {
					geminiOut = "docs/" + filepath.Base(geminiPath)
				}
				codexEditCost := getSubstepCost(ctx, "edit-codex")
				geminiEditCost := getSubstepCost(ctx, "edit-gemini")
				expanded = append(expanded, ExpandedStep{stepNum, "Edit", "Codex", "✓", codexEditCost, codexOut})
				expanded = append(expanded, ExpandedStep{stepNum, "Edit", "Gemini", "✓", geminiEditCost, geminiOut})
			} else {
				// Generic parallel
				expanded = append(expanded, ExpandedStep{stepNum, s.Name, "parallel", "✓", s.Cost, "-"})
			}
		} else {
			output := findStepOutput(s.Name)
			if s.Name == "edit" && len(articles) > 0 {
				output = "docs/" + filepath.Base(articles[articleIdx])
				articleIdx++
			}
			expanded = append(expanded, ExpandedStep{stepNum, capitalize(s.Name), capitalize(s.Tool), "-", s.Cost, output})
		}
	}

	// Calculate column widths
	stageW, toolW, paraW, costW, outW := 12, 8, 10, 8, 10
	for _, e := range expanded {
		stage := fmt.Sprintf("%d. %s", e.StepNum, e.Name)
		if len(stage) > stageW {
			stageW = len(stage)
		}
		if len(e.Tool) > toolW {
			toolW = len(e.Tool)
		}
		if len(e.Output) > outW {
			outW = len(e.Output)
		}
	}
	costW = 10 // Fixed for $0.0000 format

	// Summary table with ASCII boxes
	sb.WriteString("## Summary\n\n")
	sb.WriteString("```\n")

	// Header
	sb.WriteString(fmt.Sprintf("┌%s┬%s┬%s┬%s┬%s┐\n",
		strings.Repeat("─", stageW+2), strings.Repeat("─", toolW+2),
		strings.Repeat("─", paraW+2), strings.Repeat("─", costW+2),
		strings.Repeat("─", outW+2)))
	sb.WriteString(fmt.Sprintf("│ %-*s │ %-*s │ %-*s │ %-*s │ %-*s │\n",
		stageW, "Stage", toolW, "Tool", paraW, "Parallel", costW, "Cost", outW, "Output"))
	sb.WriteString(fmt.Sprintf("├%s┼%s┼%s┼%s┼%s┤\n",
		strings.Repeat("─", stageW+2), strings.Repeat("─", toolW+2),
		strings.Repeat("─", paraW+2), strings.Repeat("─", costW+2),
		strings.Repeat("─", outW+2)))

	// Rows
	for i, e := range expanded {
		stage := fmt.Sprintf("%d. %s", e.StepNum, e.Name)
		cost := fmt.Sprintf("$%.4f", e.Cost)
		sb.WriteString(fmt.Sprintf("│ %-*s │ %-*s │ %-*s │ %-*s │ %-*s │\n",
			stageW, stage, toolW, e.Tool, paraW, e.Parallel, costW, cost, outW, e.Output))
		if i < len(expanded)-1 {
			sb.WriteString(fmt.Sprintf("├%s┼%s┼%s┼%s┼%s┤\n",
				strings.Repeat("─", stageW+2), strings.Repeat("─", toolW+2),
				strings.Repeat("─", paraW+2), strings.Repeat("─", costW+2),
				strings.Repeat("─", outW+2)))
		}
	}

	// Footer
	sb.WriteString(fmt.Sprintf("└%s┴%s┴%s┴%s┴%s┘\n",
		strings.Repeat("─", stageW+2), strings.Repeat("─", toolW+2),
		strings.Repeat("─", paraW+2), strings.Repeat("─", costW+2),
		strings.Repeat("─", outW+2)))
	sb.WriteString("```\n\n")

	sb.WriteString(fmt.Sprintf("**Total:** %s | %d articles produced\n\n", duration.Round(time.Second), len(articles)))
	sb.WriteString("---\n\n")

	// Comparison table
	if len(articles) >= 2 {
		sb.WriteString("## Comparison\n\n")

		// Get article data - search case-insensitively
		codexPath := findArticleByTool(articles, "codex")
		geminiPath := findArticleByTool(articles, "gemini")

		codexTitle := extractTitle(codexPath)
		geminiTitle := extractTitle(geminiPath)
		codexWords := countWords(codexPath)
		geminiWords := countWords(geminiPath)
		codexOpening := extractOpeningSummary(codexPath)
		geminiOpening := extractOpeningSummary(geminiPath)
		codexAngle := extractAngle(codexPath)
		geminiAngle := extractAngle(geminiPath)
		codexData := extractDataPoint(codexPath)
		geminiData := extractDataPoint(geminiPath)
		codexTone := extractTone(codexPath)
		geminiTone := extractTone(geminiPath)

		// Calculate widths
		labelW := 9
		col1W := max(len(codexTitle), len(codexOpening), len(codexAngle), len(codexData), len(codexTone), 20)
		col2W := max(len(geminiTitle), len(geminiOpening), len(geminiAngle), len(geminiData), len(geminiTone), 20)
		if col1W > 48 {
			col1W = 48
		}
		if col2W > 48 {
			col2W = 48
		}

		sb.WriteString("```\n")

		// Header
		sb.WriteString(fmt.Sprintf("┌%s┬%s┬%s┐\n",
			strings.Repeat("─", labelW+2), strings.Repeat("─", col1W+2), strings.Repeat("─", col2W+2)))
		sb.WriteString(fmt.Sprintf("│ %-*s │ %-*s │ %-*s │\n",
			labelW, "", col1W, "Codex", col2W, "Gemini"))
		sb.WriteString(fmt.Sprintf("├%s┼%s┼%s┤\n",
			strings.Repeat("─", labelW+2), strings.Repeat("─", col1W+2), strings.Repeat("─", col2W+2)))

		// Rows
		rows := []struct{ label, c, g string }{
			{"Title", truncate(codexTitle, col1W), truncate(geminiTitle, col2W)},
			{"Words", fmt.Sprintf("%d", codexWords), fmt.Sprintf("%d", geminiWords)},
			{"Opening", truncate(codexOpening, col1W), truncate(geminiOpening, col2W)},
			{"Angle", truncate(codexAngle, col1W), truncate(geminiAngle, col2W)},
			{"Data", truncate(codexData, col1W), truncate(geminiData, col2W)},
			{"Tone", truncate(codexTone, col1W), truncate(geminiTone, col2W)},
		}

		for i, r := range rows {
			sb.WriteString(fmt.Sprintf("│ %-*s │ %-*s │ %-*s │\n",
				labelW, r.label, col1W, r.c, col2W, r.g))
			if i < len(rows)-1 {
				sb.WriteString(fmt.Sprintf("├%s┼%s┼%s┤\n",
					strings.Repeat("─", labelW+2), strings.Repeat("─", col1W+2), strings.Repeat("─", col2W+2)))
			}
		}

		sb.WriteString(fmt.Sprintf("└%s┴%s┴%s┘\n",
			strings.Repeat("─", labelW+2), strings.Repeat("─", col1W+2), strings.Repeat("─", col2W+2)))
		sb.WriteString("```\n")

	} else if len(articles) == 1 {
		sb.WriteString("## Article\n\n")
		title := extractTitle(articles[0])
		words := countWords(articles[0])
		sb.WriteString(fmt.Sprintf("**Title:** %s  \n", title))
		sb.WriteString(fmt.Sprintf("**Words:** %d  \n", words))
		sb.WriteString(fmt.Sprintf("**File:** `%s`\n", filepath.Base(articles[0])))
	}

	if err := os.WriteFile(path, []byte(sb.String()), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to write run report to %s: %v\n", path, err)
	}
}

func getSubstepCost(ctx *Context, stepName string) float64 {
	ctx.mu.RLock()
	defer ctx.mu.RUnlock()
	if env, ok := ctx.StepResults[stepName]; ok && env != nil {
		if cost, ok := env.Result["cost_usd"].(float64); ok {
			return cost
		}
	}
	return 0.0
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

func max(nums ...int) int {
	m := nums[0]
	for _, n := range nums {
		if n > m {
			m = n
		}
	}
	return m
}

func extractOpeningSummary(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return "Unknown"
	}
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Extract character name and situation
		// Look for patterns like "Name, job, time"
		if idx := strings.Index(line, ","); idx > 0 && idx < 30 {
			// Get first part (likely a name)
			name := line[:idx]
			// Find next comma or period
			rest := line[idx+1:]
			if idx2 := strings.Index(rest, ","); idx2 > 0 && idx2 < 40 {
				return strings.TrimSpace(name) + ", " + strings.TrimSpace(rest[:idx2])
			}
			if idx2 := strings.Index(rest, "."); idx2 > 0 && idx2 < 50 {
				return strings.TrimSpace(name) + ", " + strings.TrimSpace(rest[:idx2])
			}
		}
		// Fallback: first 40 chars
		if len(line) > 40 {
			return line[:37] + "..."
		}
		return line
	}
	return "Unknown"
}

func extractAngle(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return "Unknown"
	}
	content := strings.ToLower(string(data))

	// Look for key themes
	angles := []string{}
	if strings.Contains(content, "systemic") || strings.Contains(content, "system") {
		angles = append(angles, "Systemic critique")
	}
	if strings.Contains(content, "optimization") {
		angles = append(angles, "optimization trap")
	}
	if strings.Contains(content, "economic") || strings.Contains(content, "economy") {
		angles = append(angles, "Economic analysis")
	}
	if strings.Contains(content, "extraction") || strings.Contains(content, "extracted") {
		angles = append(angles, "value extraction")
	}
	if strings.Contains(content, "builder") {
		angles = append(angles, "Builder-focused")
	}
	if strings.Contains(content, "political") {
		angles = append(angles, "Political lens")
	}

	if len(angles) >= 2 {
		return angles[0] + ", " + angles[1]
	} else if len(angles) == 1 {
		return angles[0]
	}
	return "General productivity"
}

func extractDataPoint(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return "Unknown"
	}
	content := string(data)

	// Look for percentage patterns
	percentRe := regexp.MustCompile(`(\d+)%`)
	matches := percentRe.FindAllStringSubmatch(content, -1)
	if len(matches) >= 2 {
		return fmt.Sprintf("Statistics (%s%%, %s%%)", matches[0][1], matches[1][1])
	} else if len(matches) == 1 {
		return fmt.Sprintf("Statistics (%s%%)", matches[0][1])
	}

	// Look for other data markers
	if strings.Contains(strings.ToLower(content), "cognitive") {
		return "Cognitive science research"
	}
	if strings.Contains(strings.ToLower(content), "study") || strings.Contains(strings.ToLower(content), "research") {
		return "Research-backed"
	}

	return "Anecdotal"
}

func extractTone(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return "Unknown"
	}
	content := strings.ToLower(string(data))

	tones := []string{}
	if strings.Contains(content, "builder") || strings.Contains(content, "operational") {
		tones = append(tones, "Builder-focused")
	}
	if strings.Contains(content, "political") || strings.Contains(content, "policy") {
		tones = append(tones, "Political")
	}
	if strings.Contains(content, "empathetic") || strings.Contains(content, "empathy") || strings.Contains(content, "human") {
		tones = append(tones, "Empathetic")
	}
	if strings.Contains(content, "critique") || strings.Contains(content, "critical") {
		tones = append(tones, "Critical")
	}
	if strings.Contains(content, "practical") || strings.Contains(content, "actionable") {
		tones = append(tones, "Practical")
	}
	if strings.Contains(content, "advocacy") || strings.Contains(content, "advocate") {
		tones = append(tones, "Advocacy-driven")
	}

	if len(tones) >= 2 {
		return tones[0] + ", " + tones[1]
	} else if len(tones) == 1 {
		return tones[0]
	}
	return "Neutral"
}

// Helper functions for report generation
func findStepOutput(stepName string) string {
	// Map step names to output files
	outputs := map[string]string{
		"research":     "docs/style-guide.md",
		"draft":        "docs/draft.md",
		"draft-codex":  "docs/draft-codex.md",
		"draft-gemini": "docs/draft-gemini.md",
		"edit":         "(title-based).md",
		"edit-codex":   "(title)-Codex.md",
		"edit-gemini":  "(title)-Gemini.md",
	}
	if out, ok := outputs[stepName]; ok {
		return "`" + out + "`"
	}
	return "-"
}

func findArticleFilesInDir(outputDir string) []string {
	if outputDir == "" {
		return nil
	}
	entries, err := os.ReadDir(outputDir)
	if err != nil {
		return nil
	}

	var articles []string
	for _, e := range entries {
		name := e.Name()
		nameLower := strings.ToLower(name)
		// Skip known non-article files
		if name == "style-guide.md" || name == "outline.md" ||
			strings.HasPrefix(nameLower, "draft") || name == "Run Report.md" {
			continue
		}
		if strings.HasSuffix(name, ".md") {
			articles = append(articles, filepath.Join(outputDir, name))
		}
	}
	return articles
}

func findArticleByTool(articles []string, tool string) string {
	toolLower := strings.ToLower(tool)
	for _, a := range articles {
		baseLower := strings.ToLower(filepath.Base(a))
		if strings.Contains(baseLower, toolLower) {
			return a
		}
	}
	return ""
}

func getArticleNames(paths []string) []string {
	var names []string
	for _, p := range paths {
		name := filepath.Base(p)
		name = strings.TrimSuffix(name, ".md")
		// Shorten for table
		if strings.Contains(name, "Codex") {
			names = append(names, "Codex")
		} else if strings.Contains(name, "Gemini") {
			names = append(names, "Gemini")
		} else {
			names = append(names, name)
		}
	}
	return names
}

func extractTitle(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return "Unknown"
	}
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "# ") {
			return strings.TrimPrefix(line, "# ")
		}
	}
	return filepath.Base(path)
}

func countWords(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	return len(strings.Fields(string(data)))
}

func extractOpening(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return "Unknown"
	}
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		// Skip title and empty lines
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Get first sentence or first 50 chars
		if len(line) > 50 {
			// Find first sentence
			if idx := strings.Index(line, ". "); idx > 0 && idx < 80 {
				return line[:idx+1]
			}
			return line[:47] + "..."
		}
		return line
	}
	return "Unknown"
}

// FinalReportJSON is the structure for the machine-readable final report
type FinalReportJSON struct {
	Meta    MetaInfo               `json:"meta"`
	Summary SummaryInfo            `json:"summary"`
	Costs   CostsInfo              `json:"costs"`
	Steps   []StepInfo             `json:"steps"`
	Outputs OutputsInfo            `json:"outputs"`
	Grade   *GradeInfo             `json:"grade,omitempty"`
	Inputs  map[string]string      `json:"inputs"`
}

type MetaInfo struct {
	JobID          string `json:"job_id"`
	Bundle         string `json:"bundle"`
	BundleSource   string `json:"bundle_source"`
	TimestampStart string `json:"timestamp_start"`
	TimestampEnd   string `json:"timestamp_end"`
	Status         string `json:"status"`
}

type SummaryInfo struct {
	TotalCostUSD    float64  `json:"total_cost_usd"`
	DurationSeconds int64    `json:"duration_seconds"`
	DurationHuman   string   `json:"duration_human"`
	RcodegenVersion string   `json:"rcodegen_version"`
	StepsTotal      int      `json:"steps_total"`
	StepsSucceeded  int      `json:"steps_succeeded"`
	StepsFailed     int      `json:"steps_failed"`
	ModelsUsed      []string `json:"models_used"`
}

type CostsInfo struct {
	TotalUSD float64               `json:"total_usd"`
	ByModel  map[string]ModelCosts `json:"by_model"`
	Totals   TokenTotals           `json:"totals"`
}

type ModelCosts struct {
	CostUSD          float64  `json:"cost_usd"`
	InputTokens      int      `json:"input_tokens"`
	OutputTokens     int      `json:"output_tokens"`
	CacheReadTokens  int      `json:"cache_read_tokens"`
	CacheWriteTokens int      `json:"cache_write_tokens"`
	Steps            []string `json:"steps"`
}

type TokenTotals struct {
	InputTokens      int `json:"input_tokens"`
	OutputTokens     int `json:"output_tokens"`
	CacheReadTokens  int `json:"cache_read_tokens"`
	CacheWriteTokens int `json:"cache_write_tokens"`
}

type StepInfo struct {
	Name             string            `json:"name"`
	Tool             string            `json:"tool"`
	Versions         map[string]string `json:"versions,omitempty"`
	Status           string            `json:"status"`
	CostUSD          float64           `json:"cost_usd"`
	InputTokens      int               `json:"input_tokens"`
	OutputTokens     int               `json:"output_tokens"`
	CacheReadTokens  int               `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int               `json:"cache_write_tokens,omitempty"`
	DurationSeconds  int64             `json:"duration_seconds,omitempty"`
}

type OutputsInfo struct {
	Directory string     `json:"directory"`
	Files     []FileInfo `json:"files"`
	Stats     OutputStats `json:"stats"`
}

type FileInfo struct {
	Path      string `json:"path"`
	Type      string `json:"type"`
	SizeBytes int64  `json:"size_bytes"`
	Lines     int    `json:"lines,omitempty"`
}

type OutputStats struct {
	TotalSourceFiles int `json:"total_source_files"`
	TotalSourceLines int `json:"total_source_lines"`
	TotalDocWords    int `json:"total_doc_words"`
}

type GradeInfo struct {
	Score          int `json:"score"`
	Letter         string `json:"letter"`
	Functionality  int `json:"functionality"`
	CodeQuality    int `json:"code_quality"`
	Security       int `json:"security"`
	UserExperience int `json:"user_experience"`
	Architecture   int `json:"architecture"`
	Testing        int `json:"testing"`
	Innovation     int `json:"innovation"`
	Documentation  int `json:"documentation"`
}

// generateFinalReportJSON creates a machine-readable JSON report for build bundles
func generateFinalReportJSON(
	projectDir string,
	jobID string,
	b *bundle.Bundle,
	startTime time.Time,
	duration time.Duration,
	totalCost float64,
	totalInputTokens int,
	totalOutputTokens int,
	totalCacheRead int,
	totalCacheWrite int,
	stepStats []StepStats,
	inputs map[string]string,
	ctx *Context,
) {
	endTime := startTime.Add(duration)

	// Build model costs map
	modelCosts := make(map[string]ModelCosts)
	modelsUsed := make(map[string]bool)
	stepsSucceeded := 0
	stepsFailed := 0

	var steps []StepInfo
	for _, s := range stepStats {
		if s.Tool != "" {
			modelsUsed[s.Tool] = true
			mc := modelCosts[s.Tool]
			mc.CostUSD += s.Cost
			mc.InputTokens += s.InputTokens
			mc.OutputTokens += s.OutputTokens
			mc.Steps = append(mc.Steps, s.Name)
			modelCosts[s.Tool] = mc
		}

		steps = append(steps, StepInfo{
			Name:         s.Name,
			Tool:         s.Tool,
			Status:       "success", // We wouldn't get here if a step failed
			CostUSD:      s.Cost,
			InputTokens:  s.InputTokens,
			OutputTokens: s.OutputTokens,
		})
		stepsSucceeded++
	}

	// Build models list
	var modelsList []string
	for m := range modelsUsed {
		modelsList = append(modelsList, m)
	}

	// Scan output files
	files, stats := scanOutputFiles(projectDir)

	// Try to extract grade from final-report.md
	grade := extractGradeFromReport(filepath.Join(projectDir, "final-report.md"))

	report := FinalReportJSON{
		Meta: MetaInfo{
			JobID:          jobID,
			Bundle:         b.Name,
			BundleSource:   b.SourcePath,
			TimestampStart: startTime.Format(time.RFC3339),
			TimestampEnd:   endTime.Format(time.RFC3339),
			Status:         "success",
		},
		Summary: SummaryInfo{
			TotalCostUSD:    totalCost,
			DurationSeconds: int64(duration.Seconds()),
			DurationHuman:   duration.Round(time.Second).String(),
			RcodegenVersion: getVersion(),
			StepsTotal:      len(stepStats),
			StepsSucceeded:  stepsSucceeded,
			StepsFailed:     stepsFailed,
			ModelsUsed:      modelsList,
		},
		Costs: CostsInfo{
			TotalUSD: totalCost,
			ByModel:  modelCosts,
			Totals: TokenTotals{
				InputTokens:      totalInputTokens,
				OutputTokens:     totalOutputTokens,
				CacheReadTokens:  totalCacheRead,
				CacheWriteTokens: totalCacheWrite,
			},
		},
		Steps:   steps,
		Outputs: OutputsInfo{
			Directory: projectDir,
			Files:     files,
			Stats:     stats,
		},
		Grade:  grade,
		Inputs: inputs,
	}

	// Write JSON file
	jsonPath := filepath.Join(projectDir, "final-report.json")
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to generate final-report.json: %v\n", err)
		return
	}
	if err := os.WriteFile(jsonPath, data, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to write final-report.json: %v\n", err)
	}
}

// scanOutputFiles scans a directory and returns file info and stats
func scanOutputFiles(dir string) ([]FileInfo, OutputStats) {
	var files []FileInfo
	var stats OutputStats

	filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}

		relPath, _ := filepath.Rel(dir, path)
		fileType := categorizeFile(relPath)

		fi := FileInfo{
			Path:      relPath,
			Type:      fileType,
			SizeBytes: info.Size(),
		}

		// Count lines for source files
		if fileType == "source" {
			if data, err := os.ReadFile(path); err == nil {
				lines := len(strings.Split(string(data), "\n"))
				fi.Lines = lines
				stats.TotalSourceLines += lines
				stats.TotalSourceFiles++
			}
		}

		// Count words for docs
		if fileType == "docs" || fileType == "report" {
			if data, err := os.ReadFile(path); err == nil {
				stats.TotalDocWords += len(strings.Fields(string(data)))
			}
		}

		files = append(files, fi)
		return nil
	})

	return files, stats
}

// categorizeFile determines the type of a file based on its path
func categorizeFile(path string) string {
	lower := strings.ToLower(path)
	ext := strings.ToLower(filepath.Ext(path))

	if strings.HasPrefix(lower, "src/") || strings.HasPrefix(lower, "lib/") {
		return "source"
	}
	if ext == ".py" || ext == ".go" || ext == ".js" || ext == ".ts" || ext == ".rb" || ext == ".rs" {
		return "source"
	}
	if strings.HasPrefix(lower, "samples/") || strings.HasPrefix(lower, "test") {
		return "sample"
	}
	if ext == ".pdf" {
		return "output"
	}
	if strings.Contains(lower, "report") {
		return "report"
	}
	if ext == ".md" || strings.Contains(lower, "readme") {
		return "docs"
	}
	if ext == ".json" {
		return "config"
	}
	return "other"
}

// extractGradeFromReport parses the final-report.md to extract the JSON grade block
func extractGradeFromReport(path string) *GradeInfo {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	content := string(data)

	// Find all JSON blocks and look for one containing "grade"
	remaining := content
	for {
		jsonBlockStart := strings.Index(remaining, "```json")
		if jsonBlockStart == -1 {
			break
		}
		jsonBlockEnd := strings.Index(remaining[jsonBlockStart+7:], "```")
		if jsonBlockEnd == -1 {
			break
		}

		jsonStr := strings.TrimSpace(remaining[jsonBlockStart+7 : jsonBlockStart+7+jsonBlockEnd])

		// Check if this block contains grade data
		if strings.Contains(jsonStr, `"grade"`) || strings.Contains(jsonStr, `"score"`) {
			// Try to parse the grade JSON
			var gradeWrapper struct {
				Grade GradeInfo `json:"grade"`
			}
			if err := json.Unmarshal([]byte(jsonStr), &gradeWrapper); err == nil && gradeWrapper.Grade.Score > 0 {
				return &gradeWrapper.Grade
			}
			// Try parsing as direct grade object
			var grade GradeInfo
			if err := json.Unmarshal([]byte(jsonStr), &grade); err == nil && grade.Score > 0 {
				return &grade
			}
		}

		// Move to next block
		remaining = remaining[jsonBlockStart+7+jsonBlockEnd:]
	}

	return nil
}

// extractOverviewFromSummary extracts the Overview section from IMPLEMENTATION_SUMMARY.md
func extractOverviewFromSummary(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}

	content := string(data)
	lines := strings.Split(content, "\n")

	// Find "## Overview" section
	inOverview := false
	var overviewLines []string

	for _, line := range lines {
		if strings.HasPrefix(line, "## Overview") {
			inOverview = true
			continue
		}
		if inOverview {
			// Stop at next section header
			if strings.HasPrefix(line, "## ") || strings.HasPrefix(line, "# ") {
				break
			}
			trimmed := strings.TrimSpace(line)
			if trimmed != "" {
				overviewLines = append(overviewLines, trimmed)
			}
		}
	}

	if len(overviewLines) == 0 {
		return ""
	}

	// Return first meaningful line (or combine if short)
	overview := strings.Join(overviewLines, " ")
	if len(overview) > 100 {
		overview = overview[:97] + "..."
	}
	return overview
}

// getVersion returns the rcodegen version from the VERSION file
func getVersion() string {
	// Try common locations
	candidates := []string{
		"VERSION",
		"../VERSION",
		"../../VERSION",
	}

	for _, path := range candidates {
		if data, err := os.ReadFile(path); err == nil {
			return strings.TrimSpace(string(data))
		}
	}

	return "unknown"
}
