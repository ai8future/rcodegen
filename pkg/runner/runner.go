package runner

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"rcodegen/pkg/lock"
	"rcodegen/pkg/reports"
	"rcodegen/pkg/settings"
)

// noTrackStatus is a package-level variable used by defineToolSpecificFlags
// to capture the --no-status flag value, which is then applied after flag.Parse()
var noTrackStatus bool

// Runner orchestrates the execution of a tool
type Runner struct {
	Tool         Tool
	Settings     *settings.Settings
	TaskConfig   *settings.TaskConfig
	SettingsOK   bool
}

// NewRunner creates a new Runner for the given tool
func NewRunner(tool Tool) *Runner {
	return &Runner{
		Tool: tool,
	}
}

// SettingsAware is an optional interface tools can implement to receive settings
type SettingsAware interface {
	SetSettings(s *settings.Settings)
}

// Run is the main entry point - loads settings, parses args, and executes
func (r *Runner) Run() {
	// Load settings from settings.json (or run interactive setup)
	var ok bool
	r.Settings, ok = settings.LoadOrSetup()
	if !ok {
		fmt.Fprintln(os.Stderr, "Setup cancelled or failed. Exiting.")
		os.Exit(1)
	}
	r.SettingsOK = true

	// Create initial TaskConfig with empty codebase for task name lookup
	r.TaskConfig = r.Settings.ToTaskConfig("")

	// Pass settings to tool if it implements SettingsAware
	if sa, ok := r.Tool.(SettingsAware); ok {
		sa.SetSettings(r.Settings)
	}

	// Parse command line arguments
	cfg := r.parseArgs()

	// Regenerate TaskConfig with actual codebase name for pattern substitution
	if cfg.Codebase != "" {
		r.TaskConfig = r.Settings.ToTaskConfig(cfg.Codebase)
	}

	// Substitute {report_dir} in all task prompts
	// Use custom output dir if specified, otherwise use tool's default (_claude/_codex)
	reportDir := r.Tool.ReportDir()
	if cfg.OutputDir != "" {
		reportDir = cfg.OutputDir
		// Create the output directory if it doesn't exist
		if err := os.MkdirAll(reportDir, 0755); err != nil {
			fmt.Fprintf(os.Stderr, "Error creating output directory %s: %v\n", reportDir, err)
			os.Exit(1)
		}
	}
	for name, prompt := range r.TaskConfig.Tasks {
		r.TaskConfig.Tasks[name] = strings.ReplaceAll(prompt, "{report_dir}", reportDir)
	}
	// Also substitute in the already-expanded cfg.Task (from parseArgs)
	cfg.Task = strings.ReplaceAll(cfg.Task, "{report_dir}", reportDir)

	// Handle status-only mode
	if cfg.StatusOnly {
		r.Tool.ShowStatus()
		os.Exit(0)
	}

	// Validate task is provided
	if cfg.Task == "" {
		fmt.Fprintln(os.Stderr, "Error: No task provided.")
		r.printUsage()
		os.Exit(1)
	}

	// Prepare tool for execution (deferred expensive setup)
	r.Tool.PrepareForExecution(cfg)

	// Show startup banner (unless outputting JSON stats only)
	if !cfg.StatsJSON {
		PrintStartupBanner(r.Tool, cfg)
	}

	// Validate all working directories upfront
	for _, workDir := range cfg.WorkDirs {
		if workDir != "" {
			if info, err := os.Stat(workDir); err != nil || !info.IsDir() {
				fmt.Fprintf(os.Stderr, "Error: Directory does not exist: %s\n", workDir)
				os.Exit(1)
			}
		}
	}

	// Acquire lock if needed (shared across all codebases)
	var lockHandle *lock.FileLock
	if cfg.UseLock {
		identifier := "multi-codebase"
		if len(cfg.WorkDirs) == 1 {
			identifier = lock.GetIdentifier(cfg.WorkDirs[0])
		}
		var err error
		lockHandle, err = lock.Acquire(identifier, true)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		defer lockHandle.Release()
	}

	// Record overall start time
	overallStart := time.Now()
	overallExit := 0

	// Capture status before all tasks (if supported)
	var statusBefore interface{}
	if cfg.TrackStatus && r.Tool.SupportsStatusTracking() {
		fmt.Printf("%sCapturing credit status before task...%s\n", Dim, Reset)
		statusBefore = r.Tool.CaptureStatusBefore()
	}

	// Run for each working directory
	workDirs := cfg.WorkDirs
	if len(workDirs) == 0 {
		workDirs = []string{""} // Current directory
	}

	for i, workDir := range workDirs {
		// Show header for multiple codebases
		if len(cfg.WorkDirs) > 1 {
			PrintCodebaseHeader(i+1, len(cfg.WorkDirs), workDir)
		}

		exitCode := r.runForWorkDir(cfg, workDir)
		if exitCode != 0 {
			overallExit = exitCode
		}
	}

	// Capture status after all tasks (if supported)
	var statusAfter interface{}
	if cfg.TrackStatus && r.Tool.SupportsStatusTracking() {
		fmt.Printf("\n%sCapturing credit status after task...%s\n", Dim, Reset)
		statusAfter = r.Tool.CaptureStatusAfter()
	}

	// Display overall summary
	overallDuration := time.Since(overallStart)
	endTime := time.Now()

	// Get primary working directory for summary and runlog
	primaryWorkDir := ""
	if len(cfg.WorkDirs) > 0 {
		primaryWorkDir = cfg.WorkDirs[0]
	}

	if len(cfg.WorkDirs) > 1 {
		PrintMultiCodebaseSummary(len(cfg.WorkDirs), overallDuration, overallExit)
		if cfg.TrackStatus && statusBefore != nil && statusAfter != nil {
			r.Tool.PrintStatusSummary(statusBefore, statusAfter)
		}
	} else {
		r.printDetailedSummary(cfg, primaryWorkDir, overallStart, overallDuration, overallExit, statusBefore, statusAfter)
	}

	// Output JSON stats if requested
	if cfg.StatsJSON {
		OutputStatsJSON(r.Tool, cfg, overallStart, endTime, overallExit)
	}

	// Write run log file
	r.writeRunLog(cfg, primaryWorkDir, overallStart, endTime, overallExit)

	os.Exit(overallExit)
}

