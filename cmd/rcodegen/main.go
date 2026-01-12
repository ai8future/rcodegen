package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"rcodegen/pkg/bundle"
	_ "rcodegen/pkg/executor" // Register dispatcher factory via init()
	"rcodegen/pkg/orchestrator"
	"rcodegen/pkg/settings"
)

func main() {
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

	// Load settings
	s, _ := settings.LoadWithFallback()

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
	env, err := orch.Run(b, inputs)

	if *jsonOutput {
		json.NewEncoder(os.Stdout).Encode(env)
	}

	if err != nil || env.Status != "success" {
		os.Exit(1)
	}
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
  rcodegen <bundle> -c <codebase> [inputs...]
  rcodegen bundle <name> -c <codebase> [key=value...]
  rcodegen list

Examples:
  rcodegen compete -c myproject task="audit for security issues"
  rcodegen security-review -c myproject
  rcodegen list`)
}

func expandPath(path string) string {
	if strings.HasPrefix(path, "~/") {
		return os.Getenv("HOME") + path[1:]
	}
	return path
}
