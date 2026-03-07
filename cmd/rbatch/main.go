// rbatch is a CLI for batch-executing multiple coding agent tasks with
// concurrency control, session chaining, budget awareness, and checkpoint/resume.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"rcodegen/pkg/batch"
	"rcodegen/pkg/colors"
	"rcodegen/pkg/runner"
	"rcodegen/pkg/settings"

	chassis "github.com/ai8future/chassis-go/v7"
	"github.com/ai8future/chassis-go/v7/registry"
)

func main() {
	chassis.RequireMajor(7)
	if err := registry.InitCLI(chassis.Version); err != nil {
		log.Fatalf("registry: %v", err)
	}

	if len(os.Args) < 2 {
		printUsage()
		registry.ShutdownCLI(1)
		os.Exit(1)
	}

	// Handle top-level -v flag before subcommand dispatch.
	if os.Args[1] == "-v" || os.Args[1] == "--version" {
		fmt.Printf("rbatch %s\n", runner.GetVersion())
		registry.ShutdownCLI(0)
		os.Exit(0)
	}

	subcommand := os.Args[1]
	subArgs := os.Args[2:]

	var exitCode int
	switch subcommand {
	case "run":
		exitCode = cmdRun(subArgs)
	case "spool":
		exitCode = cmdSpool(subArgs)
	case "watch":
		exitCode = cmdWatch(subArgs)
	case "resume":
		exitCode = cmdResume(subArgs)
	case "status":
		exitCode = cmdStatus(subArgs)
	case "help", "--help", "-h":
		printUsage()
		exitCode = 0
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", subcommand)
		printUsage()
		exitCode = 1
	}

	registry.ShutdownCLI(exitCode)
	os.Exit(exitCode)
}

// cmdRun implements: rbatch run <manifest.json> [flags]
func cmdRun(args []string) int {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	concurrency := fs.Int("concurrency", 0, "override manifest concurrency (0 = use manifest value)")
	threshold := fs.Int("threshold", 0, "budget threshold percentage (0 = disabled)")
	onBudget := fs.String("on-budget", "", "budget action: stop, wait, ask")
	maxWait := fs.String("max-wait", "", "max wait duration when on-budget=wait (e.g. 2h)")
	serverAddr := fs.String("server", "", "rserve address for remote execution (e.g. localhost:9090)")
	dryRun := fs.Bool("dry-run", false, "show execution plan without running")
	verbose := fs.Bool("v", false, "verbose output")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: rbatch run <manifest.json> [flags]\n\nFlags:\n")
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		return 1
	}

	if fs.NArg() < 1 {
		fmt.Fprintf(os.Stderr, "error: manifest file path required\n")
		fs.Usage()
		return 1
	}

	manifestPath := fs.Arg(0)

	m, err := batch.LoadManifest(manifestPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	// Apply CLI overrides.
	if *concurrency > 0 {
		m.Concurrency = *concurrency
	}
	if *threshold > 0 {
		m.Budget.ThresholdPct = *threshold
	}
	if *onBudget != "" {
		m.Budget.OnBudget = *onBudget
	}
	if *maxWait != "" {
		m.Budget.MaxWait = *maxWait
	}

	if *dryRun {
		printDryRun(m)
		return 0
	}

	// Create executor.
	exec, cleanup := createExecutor(*serverAddr, *verbose)
	defer cleanup()

	// Set up signal-aware context.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// Create and configure the batch runner.
	br := batch.NewBatchRunner(m, exec)
	start := time.Now()
	br.OnEvent = makeEventHandler(m, *verbose, start)

	// Run the batch.
	result := br.Run(ctx)

	// Persist results.
	writeBatchResults(m.Name, result)

	// Print summary.
	fmt.Fprintln(os.Stderr) // clear progress line
	batch.PrintBatchSummary(result)

	if result.JobsFailed > 0 || result.Status == "stopped" || result.Status == "cancelled" {
		return 1
	}
	return 0
}

