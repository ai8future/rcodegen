package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	rcodegenpkg "rcodegen"
	"rcodegen/pkg/bundle"
	_ "rcodegen/pkg/executor" // Register dispatcher factory via init()
	"rcodegen/pkg/orchestrator"
	"rcodegen/pkg/runner"
	"rcodegen/pkg/settings"

	chassis "github.com/ai8future/chassis-go/v11"
	"github.com/ai8future/chassis-go/v11/logz"
	"github.com/ai8future/chassis-go/v11/registry"
)

func main() {
	chassis.SetAppVersion(rcodegenpkg.AppVersion)
	chassis.RequireMajor(11)
	logger := logz.New("info")
	if err := registry.InitCLI(chassis.Version); err != nil {
		logger.Error("registry init failed", "error", err)
		os.Exit(1)
	}
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "bundle", "run":
		runBundle()
	case "list":
		listBundles()
	case "help", "-h", "--help":
		printUsage()
	default:
		// Treat as bundle name shortcut
		os.Args = append([]string{os.Args[0], "bundle"}, os.Args[1:]...)
		runBundle()
	}
}

func runBundle() {
	// Pre-process args to separate flags from positional args
	// This allows flags like --opus-only to appear anywhere
	// Flags that take values: -c
	flagsWithValues := map[string]bool{"-c": true}

	var flagArgs, positionalArgs []string
	args := os.Args[2:]
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if strings.HasPrefix(arg, "-") {
			flagArgs = append(flagArgs, arg)
			// Check if this flag takes a value and the next arg is that value
			if flagsWithValues[arg] && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
				flagArgs = append(flagArgs, args[i])
			}
		} else {
			positionalArgs = append(positionalArgs, arg)
		}
	}

	fs := flag.NewFlagSet("bundle", flag.ExitOnError)
	codebase := fs.String("c", "", "Codebase path")
	jsonOutput := fs.Bool("j", false, "Output JSON")
	liveMode := fs.Bool("live", true, "Enable animated live display (default: true)")
	staticMode := fs.Bool("static", false, "Use static display instead of animated")
	opusOnly := fs.Bool("opus-only", false, "Force all Claude steps to use Opus model")
	flashOnly := fs.Bool("flash", false, "Force all Gemini steps to use flash preview model")

	fs.Parse(flagArgs)

	if len(positionalArgs) == 0 {
		fmt.Fprintln(os.Stderr, "Error: bundle name required")
		os.Exit(1)
	}
	bundleName := positionalArgs[0]

	// Parse remaining args as inputs (key=value or positional task)
	inputs := make(map[string]string)
	if *codebase != "" {
		inputs["codebase"] = expandPath(*codebase)
	}

	for _, arg := range positionalArgs[1:] {
		if idx := strings.Index(arg, "="); idx != -1 {
			inputs[arg[:idx]] = arg[idx+1:]
		} else {
			// Positional argument without = is treated as "task" input
			inputs["task"] = arg
		}
	}

	// Expand @file references in all input values
	for k, v := range inputs {
		inputs[k] = runner.ExpandFileReferences(v)
	}

	// Load settings
	s, _, err := settings.LoadWithFallback()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Load bundle
	b, err := bundle.Load(bundleName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Run
	orch := orchestrator.New(s)
	// Live mode is default unless --static is set or -j (JSON) output is requested
	if *liveMode && !*staticMode && !*jsonOutput {
		orch.SetLiveMode(true)
	}
	if *opusOnly {
		orch.SetOpusOnly(true)
	}
	if *flashOnly {
		orch.SetFlashOnly(true)
	}
	env, err := orch.Run(b, inputs)

	if *jsonOutput {
		if err := json.NewEncoder(os.Stdout).Encode(env); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to encode JSON output: %v\n", err)
		}
	}

	if err != nil || env.Status != "success" {
		registry.ShutdownCLI(1)
		os.Exit(1)
	}
	registry.ShutdownCLI(0)
}

func listBundles() {
	names, _ := bundle.List()
	fmt.Println("Available bundles:")
	for _, name := range names {
		b, err := bundle.Load(name)
		if err == nil {
			fmt.Printf("  %s - %s\n", name, b.Description)
		} else {
			fmt.Printf("  %s\n", name)
		}
	}
}

func printUsage() {
	fmt.Println(`rcodegen - Multi-tool orchestrator

Usage:
  rcodegen <bundle> [options] [inputs...]
  rcodegen list

Options:
  -c <path>      Codebase path (or run from within project directory)
  --opus-only    Force all Claude steps to use Opus model
  --flash        Force all Gemini steps to use flash preview model
  --static       Use static display instead of animated
  -j             Output JSON

Inputs:
  key=value      Named input (e.g., project_name=myapp)
  "text"         Positional argument becomes 'task' input

Examples:
  rcodegen build-review-audit -c ~/projects/myapp "Add user authentication"
  rcodegen build-review-audit project_name=myapp "Build a CLI tool" --opus-only
  rcodegen security-review -c ./myproject
  rcodegen list`)

	// Show available bundles
	names, err := bundle.List()
	if err == nil && len(names) > 0 {
		fmt.Println("\nAvailable bundles:")
		for _, name := range names {
			b, err := bundle.Load(name)
			if err == nil {
				fmt.Printf("  %-20s %s\n", name, b.Description)
			} else {
				fmt.Printf("  %s\n", name)
			}
		}
	}
}

func expandPath(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return filepath.Join(home, path[2:])
	}
	return path
}