// runForWorkDir runs the task for a single working directory
func (r *Runner) runForWorkDir(cfg *Config, workDir string) int {
	startTime := time.Now()

	// Execute the task
	var exitCode int
	if cfg.Task == "suite" {
		exitCode = r.runMultipleReports(cfg, workDir)
	} else {
		// Check if we should skip due to unreviewed previous report
		reportDir := r.getReportDir(cfg, workDir)
		pattern := r.TaskConfig.ReportPatterns[cfg.TaskShortcut]
		if reports.ShouldSkipTask(reportDir, cfg.TaskShortcut, pattern, cfg.RequireReview) {
			exitCode = 0 // Skipped, not an error
		} else {
			exitCode = r.runSingleTask(cfg, workDir)
		}
	}

	// Record end time
	duration := time.Since(startTime)

	// Delete old reports if requested
	if cfg.DeleteOld && exitCode == 0 {
		var shortcuts []string
		switch cfg.TaskShortcut {
		case "suite":
			shortcuts = []string{"audit", "test", "fix", "refactor"}
		case "":
			// Custom task, no deletion
		default:
			shortcuts = []string{cfg.TaskShortcut}
		}
		if len(shortcuts) > 0 {
			fmt.Println()
			reportDir := r.getReportDir(cfg, workDir)
			reports.DeleteOldReports(reportDir, shortcuts, r.TaskConfig.ReportPatterns)
		}
	}

	// Display summary for single codebase runs within multi-codebase
	if len(cfg.WorkDirs) <= 1 {
		// Summary will be printed at the end
	}

	_ = duration // Used in multi-codebase mode
	return exitCode
}