// cmdSpool implements: rbatch spool <directory> [flags]
func cmdSpool(args []string) int {
	fs := flag.NewFlagSet("spool", flag.ExitOnError)
	serverAddr := fs.String("server", "", "rserve address for remote execution")
	verbose := fs.Bool("v", false, "verbose output")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: rbatch spool <directory> [flags]\n\nFlags:\n")
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		return 1
	}

	if fs.NArg() < 1 {
		fmt.Fprintf(os.Stderr, "error: spool directory required\n")
		fs.Usage()
		return 1
	}

	spoolDir := fs.Arg(0)

	sp := batch.NewSpool(spoolDir)
	if err := sp.Init(); err != nil {
		fmt.Fprintf(os.Stderr, "error initializing spool: %v\n", err)
		return 1
	}

	manifests, err := sp.Scan()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error scanning spool: %v\n", err)
		return 1
	}

	if len(manifests) == 0 {
		fmt.Println("no pending manifests found")
		return 0
	}

	fmt.Printf("found %d manifest(s) in spool\n", len(manifests))

	// Create executor.
	exec, cleanup := createExecutor(*serverAddr, *verbose)
	defer cleanup()

	// Set up signal-aware context.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// Collect filenames corresponding to each manifest (same order from Scan).
	pendingDir := filepath.Join(spoolDir, "pending")
	pendingEntries, _ := os.ReadDir(pendingDir)
	var jsonFiles []string
	for _, e := range pendingEntries {
		if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".json") {
			jsonFiles = append(jsonFiles, e.Name())
		}
	}
	// Sort to match Scan ordering.
	sort.Strings(jsonFiles)

	totalFailed := 0
	for i, m := range manifests {
		// Check for cancellation between manifests.
		select {
		case <-ctx.Done():
			fmt.Fprintf(os.Stderr, "\ninterrupted after %d/%d manifests\n", i, len(manifests))
			return 1
		default:
		}

		// Determine the spool filename for this manifest.
		var filename string
		if i < len(jsonFiles) {
			filename = jsonFiles[i]
		}

		fmt.Printf("\n--- manifest %d/%d: %s ---\n", i+1, len(manifests), m.Name)

		// Mark as running in the spool.
		if filename != "" {
			_ = sp.MarkRunning(filename)
		}

		br := batch.NewBatchRunner(m, exec)
		start := time.Now()
		br.OnEvent = makeEventHandler(m, *verbose, start)

		result := br.Run(ctx)

		writeBatchResults(m.Name, result)
		fmt.Fprintln(os.Stderr) // clear progress line
		batch.PrintBatchSummary(result)

		// Move to done or failed in the spool.
		if filename != "" {
			if result.JobsFailed > 0 || result.Status == "stopped" || result.Status == "cancelled" {
				_ = sp.MarkFailed(filename)
			} else {
				_ = sp.MarkDone(filename)
			}
		}

		if result.JobsFailed > 0 {
			totalFailed += result.JobsFailed
		}
	}

	if totalFailed > 0 {
		return 1
	}
	return 0
}

// cmdWatch implements: rbatch watch <directory> (stub)
func cmdWatch(args []string) int {
	fmt.Fprintf(os.Stderr, "watch is not yet implemented, use spool\n")
	return 1
}

