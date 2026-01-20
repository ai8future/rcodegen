// Package runner provides the core execution framework for rcodegen tools.
// It handles argument parsing, task execution, and output formatting.
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

// RunResult holds the result of a Run() invocation
type RunResult struct {
	ExitCode     int
	TokenUsage   *TokenUsage
	TotalCostUSD float64
	Error        error
}

// runError creates a RunResult for an error condition
func runError(code int, err error) *RunResult {
	return &RunResult{ExitCode: code, Error: err}
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

// RunAndExit runs the task and exits with the appropriate code
// This is the entry point for CLI binaries
func (r *Runner) RunAndExit() {
	result := r.Run()
	if result.Error != nil {
		fmt.Fprintln(os.Stderr, result.Error)
	}
	os.Exit(result.ExitCode)
}

// Run is the main entry point - loads settings, parses args, and executes
func (r *Runner) Run() *RunResult {
	// Load settings from settings.json (or run interactive setup)
	var ok bool
	r.Settings, ok = settings.LoadOrSetup()
	if !ok {
		return runError(1, fmt.Errorf("setup cancelled or failed"))
	}
	r.SettingsOK = true

	// Create initial TaskConfig with empty codebase for task name lookup
	r.TaskConfig = r.Settings.ToTaskConfig("", r.Tool.ReportPrefix())

	// Pass settings to tool if it implements SettingsAware
	if sa, ok := r.Tool.(SettingsAware); ok {
		sa.SetSettings(r.Settings)
	}

	// Parse command line arguments
	cfg, err := r.parseArgs()
	if err != nil {
		return runError(1, err)
	}
	if cfg == nil {
		// Help or tasks were shown, exit cleanly
		return &RunResult{ExitCode: 0}
	}

	// Regenerate TaskConfig with actual codebase name for pattern substitution
	if cfg.Codebase != "" {
		r.TaskConfig = r.Settings.ToTaskConfig(cfg.Codebase, r.Tool.ReportPrefix())
	}

	// Substitute {report_dir} in all task prompts
	// Use custom output dir if specified, otherwise use unified _rcodegen directory
	reportDir := r.Tool.ReportDir()
	if cfg.OutputDir != "" {
		reportDir = cfg.OutputDir
		// Create the output directory if it doesn't exist
		if err := os.MkdirAll(reportDir, 0755); err != nil {
			return runError(1, fmt.Errorf("error creating output directory %s: %v", reportDir, err))
		}
	}
	// Substitute {report_dir} in the task being executed (don't mutate shared TaskConfig)
	cfg.Task = strings.ReplaceAll(cfg.Task, "{report_dir}", reportDir)

	// Handle status-only mode
	if cfg.StatusOnly {
		r.Tool.ShowStatus()
		return &RunResult{ExitCode: 0}
	}

	// Validate task is provided
	if cfg.Task == "" {
		r.printUsage()
		return runError(1, fmt.Errorf("no task provided"))
	}

	// Prepare tool for execution (deferred expensive setup)
	r.Tool.PrepareForExecution(cfg)

	// Show startup banner (unless outputting JSON stats only)
	if !cfg.StatsJSON {
		PrintStartupBanner(r.Tool, cfg)
	}

	// Validate all working directories upfront
	reportDirName := r.Tool.ReportDir()
	for i, workDir := range cfg.WorkDirs {
		if workDir != "" {
			if info, err := os.Stat(workDir); err != nil || !info.IsDir() {
				return runError(1, fmt.Errorf("directory does not exist: %s", workDir))
			}
			// Auto-correct if running inside an _rcodegen directory (move up to parent)
			if filepath.Base(workDir) == reportDirName {
				parentDir := filepath.Dir(workDir)
				fmt.Printf("%sNote:%s Adjusting from %s to parent directory %s\n", Yellow, Reset, reportDirName, parentDir)
				cfg.WorkDirs[i] = parentDir
			}
		}
	}

	// Also check current directory when no workDirs specified
	if len(cfg.WorkDirs) == 0 {
		if cwd, err := os.Getwd(); err == nil {
			if filepath.Base(cwd) == reportDirName {
				parentDir := filepath.Dir(cwd)
				fmt.Printf("%sNote:%s Adjusting from %s to parent directory %s\n", Yellow, Reset, reportDirName, parentDir)
				cfg.WorkDirs = []string{parentDir}
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
			return runError(1, err)
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

	return &RunResult{
		ExitCode:     overallExit,
		TokenUsage:   cfg.TokenUsage,
		TotalCostUSD: cfg.TotalCostUSD,
	}
}

// runForWorkDir runs the task for a single working directory
func (r *Runner) runForWorkDir(cfg *Config, workDir string) int {
	startTime := time.Now()

	// Execute the task
	var exitCode int
	if cfg.Task == TaskSuite {
		exitCode = r.runMultipleReports(cfg, workDir)
	} else {
		// Check if we should skip due to unreviewed previous report
		reportDir := r.getReportDir(cfg, workDir)
		pattern := r.TaskConfig.ReportPatterns[cfg.TaskShortcut]
		if reports.ShouldSkipTask(reportDir, cfg.TaskShortcut, pattern, cfg.RequireReview) {
			exitCode = 0 // Skipped, not an error
		} else {
			exitCode = r.runSingleTask(cfg, workDir)
			// Persist grade after successful task completion
			if exitCode == 0 && cfg.TaskShortcut != "" {
				r.persistGrade(cfg, workDir, cfg.TaskShortcut)
			}
		}
	}

	// Record end time
	duration := time.Since(startTime)

	// Delete old reports if requested
	if cfg.DeleteOld && exitCode == 0 {
		var shortcuts []string
		switch cfg.TaskShortcut {
		case TaskSuite:
			shortcuts = ReportTypes
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

// getTask returns a task prompt with placeholders substituted
// This avoids mutating the shared TaskConfig
func (r *Runner) getTask(cfg *Config, workDir, taskName string) string {
	task := r.TaskConfig.Tasks[taskName]
	reportDir := r.getReportDir(cfg, workDir)
	return strings.ReplaceAll(task, "{report_dir}", reportDir)
}

// getReportDir returns the report directory path for a working directory
func (r *Runner) getReportDir(cfg *Config, workDir string) string {
	// Use custom output dir if specified (replaces _rcodegen)
	if cfg.OutputDir != "" {
		return cfg.OutputDir
	}
	// Default behavior: use tool's report dir (_rcodegen)
	reportDirName := r.Tool.ReportDir()
	if workDir != "" {
		return filepath.Join(workDir, reportDirName)
	}
	if cwd, err := os.Getwd(); err == nil {
		return filepath.Join(cwd, reportDirName)
	}
	return reportDirName
}

// persistGrade extracts the grade from the newest report and saves it to .grades.json
func (r *Runner) persistGrade(cfg *Config, workDir, taskShortcut string) {
	reportDir := r.getReportDir(cfg, workDir)
	toolName := strings.ToLower(r.Tool.Name())

	// Retry loop to find the newest report file (it might take a moment to appear)
	var reportPath string
	var err error
	for i := 0; i < 10; i++ {
		reportPath, err = FindNewestReport(reportDir, toolName, taskShortcut)
		if err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if err != nil {
		// Report not found - this can happen if the tool didn't create one
		// Debug: fmt.Fprintf(os.Stderr, "%sDebug:%s No report found for %s/%s: %v\n", Dim, Reset, toolName, taskShortcut, err)
		return
	}

	// Verify file exists and is readable
	if _, err := os.Stat(reportPath); err != nil {
		return
	}

	// Extract grade from the report
	grade, err := ExtractGradeFromReport(reportPath)
	if err != nil {
		// No grade found in report - common for some task types
		// Debug: fmt.Fprintf(os.Stderr, "%sDebug:%s No grade in %s: %v\n", Dim, Reset, filepath.Base(reportPath), err)
		return
	}

	// Parse the filename to get the date
	filename := filepath.Base(reportPath)
	_, _, _, date, err := ParseReportFilename(filename)
	if err != nil {
		// Use current time if filename parsing fails
		date = time.Now()
	}

	// Append to .grades.json
	if err := AppendGrade(reportDir, filename, toolName, taskShortcut, grade, date); err != nil {
		fmt.Fprintf(os.Stderr, "%sWarning:%s Could not save grade: %v\n", Yellow, Reset, err)
	}
}

// runSingleTask runs a single task
func (r *Runner) runSingleTask(cfg *Config, workDir string) int {
	if cfg.DryRun {
		cmd := r.Tool.BuildCommand(cfg, workDir, cfg.Task)
		fmt.Printf("%s%sDry run - would execute:%s\n", Bold, Cyan, Reset)
		fmt.Printf("  %sCommand:%s %s\n", Dim, Reset, cmd.Path)
		fmt.Printf("  %sArgs:%s %v\n", Dim, Reset, cmd.Args[1:])
		fmt.Printf("  %sDir:%s %s\n", Dim, Reset, cmd.Dir)
		fmt.Printf("  %sTask:%s\n%s\n", Dim, Reset, cfg.Task)
		return 0
	}
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
	if err := parser.ProcessReader(stdout); err != nil {
		fmt.Fprintf(os.Stderr, "%sWarning:%s Stream parsing error: %v\n", Yellow, Reset, err)
	}

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

// runMultipleReports runs the "suite" meta-task (5 sequential reports)
func (r *Runner) runMultipleReports(cfg *Config, workDir string) int {
	overallExit := 0

	fmt.Printf("%s%sRunning all %d report types sequentially...%s\n\n", Bold, Cyan, len(ReportTypes), Reset)

	// Run each report type
	for _, reportType := range ReportTypes {
		PrintReportHeader(reportType)

		// Check if we should skip this report type
		reportDir := r.getReportDir(cfg, workDir)
		pattern := r.TaskConfig.ReportPatterns[reportType]
		if reports.ShouldSkipTask(reportDir, reportType, pattern, cfg.RequireReview) {
			fmt.Println()
			continue
		}

		// Handle dry run mode
		if cfg.DryRun {
			task := r.getTask(cfg, workDir, reportType)
			cmd := r.Tool.BuildCommand(cfg, workDir, task)
			fmt.Printf("%s%sDry run - would execute %s:%s\n", Bold, Cyan, reportType, Reset)
			fmt.Printf("  %sCommand:%s %s\n", Dim, Reset, cmd.Path)
			fmt.Printf("  %sArgs:%s %v\n", Dim, Reset, cmd.Args[1:])
			fmt.Printf("  %sDir:%s %s\n", Dim, Reset, cmd.Dir)
			fmt.Println()
			continue
		}

		reportStart := time.Now()
		exitCode := r.executeCommand(cfg, workDir, r.getTask(cfg, workDir, reportType))
		reportDuration := time.Since(reportStart)
		PrintReportProgress(reportType, reportDuration, exitCode)
		if exitCode != 0 {
			overallExit = exitCode
		} else {
			// Persist grade after successful report completion
			r.persistGrade(cfg, workDir, reportType)
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

// setWorkingDirectories configures the working directories based on -c or -d flags.
// Returns an error if -c is used but code_dir is not configured.
func (r *Runner) setWorkingDirectories(cfg *Config, codePath, dirPath string) error {
	if codePath != "" {
		// Check if code_dir is configured when using -c flag
		if !r.Settings.IsCodeDirConfigured() {
			settings.PrintCodeDirWarning()
			return fmt.Errorf("code_dir not configured - cannot use -c flag")
		}
		// Split on comma for multiple codebases
		for _, p := range strings.Split(codePath, ",") {
			p = strings.TrimSpace(p)
			if p != "" {
				cfg.WorkDirs = append(cfg.WorkDirs, filepath.Join(r.Settings.GetCodeDir(), p))
				if cfg.Codebase == "" {
					cfg.Codebase = p
				}
			}
		}
	} else if dirPath != "" {
		// Split on comma for multiple directories
		for _, p := range strings.Split(dirPath, ",") {
			p = strings.TrimSpace(p)
			if p != "" {
				cfg.WorkDirs = append(cfg.WorkDirs, p)
				if cfg.Codebase == "" {
					cfg.Codebase = filepath.Base(p)
				}
			}
		}
	} else {
		// No directory specified - use current directory's basename
		if cwd, err := os.Getwd(); err == nil {
			cfg.Codebase = filepath.Base(cwd)
		}
	}
	return nil
}

// expandTaskShortcut expands a task shortcut to its full prompt if it exists.
func (r *Runner) expandTaskShortcut(cfg *Config) {
	if cfg.Task == "" || cfg.Task == TaskSuite {
		if cfg.Task == TaskSuite {
			cfg.TaskShortcut = cfg.Task
		}
		return
	}
	if r.TaskConfig != nil {
		if expanded, ok := r.TaskConfig.Tasks[cfg.Task]; ok {
			cfg.TaskShortcut = cfg.Task
			cfg.Task = expanded
		}
	}
}

// applyVariableSubstitution replaces {var} placeholders with -x flag values.
func applyVariableSubstitution(cfg *Config) {
	if cfg.Task == "" || len(cfg.Vars) == 0 {
		return
	}
	for key, value := range cfg.Vars {
		placeholder := "{" + key + "}"
		cfg.Task = strings.ReplaceAll(cfg.Task, placeholder, value)
	}
}

// validatePlaceholders checks for unsubstituted placeholders and returns an error if found.
func validatePlaceholders(task string) error {
	if task == "" || task == TaskSuite {
		return nil
	}
	re := regexp.MustCompile(`\{([a-zA-Z_][a-zA-Z0-9_]*)\}`)
	systemPlaceholders := map[string]bool{
		"report_dir":  true,
		"report_file": true,
		"codebase":    true,
	}
	matches := re.FindAllStringSubmatch(task, -1)
	if len(matches) == 0 {
		return nil
	}
	var missing []string
	for _, m := range matches {
		if !systemPlaceholders[m[1]] {
			missing = append(missing, m[1])
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing variables: %s\nUse -x name=value to provide them",
			strings.Join(missing, ", "))
	}
	return nil
}

// parseArgs parses command line arguments.
// Returns (*Config, nil) on success.
// Returns (nil, nil) when help or tasks were shown (exit 0).
// Returns (nil, error) on error (exit 1).
func (r *Runner) parseArgs() (*Config, error) {
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
		return nil, err
	}

	// Extract -x flags before standard flag parsing
	cleanedArgs, vars := ParseVarFlags(os.Args[1:])
	cfg.Vars = vars

	// Reorder args so flags can appear anywhere (before or after task)
	cleanedArgs = reorderArgsForFlagParsing(cleanedArgs, flagGroups)
	os.Args = append([]string{os.Args[0]}, cleanedArgs...)

	// Define common flags
	var codePath, dirPath string
	var showTasks, showHelp, migrateGrades, migrateGradesAll bool

	flag.StringVar(&codePath, "c", "", "Project path relative to configured code directory")
	flag.StringVar(&codePath, "code", "", "Project path relative to configured code directory")
	flag.StringVar(&dirPath, "d", "", "Set working directory to absolute path")
	flag.StringVar(&dirPath, "dir", "", "Set working directory to absolute path")
	flag.StringVar(&cfg.OutputDir, "o", "", "Output directory for reports (replaces _rcodegen)")
	flag.StringVar(&cfg.OutputDir, "output", "", "Output directory for reports (replaces _rcodegen)")
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
	flag.BoolVar(&cfg.DryRun, "n", false, "Dry run - show command without executing")
	flag.BoolVar(&cfg.DryRun, "dry-run", false, "Dry run - show command without executing")
	flag.BoolVar(&showTasks, "t", false, "List available task shortcuts")
	flag.BoolVar(&showTasks, "tasks", false, "List available task shortcuts")
	flag.BoolVar(&showHelp, "h", false, "Show help message")
	flag.BoolVar(&showHelp, "help", false, "Show help message")
	flag.BoolVar(&migrateGrades, "migrate-grades", false, "Migrate existing reports to .grades.json")
	flag.BoolVar(&migrateGradesAll, "migrate-grades-all", false, "Migrate grades for all repos in code directory")

	// Define tool-specific flags
	r.defineToolSpecificFlags(cfg)

	flag.Usage = r.printUsage
	flag.Parse()

	// Handle --no-status flag (must be after Parse)
	if noTrackStatus {
		cfg.TrackStatus = false
		cfg.NoTrackStatus = true
	}

	// Handle special flags - return nil config to signal exit 0
	if showHelp {
		r.printUsage()
		return nil, nil
	}

	if showTasks {
		r.listTasks()
		return nil, nil
	}

	if migrateGradesAll {
		// Migrate grades for all repos in the code directory
		codeDir := r.Settings.CodeDir
		if codeDir == "" {
			return nil, fmt.Errorf("code_dir not configured in settings")
		}
		fmt.Printf("%s%sMigrating grades for all repos in %s%s\n", Bold, Cyan, codeDir, Reset)
		if err := MigrateGradesAll(codeDir); err != nil {
			return nil, err
		}
		return nil, nil
	}

	if migrateGrades {
		// Migrate grades for current directory or specified path
		targetDir := dirPath
		if targetDir == "" {
			if codePath != "" && r.Settings.CodeDir != "" {
				targetDir = filepath.Join(r.Settings.CodeDir, codePath)
			} else {
				targetDir, _ = os.Getwd()
			}
		}
		fmt.Printf("%s%sMigrating grades in %s%s\n", Bold, Cyan, targetDir, Reset)
		if err := MigrateGrades(targetDir); err != nil {
			return nil, err
		}
		return nil, nil
	}

	// Validate tool-specific configuration
	if err := r.Tool.ValidateConfig(cfg); err != nil {
		return nil, err
	}

	// Set working directories (supports comma-separated list)
	if err := r.setWorkingDirectories(cfg, codePath, dirPath); err != nil {
		return nil, err
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

	// Expand task shortcut and apply variable substitution
	r.expandTaskShortcut(cfg)
	applyVariableSubstitution(cfg)

	// Validate no unsubstituted placeholders remain
	if err := validatePlaceholders(cfg.Task); err != nil {
		return nil, err
	}

	return cfg, nil
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
		case "Flash":
			if fd.Short != "" {
				flag.BoolVar(&cfg.Flash, strings.TrimPrefix(fd.Short, "-"), false, fd.Description)
			}
			if fd.Long != "" {
				flag.BoolVar(&cfg.Flash, strings.TrimPrefix(fd.Long, "--"), false, fd.Description)
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
	fmt.Printf("    Run audit, test, fix, refactor, quick sequentially as 5 separate sessions\n")
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
	fmt.Printf("  %s-o%s, %s--output%s %s<path>%s   Output directory for reports %s(replaces _rcodegen)%s\n\n", Green, Reset, Green, Reset, Yellow, Reset, Dim, Reset)

	// Execution Options
	fmt.Printf("%s%sExecution Options:%s\n", Bold, Cyan, Reset)
	fmt.Printf("  %s-m%s, %s--model%s %s<name>%s    Specify model %s(default: %s)%s\n", Green, Reset, Green, Reset, Yellow, Reset, Dim, r.Tool.DefaultModel(), Reset)
	fmt.Printf("  %s-n%s, %s--dry-run%s         Show command without executing\n", Green, Reset, Green, Reset)
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
	fmt.Printf("  Configure %scode_dir%s in ~/.rcodegen/settings.json to use -c flag.\n", Dim, Reset)
	fmt.Printf("  See %ssettings.json.example%s for format.\n\n", Magenta, Reset)

	// Task Shortcuts
	fmt.Printf("%s%sTask Shortcuts:%s\n", Bold, Cyan, Reset)
	fmt.Printf("  Loaded from %ssettings.json%s + built-in: %ssuite%s (runs all 5 sequentially)\n", Magenta, Reset, Yellow, Reset)
	fmt.Printf("  Use %s-t%s to see full list.\n\n", Green, Reset)

	// Security Note
	warnings := r.Tool.SecurityWarning()
	fmt.Printf("%s%sSecurity Note:%s\n", Bold, Yellow, Reset)
	for _, line := range warnings {
		fmt.Printf("  %s%s%s\n", Yellow, line, Reset)
	}
}