// getReportDir returns the report directory path for a working directory
func (r *Runner) getReportDir(cfg *Config, workDir string) string {
	// Use custom output dir if specified (replaces tool-specific _claude/_codex)
	if cfg.OutputDir != "" {
		return cfg.OutputDir
	}
	// Default behavior: use tool's report dir (_claude or _codex)
	reportDirName := r.Tool.ReportDir()
	if workDir != "" {
		return filepath.Join(workDir, reportDirName)
	}
	if cwd, err := os.Getwd(); err == nil {
		return filepath.Join(cwd, reportDirName)
	}
	return reportDirName
}

// runSingleTask runs a single task
func (r *Runner) runSingleTask(cfg *Config, workDir string) int {
	return r.executeCommand(cfg, workDir, cfg.Task)
}

// executeCommand builds and runs the tool command
func (r *Runner) executeCommand(cfg *Config, workDir, task string) int {
	cmd := r.Tool.BuildCommand(cfg, workDir, task)

	// If tool uses stream output (like Claude's stream-json) and not in JSON mode,
	// parse and format the output nicely
	if r.Tool.UsesStreamOutput() && !cfg.OutputJSON {
		return r.executeWithStreamParser(cfg, cmd)
	}

	// Default: direct output passthrough
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin

	err := cmd.Run()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode()
		}
		return 1
	}
	return 0
}

// executeWithStreamParser runs a command and parses stream-json output
func (r *Runner) executeWithStreamParser(cfg *Config, cmd *exec.Cmd) int {
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		fmt.Fprintf(os.Stderr, "%sError:%s Could not create stdout pipe: %v\n", Yellow, Reset, err)
		return 1
	}

	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin

	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "%sError:%s Could not start command: %v\n", Yellow, Reset, err)
		return 1
	}

	// Parse and format the output
	parser := NewStreamParser(os.Stdout)
	parser.ProcessReader(stdout)

	// Capture token usage from parser
	if parser.Usage != nil {
		cfg.TokenUsage = parser.Usage
	}
	if parser.TotalCostUSD > 0 {
		cfg.TotalCostUSD = parser.TotalCostUSD
	}

	err = cmd.Wait()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode()
		}
		return 1
	}
	return 0
}

// runMultipleReports runs the "suite" meta-task (4 sequential reports)
func (r *Runner) runMultipleReports(cfg *Config, workDir string) int {
	overallExit := 0
	reportTypes := []string{"audit", "test", "fix", "refactor"}

	fmt.Printf("%s%sRunning all 4 report types sequentially...%s\n\n", Bold, Cyan, Reset)

	// Run each report type
	for _, reportType := range reportTypes {
		PrintReportHeader(reportType)

		// Check if we should skip this report type
		reportDir := r.getReportDir(cfg, workDir)
		pattern := r.TaskConfig.ReportPatterns[reportType]
		if reports.ShouldSkipTask(reportDir, reportType, pattern, cfg.RequireReview) {
			fmt.Println()
			continue
		}

		reportStart := time.Now()
		exitCode := r.executeCommand(cfg, workDir, r.TaskConfig.Tasks[reportType])
		reportDuration := time.Since(reportStart)
		PrintReportProgress(reportType, reportDuration, exitCode)
		if exitCode != 0 {
			overallExit = exitCode
		}
	}

	return overallExit
}