// cmdResume implements: rbatch resume [state.json] [flags]
func cmdResume(args []string) int {
	fs := flag.NewFlagSet("resume", flag.ExitOnError)
	serverAddr := fs.String("server", "", "rserve address for remote execution")
	concurrency := fs.Int("concurrency", 0, "override concurrency (0 = use checkpoint value)")
	verbose := fs.Bool("v", false, "verbose output")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: rbatch resume [state.json] [flags]\n\nFlags:\n")
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		return 1
	}

	// Determine checkpoint path.
	var cpPath string
	if fs.NArg() > 0 {
		cpPath = fs.Arg(0)
	} else {
		// Find the latest checkpoint under ~/.rcodegen/batches/
		home, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: cannot determine home directory: %v\n", err)
			return 1
		}
		baseDir := filepath.Join(home, ".rcodegen", "batches")
		found, err := batch.FindLatestCheckpoint(baseDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		cpPath = found
		fmt.Printf("resuming from: %s\n", cpPath)
	}

	cp, err := batch.LoadCheckpoint(cpPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error loading checkpoint: %v\n", err)
		return 1
	}

	if cp.Snapshot == nil || len(cp.Snapshot.Pending) == 0 {
		fmt.Println("no pending jobs to resume")
		return 0
	}

	fmt.Printf("checkpoint: %s (%s)\n", cp.Batch, cp.Reason)
	fmt.Printf("resuming %d pending job(s)\n", len(cp.Snapshot.Pending))

	// Build a manifest from the pending jobs.
	m := &batch.Manifest{
		Name:        cp.Batch + "-resumed",
		Concurrency: 1,
		Jobs:        cp.Snapshot.Pending,
	}

	if *concurrency > 0 {
		m.Concurrency = *concurrency
	}

	// Create executor.
	exec, cleanup := createExecutor(*serverAddr, *verbose)
	defer cleanup()

	// Set up signal-aware context.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	br := batch.NewBatchRunner(m, exec)
	start := time.Now()
	br.OnEvent = makeEventHandler(m, *verbose, start)

	result := br.Run(ctx)

	// Carry forward cost from completed jobs in the checkpoint.
	result.TotalCost += cp.Snapshot.TotalCost

	writeBatchResults(m.Name, result)
	fmt.Fprintln(os.Stderr) // clear progress line
	batch.PrintBatchSummary(result)

	if result.JobsFailed > 0 || result.Status == "stopped" || result.Status == "cancelled" {
		return 1
	}
	return 0
}

// cmdStatus implements: rbatch status [batch-name]
func cmdStatus(args []string) int {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: rbatch status [batch-name]\n")
	}

	if err := fs.Parse(args); err != nil {
		return 1
	}

	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: cannot determine home directory: %v\n", err)
		return 1
	}
	baseDir := filepath.Join(home, ".rcodegen", "batches")

	// If a specific batch name is given, show just that one.
	if fs.NArg() > 0 {
		batchName := fs.Arg(0)
		return showBatchStatus(filepath.Join(baseDir, batchName))
	}

	// Otherwise list all batches.
	entries, err := os.ReadDir(baseDir)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println("no batches found")
			return 0
		}
		fmt.Fprintf(os.Stderr, "error reading batches directory: %v\n", err)
		return 1
	}

	if len(entries) == 0 {
		fmt.Println("no batches found")
		return 0
	}

	fmt.Printf("%s%-20s  %-12s  %-6s  %-6s  %-8s  %s%s\n",
		colors.Bold, "BATCH", "STATUS", "TOTAL", "FAIL", "COST", "CHECKPOINT", colors.Reset)

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		batchDir := filepath.Join(baseDir, e.Name())
		printBatchRow(e.Name(), batchDir)
	}

	return 0
}

// showBatchStatus prints detailed info for a single batch directory.
func showBatchStatus(batchDir string) int {
	name := filepath.Base(batchDir)

	// Try to read summary.json
	summaryPath := filepath.Join(batchDir, "summary.json")
	if data, err := os.ReadFile(summaryPath); err == nil {
		var result batch.BatchResult
		if err := json.Unmarshal(data, &result); err == nil {
			fmt.Printf("%sBatch: %s%s\n", colors.Bold, name, colors.Reset)
			batch.PrintBatchSummary(&result)
		}
	}

	// Try to read state.json
	statePath := filepath.Join(batchDir, "state.json")
	if data, err := os.ReadFile(statePath); err == nil {
		var cp batch.Checkpoint
		if err := json.Unmarshal(data, &cp); err == nil {
			fmt.Printf("\n%sCheckpoint:%s\n", colors.Bold, colors.Reset)
			fmt.Printf("  At:      %s\n", cp.CheckpointAt)
			fmt.Printf("  Reason:  %s\n", cp.Reason)
			if cp.Snapshot != nil {
				fmt.Printf("  Pending: %d jobs\n", len(cp.Snapshot.Pending))
				fmt.Printf("  Cost:    $%.2f\n", cp.Snapshot.TotalCost)
			}
		}
	} else if os.IsNotExist(err) {
		// Check if the directory even exists.
		if _, statErr := os.Stat(batchDir); os.IsNotExist(statErr) {
			fmt.Fprintf(os.Stderr, "batch %q not found\n", name)
			return 1
		}
	}

	return 0
}

