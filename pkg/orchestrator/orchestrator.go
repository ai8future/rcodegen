package orchestrator

import (
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

// DispatcherFactory creates a dispatcher from a tool registry.
// This is set by the executor package to break the circular dependency.
var DispatcherFactory func(tools map[string]runner.Tool) StepExecutor

type Orchestrator struct {
	settings   *settings.Settings
	dispatcher StepExecutor
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
	}
}

func (o *Orchestrator) Run(b *bundle.Bundle, inputs map[string]string) (*envelope.Envelope, error) {
	start := time.Now()

	// Validate required inputs
	for _, input := range b.Inputs {
		if input.Required {
			if _, ok := inputs[input.Name]; !ok {
				if input.Default != "" {
					inputs[input.Name] = input.Default
				} else {
					return envelope.New().
						Failure("MISSING_INPUT", "Required input: "+input.Name).
						Build(), nil
				}
			}
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

	fmt.Printf("Job ID: %s\n", ws.JobID)
	fmt.Printf("Bundle: %s\n\n", b.Name)

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
		fmt.Printf("Output dir: %s\n\n", outputDir)
	}

	// Create context
	ctx := NewContext(inputs)

	// Track costs
	var totalCost float64
	var totalInputTokens, totalOutputTokens int
	var totalCacheRead, totalCacheWrite int
	var stepStats []StepStats

	// Execute steps
	for i, step := range b.Steps {
		fmt.Printf("[%d/%d] %s...\n", i+1, len(b.Steps), step.Name)

		// Check condition
		if step.If != "" && !EvaluateCondition(step.If, ctx) {
			fmt.Printf("  Skipped (condition false)\n")
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

		// Execute step
		env, err := o.dispatcher.Execute(&step, ctx, ws)
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

		// Track step stats for report
		isParallel := len(step.Parallel) > 0
		stepStats = append(stepStats, StepStats{
			Name:         step.Name,
			Tool:         step.Tool,
			Parallel:     isParallel,
			Cost:         stepCost,
			InputTokens:  stepIn,
			OutputTokens: stepOut,
		})

		if stepCost > 0 || stepIn > 0 || stepOut > 0 {
			fmt.Printf("  %s ($%.4f, in:%d out:%d)\n", env.Status, stepCost, stepIn, stepOut)
		} else {
			fmt.Printf("  %s\n", env.Status)
		}

		if env.Status == envelope.StatusFailure {
			return env, fmt.Errorf("step %s failed", step.Name)
		}
	}

	duration := time.Since(start)

	fmt.Printf("\nCompleted in %s\n", duration.Round(time.Second))
	fmt.Printf("Total cost: $%.4f\n", totalCost)
	fmt.Printf("Tokens: %d input, %d output", totalInputTokens, totalOutputTokens)
	if totalCacheRead > 0 || totalCacheWrite > 0 {
		fmt.Printf(" (cache: %d read, %d write)", totalCacheRead, totalCacheWrite)
	}
	fmt.Printf("\n")
	fmt.Printf("Output: %s\n", ws.JobDir)

	// Generate run report for article bundles
	if strings.HasPrefix(b.Name, "article") && outputDir != "" {
		reportPath := filepath.Join(outputDir, "Run Report.md")
		generateRunReport(reportPath, ws.JobID, b.Name, duration, totalCost, stepStats, ctx, outputDir)
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
				expanded = append(expanded, ExpandedStep{stepNum, "Edit", "Gemini", "✓", codexEditCost, codexOut})
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

	os.WriteFile(path, []byte(sb.String()), 0644)
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