// printDetailedSummary prints a detailed summary for single-codebase runs
func (r *Runner) printDetailedSummary(cfg *Config, workDir string, startTime time.Time, duration time.Duration, exitCode int, statusBefore, statusAfter interface{}) {
	fmt.Println()
	fmt.Printf("%s%s══════════════════════════════════════════%s\n", Bold, Cyan, Reset)
	fmt.Printf("%s%s  Run Summary%s\n", Bold, Cyan, Reset)
	fmt.Printf("%s%s══════════════════════════════════════════%s\n", Bold, Cyan, Reset)
	fmt.Printf("  %sCommand:%s      %s %s\n", Dim, Reset, r.Tool.Name(), cfg.OriginalCmd)

	if cfg.TaskShortcut != "" {
		fmt.Printf("  %sShortcut:%s     %s%s%s\n", Dim, Reset, Magenta, cfg.TaskShortcut, Reset)
	}

	fmt.Printf("  %sStarted:%s      %s\n", Dim, Reset, startTime.Format("2006-01-02 15:04:05"))
	fmt.Printf("  %sDuration:%s     %s%s%s\n", Dim, Reset, Yellow, FormatDuration(duration), Reset)
	fmt.Printf("  %sModel:%s        %s\n", Dim, Reset, cfg.Model)

	// Tool-specific summary fields
	r.Tool.PrintToolSpecificSummaryFields(cfg)

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

	// Print token usage if available
	if cfg.TokenUsage != nil {
		fmt.Printf("  %s───────────────────────────────────────%s\n", Dim, Reset)
		fmt.Printf("  %sTokens in:%s    %s%d%s\n", Dim, Reset, Cyan, cfg.TokenUsage.InputTokens, Reset)
		fmt.Printf("  %sTokens out:%s   %s%d%s\n", Dim, Reset, Cyan, cfg.TokenUsage.OutputTokens, Reset)
		if cfg.TokenUsage.CacheReadInputTokens > 0 || cfg.TokenUsage.CacheCreationInputTokens > 0 {
			fmt.Printf("  %sCache:%s        %d read, %d created\n", Dim, Reset,
				cfg.TokenUsage.CacheReadInputTokens, cfg.TokenUsage.CacheCreationInputTokens)
		}
		if cfg.TotalCostUSD > 0 {
			fmt.Printf("  %sCost:%s         %s$%.4f%s\n", Dim, Reset, Yellow, cfg.TotalCostUSD, Reset)
		}
	}

	// Print tool-specific status summary
	if cfg.TrackStatus && statusBefore != nil && statusAfter != nil {
		fmt.Printf("  %s───────────────────────────────────────%s\n", Dim, Reset)
		r.Tool.PrintStatusSummary(statusBefore, statusAfter)
	}

	fmt.Printf("%s%s══════════════════════════════════════════%s\n", Bold, Cyan, Reset)
}