// printBatchRow prints a single row in the status table for a batch directory.
func printBatchRow(name, batchDir string) {
	status := "-"
	total := "-"
	failed := "-"
	cost := "-"
	checkpoint := "-"

	// Try to read summary.
	summaryPath := filepath.Join(batchDir, "summary.json")
	if data, err := os.ReadFile(summaryPath); err == nil {
		var result batch.BatchResult
		if err := json.Unmarshal(data, &result); err == nil {
			status = result.Status
			total = fmt.Sprintf("%d", result.JobsTotal)
			failed = fmt.Sprintf("%d", result.JobsFailed)
			cost = fmt.Sprintf("$%.2f", result.TotalCost)
		}
	}

	// Try to read state.
	statePath := filepath.Join(batchDir, "state.json")
	if info, err := os.Stat(statePath); err == nil {
		checkpoint = info.ModTime().Format("Jan 02 15:04")
	}

	fmt.Printf("%-20s  %-12s  %-6s  %-6s  %-8s  %s\n", name, status, total, failed, cost, checkpoint)
}

// createExecutor returns a LocalExecutor or RemoteExecutor depending on flags,
// along with a cleanup function that should be deferred.
func createExecutor(serverAddr string, verbose bool) (batch.Executor, func()) {
	if serverAddr != "" {
		if verbose {
			fmt.Printf("connecting to rserve at %s\n", serverAddr)
		}
		exec, err := batch.NewRemoteExecutor(serverAddr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		return exec, func() { exec.Close() }
	}

	// Local executor.
	s, _, err := settings.LoadWithFallback()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error loading settings: %v\n", err)
		os.Exit(1)
	}

	if verbose {
		fmt.Println("using local executor")
	}

	exec := batch.NewLocalExecutor(s)
	return exec, func() {} // no cleanup needed for local
}

// printDryRun shows the execution plan without running any jobs.
func printDryRun(m *batch.Manifest) {
	fmt.Printf("%sDry Run: %s%s\n\n", colors.Bold, m.Name, colors.Reset)
	fmt.Printf("  Concurrency:  %d\n", m.Concurrency)
	fmt.Printf("  Budget:       threshold=%d%% on_budget=%s max_wait=%s\n",
		m.Budget.ThresholdPct, m.Budget.OnBudget, m.Budget.MaxWait)
	fmt.Printf("  Total jobs:   %d\n\n", len(m.Jobs))

	groups := batch.BuildSessionGroups(m.Jobs)
	fmt.Printf("%sSession Groups (%d):%s\n", colors.Bold, len(groups), colors.Reset)

	for i, g := range groups {
		sessionLabel := "(standalone)"
		if g.Session != "" {
			sessionLabel = fmt.Sprintf("session=%q", g.Session)
		}
		fmt.Printf("\n  Group %d [%s] %s\n", i+1, g.ID, sessionLabel)

		for j, job := range g.Jobs {
			toolColor := colors.Cyan
			fmt.Printf("    %d. %s%-8s%s %s%s%s",
				j+1, toolColor, job.Tool, colors.Reset,
				colors.Bold, job.Name, colors.Reset)
			if job.Dir != "" {
				fmt.Printf("  dir=%s", job.Dir)
			}
			if job.Model != "" {
				fmt.Printf("  model=%s", job.Model)
			}
			fmt.Println()
			// Truncate task for display.
			task := job.Task
			if len(task) > 80 {
				task = task[:77] + "..."
			}
			fmt.Printf("       %s%s%s\n", colors.Dim, task, colors.Reset)
		}
	}

	// Summarize tools used.
	toolCounts := map[string]int{}
	for _, job := range m.Jobs {
		toolCounts[job.Tool]++
	}
	fmt.Printf("\n%sTools:%s", colors.Bold, colors.Reset)
	for tool, count := range toolCounts {
		fmt.Printf("  %s=%d", tool, count)
	}
	fmt.Println()
}