// parseArgs parses command line arguments
func (r *Runner) parseArgs() *Config {
	cfg := NewConfig()

	// Apply tool defaults
	cfg.Model = r.Tool.DefaultModel()
	r.Tool.ApplyToolDefaults(cfg)

	// Build flag groups for duplicate checking
	flagGroups := CommonFlagGroups()
	for _, fd := range r.Tool.ToolSpecificFlags() {
		if fd.Short != "" || fd.Long != "" {
			names := []string{}
			if fd.Short != "" {
				names = append(names, fd.Short)
			}
			if fd.Long != "" {
				names = append(names, fd.Long)
			}
			flagGroups = append(flagGroups, FlagAliases{Names: names, TakesArg: fd.TakesArg})
		}
	}

	// Check for conflicting duplicate flags before parsing
	if err := CheckDuplicateFlags(os.Args[1:], flagGroups); err != nil {
		ExitWithError("%v", err)
	}

	// Extract -x flags before standard flag parsing
	cleanedArgs, vars := ParseVarFlags(os.Args[1:])
	cfg.Vars = vars
	os.Args = append([]string{os.Args[0]}, cleanedArgs...)

	// Define common flags
	var codePath, dirPath string
	var showTasks, showHelp bool

	flag.StringVar(&codePath, "c", "", "Project path relative to configured code directory")
	flag.StringVar(&codePath, "code", "", "Project path relative to configured code directory")
	flag.StringVar(&dirPath, "d", "", "Set working directory to absolute path")
	flag.StringVar(&dirPath, "dir", "", "Set working directory to absolute path")
	flag.StringVar(&cfg.OutputDir, "o", "", "Output directory for reports (replaces _claude/_codex)")
	flag.StringVar(&cfg.OutputDir, "output", "", "Output directory for reports (replaces _claude/_codex)")
	flag.BoolVar(&cfg.OutputJSON, "j", false, "Output as newline-delimited JSON")
	flag.BoolVar(&cfg.OutputJSON, "json", false, "Output as newline-delimited JSON")
	flag.BoolVar(&cfg.UseLock, "l", false, "Queue behind other running instances")
	flag.BoolVar(&cfg.UseLock, "lock", false, "Queue behind other running instances")
	flag.BoolVar(&cfg.DeleteOld, "D", false, "Delete previous reports with same pattern after run")
	flag.BoolVar(&cfg.DeleteOld, "delete-old", false, "Delete previous reports with same pattern after run")
	flag.BoolVar(&cfg.RequireReview, "R", false, "Skip tasks if previous report lacks 'Date Modified:'")
	flag.BoolVar(&cfg.RequireReview, "require-review", false, "Skip tasks if previous report lacks 'Date Modified:'")
	flag.StringVar(&cfg.Model, "m", cfg.Model, "Specify model")
	flag.StringVar(&cfg.Model, "model", cfg.Model, "Specify model")
	flag.BoolVar(&cfg.StatsJSON, "J", false, "Output run statistics as JSON")
	flag.BoolVar(&cfg.StatsJSON, "stats-json", false, "Output run statistics as JSON")
	flag.BoolVar(&cfg.StatusOnly, "status-only", false, "Show status and exit")
	flag.BoolVar(&showTasks, "t", false, "List available task shortcuts")
	flag.BoolVar(&showTasks, "tasks", false, "List available task shortcuts")
	flag.BoolVar(&showHelp, "h", false, "Show help message")
	flag.BoolVar(&showHelp, "help", false, "Show help message")

	// Define tool-specific flags
	r.defineToolSpecificFlags(cfg)

	flag.Usage = r.printUsage
	flag.Parse()

	// Handle --no-status flag (must be after Parse)
	if noTrackStatus {
		cfg.TrackStatus = false
	}

	// Handle special flags
	if showHelp {
		r.printUsage()
		os.Exit(0)
	}

	if showTasks {
		r.listTasks()
		os.Exit(0)
	}

	// Validate tool-specific configuration
	if err := r.Tool.ValidateConfig(cfg); err != nil {
		ExitWithError("%v", err)
	}

	// Set working directories (supports comma-separated list)
	if codePath != "" {
		if !r.SettingsOK {
			settings.PrintSetupInstructions(r.Tool.Name())
			fmt.Fprintf(os.Stderr, "  %sUsing fallback:%s %s~/Desktop/_code%s\n\n", Dim, Reset, Magenta, Reset)
		}
		// Split on comma for multiple codebases
		paths := strings.Split(codePath, ",")
		for _, p := range paths {
			p = strings.TrimSpace(p)
			if p != "" {
				cfg.WorkDirs = append(cfg.WorkDirs, filepath.Join(r.Settings.GetCodeDir(), p))
				// Use first codebase name for report filenames
				if cfg.Codebase == "" {
					cfg.Codebase = p
				}
			}
		}
	} else if dirPath != "" {
		// Split on comma for multiple directories
		paths := strings.Split(dirPath, ",")
		for _, p := range paths {
			p = strings.TrimSpace(p)
			if p != "" {
				cfg.WorkDirs = append(cfg.WorkDirs, p)
				// Use first directory basename as codebase name for report filenames
				if cfg.Codebase == "" {
					cfg.Codebase = filepath.Base(p)
				}
			}
		}
	} else {
		// No directory specified - use current directory's basename as codebase name
		if cwd, err := os.Getwd(); err == nil {
			cfg.Codebase = filepath.Base(cwd)
		}
	}

	// Apply settings default for output directory if not specified via CLI
	if cfg.OutputDir == "" && r.Settings.OutputDir != "" {
		cfg.OutputDir = r.Settings.OutputDir
	}

	// Get task from remaining args
	args := flag.Args()
	if len(args) > 0 {
		cfg.Task = args[0]
	}

	// Build original command string for summary
	cfg.OriginalCmd = strings.Join(os.Args[1:], " ")

	// Expand task shortcut if it matches
	if cfg.Task != "" && cfg.Task != "suite" {
		if r.TaskConfig != nil {
			if expanded, ok := r.TaskConfig.Tasks[cfg.Task]; ok {
				cfg.TaskShortcut = cfg.Task
				cfg.Task = expanded
			}
		}
	} else if cfg.Task == "suite" {
		cfg.TaskShortcut = cfg.Task
	}

	// Apply variable substitution to task
	if cfg.Task != "" && len(cfg.Vars) > 0 {
		for key, value := range cfg.Vars {
			placeholder := "{" + key + "}"
			cfg.Task = strings.ReplaceAll(cfg.Task, placeholder, value)
		}
	}

	// Validate no unsubstituted placeholders remain (skip for "suite" meta-task)
	// System placeholders (report_dir, report_file, codebase) are substituted later
	if cfg.Task != "" && cfg.Task != "suite" {
		re := regexp.MustCompile(`\{([a-zA-Z_][a-zA-Z0-9_]*)\}`)
		systemPlaceholders := map[string]bool{
			"report_dir":  true,
			"report_file": true,
			"codebase":    true,
		}
		if matches := re.FindAllStringSubmatch(cfg.Task, -1); len(matches) > 0 {
			var missing []string
			for _, m := range matches {
				if !systemPlaceholders[m[1]] {
					missing = append(missing, m[1])
				}
			}
			if len(missing) > 0 {
				ExitWithError("Missing variables: %s%s%s\nUse %s-x name=value%s to provide them",
					Green, strings.Join(missing, ", "), Reset, Green, Reset)
			}
		}
	}

	return cfg
}

// defineToolSpecificFlags defines flags specific to this tool
func (r *Runner) defineToolSpecificFlags(cfg *Config) {
	for _, fd := range r.Tool.ToolSpecificFlags() {
		switch fd.Target {
		case "MaxBudget":
			if fd.Short != "" {
				flag.StringVar(&cfg.MaxBudget, strings.TrimPrefix(fd.Short, "-"), fd.Default, fd.Description)
			}
			if fd.Long != "" {
				flag.StringVar(&cfg.MaxBudget, strings.TrimPrefix(fd.Long, "--"), fd.Default, fd.Description)
			}
		case "Effort":
			if fd.Short != "" {
				flag.StringVar(&cfg.Effort, strings.TrimPrefix(fd.Short, "-"), fd.Default, fd.Description)
			}
			if fd.Long != "" {
				flag.StringVar(&cfg.Effort, strings.TrimPrefix(fd.Long, "--"), fd.Default, fd.Description)
			}
		case "TrackStatus":
			if fd.Short != "" {
				flag.BoolVar(&cfg.TrackStatus, strings.TrimPrefix(fd.Short, "-"), true, fd.Description)
			}
			if fd.Long != "" {
				flag.BoolVar(&cfg.TrackStatus, strings.TrimPrefix(fd.Long, "--"), true, fd.Description)
			}
		case "NoTrackStatus":
			if fd.Short != "" {
				flag.BoolVar(&noTrackStatus, strings.TrimPrefix(fd.Short, "-"), false, fd.Description)
			}
			if fd.Long != "" {
				flag.BoolVar(&noTrackStatus, strings.TrimPrefix(fd.Long, "--"), false, fd.Description)
			}
		}
	}
}

// listTasks displays available task shortcuts
func (r *Runner) listTasks() {
	fmt.Println("Available task shortcuts:")
	if r.TaskConfig != nil {
		for name, desc := range r.TaskConfig.Tasks {
			fmt.Printf("\n  %s:\n", name)
			// Truncate long descriptions
			if len(desc) > 100 {
				fmt.Printf("    %s...\n", desc[:100])
			} else {
				fmt.Printf("    %s\n", desc)
			}
		}
	}
	fmt.Printf("\n  suite:\n")
	fmt.Printf("    Run audit, test, fix, refactor sequentially as 4 separate sessions\n")
}