// makeEventHandler returns an OnEvent callback for progress reporting.
func makeEventHandler(m *batch.Manifest, verbose bool, start time.Time) func(batch.BatchEvent) {
	total := len(m.Jobs)
	var mu sync.Mutex
	completed := 0
	failed := 0
	active := 0
	var cost float64

	return func(e batch.BatchEvent) {
		mu.Lock()
		defer mu.Unlock()

		switch e.Type {
		case "group_start":
			if verbose {
				fmt.Fprintf(os.Stderr, "\n  group %s started\n", e.GroupID)
			}
		case "job_start":
			active++
			if verbose {
				fmt.Fprintf(os.Stderr, "\n  %s[start]%s %s (group %s)\n",
					colors.Cyan, colors.Reset, e.JobName, e.GroupID)
			}
			batch.PrintLiveStatus(m.Name, completed, failed, total, active, cost, time.Since(start))
		case "job_complete":
			active--
			completed++
			if e.Result != nil {
				cost += e.Result.Cost
			}
			if verbose {
				dur := ""
				if e.Result != nil {
					dur = e.Result.Duration
				}
				fmt.Fprintf(os.Stderr, "\n  %s[done]%s  %s (%s)\n",
					colors.Green, colors.Reset, e.JobName, dur)
			}
			batch.PrintLiveStatus(m.Name, completed, failed, total, active, cost, time.Since(start))
		case "job_fail":
			active--
			failed++
			if e.Result != nil {
				cost += e.Result.Cost
			}
			errMsg := ""
			if e.Result != nil && e.Result.Error != "" {
				errMsg = ": " + e.Result.Error
			}
			fmt.Fprintf(os.Stderr, "\n  %s[FAIL]%s  %s%s\n",
				colors.Red, colors.Reset, e.JobName, errMsg)
			batch.PrintLiveStatus(m.Name, completed, failed, total, active, cost, time.Since(start))
		case "budget_check":
			if verbose {
				fmt.Fprintf(os.Stderr, "\n  budget check: %d%% remaining\n", e.Remaining)
			}
		}
	}
}

// writeBatchResults persists the summary and per-job results to the batch directory.
func writeBatchResults(batchName string, result *batch.BatchResult) {
	if batchName == "" {
		batchName = "unnamed"
	}

	dir, err := batch.BatchDir(batchName)
	if err != nil {
		return // best-effort
	}

	if err := batch.WriteSummary(dir, result); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not write summary: %v\n", err)
	}
}

// printUsage prints the top-level help message.
func printUsage() {
	name := "rbatch"
	version := runner.GetVersion()

	fmt.Fprintf(os.Stderr, `%s %s - batch job runner for coding agents

Usage:
  %s <command> [flags]

Commands:
  run <manifest.json>    Execute a batch manifest
  spool <directory>      Process manifests from a spool directory
  watch <directory>      Watch a spool directory (not yet implemented)
  resume [state.json]    Resume from a checkpoint
  status [batch-name]    Show batch status

Flags:
  -v                     Show version

Run '%s <command> --help' for command-specific help.
`, name, version, name, name)

	// Collect unique tool names from the listing for context.
	fmt.Fprintf(os.Stderr, `
Supported tools: %s

Examples:
  %s run batch.json
  %s run batch.json --concurrency 4 --dry-run
  %s run batch.json --server localhost:9090
  %s spool /path/to/spool/dir
  %s resume
  %s status
`, strings.Join([]string{"claude", "codex", "gemini"}, ", "),
		name, name, name, name, name, name)
}