// writeRunLog writes a .runlog file with run metadata
func (r *Runner) writeRunLog(cfg *Config, workDir string, startTime, endTime time.Time, exitCode int) {
	// Determine output directory
	outputDir := r.getReportDir(cfg, workDir)

	// Create directory if it doesn't exist
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "%sWarning:%s Could not create runlog directory: %v\n", Yellow, Reset, err)
		return
	}

	// Build filename: {codebase}-{task}-YYYY-MM-DD_HHMM.runlog
	taskName := cfg.TaskShortcut
	if taskName == "" {
		taskName = "custom"
	}
	codebaseName := cfg.Codebase
	if codebaseName == "" {
		codebaseName = "unnamed"
	}
	timestamp := startTime.Format("2006-01-02_1504")
	filename := fmt.Sprintf("%s-%s-%s.runlog", codebaseName, taskName, timestamp)
	filepath := filepath.Join(outputDir, filename)

	// Build content
	var lines []string
	lines = append(lines, fmt.Sprintf("Tool:      %s", r.Tool.Name()))

	// Add tool-specific fields (Model, Budget/Effort)
	for _, field := range r.Tool.RunLogFields(cfg) {
		lines = append(lines, field)
	}

	lines = append(lines, fmt.Sprintf("Codebase:  %s", codebaseName))
	lines = append(lines, fmt.Sprintf("Output:    %s", outputDir))
	lines = append(lines, fmt.Sprintf("Command:   %s %s", r.Tool.Name(), cfg.OriginalCmd))
	lines = append(lines, fmt.Sprintf("Started:   %s", startTime.Format("2006-01-02 15:04:05")))
	lines = append(lines, fmt.Sprintf("Ended:     %s", endTime.Format("2006-01-02 15:04:05")))
	lines = append(lines, fmt.Sprintf("Duration:  %s", FormatDuration(endTime.Sub(startTime))))
	lines = append(lines, fmt.Sprintf("Exit Code: %d", exitCode))

	// Add token usage if available
	if cfg.TokenUsage != nil {
		lines = append(lines, "")
		lines = append(lines, "--- Token Usage ---")
		lines = append(lines, fmt.Sprintf("Input:     %d", cfg.TokenUsage.InputTokens))
		lines = append(lines, fmt.Sprintf("Output:    %d", cfg.TokenUsage.OutputTokens))
		if cfg.TokenUsage.CacheReadInputTokens > 0 {
			lines = append(lines, fmt.Sprintf("Cache Read: %d", cfg.TokenUsage.CacheReadInputTokens))
		}
		if cfg.TokenUsage.CacheCreationInputTokens > 0 {
			lines = append(lines, fmt.Sprintf("Cache Create: %d", cfg.TokenUsage.CacheCreationInputTokens))
		}
		if cfg.TotalCostUSD > 0 {
			lines = append(lines, fmt.Sprintf("Cost:      $%.4f", cfg.TotalCostUSD))
		}
	}

	content := strings.Join(lines, "\n") + "\n"

	// Write file
	if err := os.WriteFile(filepath, []byte(content), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "%sWarning:%s Could not write runlog: %v\n", Yellow, Reset, err)
		return
	}
}

// printUsage prints the help message
func (r *Runner) printUsage() {
	configPath := settings.GetConfigPath()
	toolName := r.Tool.Name()

	// Header
	fmt.Printf("%s%sUsage:%s %s %s[OPTIONS]%s %s\"<task>\"%s\n\n", Bold, Cyan, Reset, toolName, Dim, Reset, Yellow, Reset)
	fmt.Printf("Run %s in unattended mode with a one-shot task.\n\n", r.Tool.BannerSubtitle())

	// Directory Options
	fmt.Printf("%s%sDirectory Options:%s\n", Bold, Cyan, Reset)
	fmt.Printf("  %s-c%s, %s--code%s %s<path>%s     Project path relative to configured code directory\n", Green, Reset, Green, Reset, Yellow, Reset)
	fmt.Printf("                        %s(comma-separated for multiple: -c proj1,proj2)%s\n", Dim, Reset)
	fmt.Printf("  %s-d%s, %s--dir%s %s<path>%s      Set working directory to absolute path\n", Green, Reset, Green, Reset, Yellow, Reset)
	fmt.Printf("                        %s(comma-separated for multiple: -d /a,/b)%s\n", Dim, Reset)
	fmt.Printf("  %s-o%s, %s--output%s %s<path>%s   Output directory for reports %s(replaces _claude/_codex)%s\n\n", Green, Reset, Green, Reset, Yellow, Reset, Dim, Reset)

	// Execution Options
	fmt.Printf("%s%sExecution Options:%s\n", Bold, Cyan, Reset)
	fmt.Printf("  %s-m%s, %s--model%s %s<name>%s    Specify model %s(default: %s)%s\n", Green, Reset, Green, Reset, Yellow, Reset, Dim, r.Tool.DefaultModel(), Reset)
	fmt.Printf("  %s-l%s, %s--lock%s            Queue behind other running %s instances\n", Green, Reset, Green, Reset, toolName)
	fmt.Printf("  %s-j%s, %s--json%s            Output as newline-delimited JSON\n", Green, Reset, Green, Reset)
	fmt.Printf("  %s-J%s, %s--stats-json%s      Output run statistics as JSON at completion\n\n", Green, Reset, Green, Reset)

	// Tool-specific help sections
	for _, section := range r.Tool.ToolSpecificHelpSections() {
		fmt.Printf("%s%s%s:%s\n", Bold, Cyan, section.Title, Reset)
		for _, line := range section.Lines {
			fmt.Println(line)
		}
		fmt.Println()
	}

	// Report Options
	fmt.Printf("%s%sReport Options:%s\n", Bold, Cyan, Reset)
	fmt.Printf("  %s-D%s, %s--delete-old%s      Delete previous reports with same pattern after run\n", Green, Reset, Green, Reset)
	fmt.Printf("  %s-R%s, %s--require-review%s  Skip if previous report unreviewed (no 'Date Modified:')\n\n", Green, Reset, Green, Reset)

	// Variable Substitution
	fmt.Printf("%s%sVariable Substitution:%s\n", Bold, Cyan, Reset)
	fmt.Printf("  %s-x%s %s<key=value>%s       Set variable for task template %s(can repeat)%s\n", Green, Reset, Yellow, Reset, Dim, Reset)
	fmt.Printf("                        Variables use %s{name}%s syntax in prompts\n\n", Yellow, Reset)

	// Other Options
	fmt.Printf("%s%sOther Options:%s\n", Bold, Cyan, Reset)
	fmt.Printf("  %s--status-only%s         Show status and exit\n", Green, Reset)
	fmt.Printf("  %s-t%s, %s--tasks%s           List available task shortcuts\n", Green, Reset, Green, Reset)
	fmt.Printf("  %s-h%s, %s--help%s            Show this help message\n\n", Green, Reset, Green, Reset)

	// Configuration
	fmt.Printf("%s%sConfiguration:%s\n", Bold, Cyan, Reset)
	fmt.Printf("  Settings loaded from: %s%s%s\n", Magenta, configPath, Reset)
	fmt.Printf("  Falls back to %s~/Desktop/_code%s if not configured.\n", Dim, Reset)
	fmt.Printf("  See %ssettings.json.example%s for format.\n\n", Magenta, Reset)

	// Task Shortcuts
	fmt.Printf("%s%sTask Shortcuts:%s\n", Bold, Cyan, Reset)
	fmt.Printf("  Loaded from %ssettings.json%s + built-in: %ssuite%s (runs all 4 sequentially)\n", Magenta, Reset, Yellow, Reset)
	fmt.Printf("  Use %s-t%s to see full list.\n\n", Green, Reset)

	// Security Note
	warnings := r.Tool.SecurityWarning()
	fmt.Printf("%s%sSecurity Note:%s\n", Bold, Yellow, Reset)
	for _, line := range warnings {
		fmt.Printf("  %s%s%s\n", Yellow, line, Reset)
	}
}
