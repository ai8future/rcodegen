# Architecture Improvements Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Improve rcodegen's portability, testability, and reliability through targeted refactoring and test coverage.

**Architecture:** Replace `os.Getenv("HOME")` with `os.UserHomeDir()` for cross-platform support, refactor `Runner.Run()` to return errors instead of calling `os.Exit()`, and add unit tests for the stream parser, settings loader, and workspace functions.

**Tech Stack:** Go 1.25+, standard library testing

---

## Task 1: Cross-Platform Home Directory

**Files:**
- Modify: `pkg/settings/settings.go:61-63,109`
- Modify: `pkg/runner/stream.go:242`
- Modify: `pkg/workspace/workspace.go` (if needed)
- Modify: `pkg/orchestrator/orchestrator.go:86`
- Test: `pkg/settings/settings_test.go`

**Step 1: Write the failing test for expandTilde**

Create `pkg/settings/settings_test.go`:

```go
package settings

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExpandTilde(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("could not get home dir: %v", err)
	}

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"tilde prefix", "~/foo/bar", filepath.Join(home, "foo/bar")},
		{"just tilde", "~", home},
		{"no tilde", "/absolute/path", "/absolute/path"},
		{"tilde in middle", "/foo/~/bar", "/foo/~/bar"},
		{"empty", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := expandTilde(tt.input)
			if result != tt.expected {
				t.Errorf("expandTilde(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}
```

**Step 2: Run test to verify it fails**

Run: `cd /Users/cliff/Desktop/_code/rcodegen && go test ./pkg/settings/... -v -run TestExpandTilde`
Expected: FAIL (expandTilde still uses os.Getenv which may work, but the test validates behavior)

**Step 3: Update expandTilde to use os.UserHomeDir**

In `pkg/settings/settings.go`, replace the `expandTilde` function:

```go
// expandTilde expands ~ to the user's home directory
func expandTilde(path string) string {
	if path == "" {
		return path
	}
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return path // fallback to original
		}
		return filepath.Join(home, path[2:])
	}
	if path == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return home
	}
	return path
}
```

**Step 4: Update GetConfigDir to use os.UserHomeDir**

In `pkg/settings/settings.go`, modify `GetConfigDir`:

```go
// GetConfigDir returns the path to the config directory (~/.rcodegen)
func GetConfigDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.Getenv("HOME") // fallback for legacy systems
	}
	return filepath.Join(home, ConfigDirName)
}
```

**Step 5: Update GetDefaultSettings to use os.UserHomeDir**

In `pkg/settings/settings.go`, modify `GetDefaultSettings`:

```go
// GetDefaultSettings returns settings with sensible defaults
func GetDefaultSettings() *Settings {
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.Getenv("HOME")
	}
	return &Settings{
		CodeDir: filepath.Join(home, "Desktop/_code"),
		// ... rest unchanged
	}
}
```

**Step 6: Run test to verify it passes**

Run: `cd /Users/cliff/Desktop/_code/rcodegen && go test ./pkg/settings/... -v -run TestExpandTilde`
Expected: PASS

**Step 7: Update stream.go shortenPath to use os.UserHomeDir**

In `pkg/runner/stream.go`, modify `shortenPath`:

```go
// shortenPath shortens a file path for display
func shortenPath(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}

	// Handle exact match (just the home directory)
	if path == home {
		return "~"
	}

	// Remove home directory prefix
	if strings.HasPrefix(path, home+"/") {
		return "~" + path[len(home):]
	}

	return path
}
```

**Step 8: Update orchestrator.go to use os.UserHomeDir**

In `pkg/orchestrator/orchestrator.go`, line 86:

```go
// Create workspace
home, err := os.UserHomeDir()
if err != nil {
	home = os.Getenv("HOME")
}
wsDir := filepath.Join(home, ".rcodegen", "workspace")
```

**Step 9: Run full test suite**

Run: `cd /Users/cliff/Desktop/_code/rcodegen && go build ./... && go test ./...`
Expected: All builds and tests pass

**Step 10: Commit**

```bash
cd /Users/cliff/Desktop/_code/rcodegen
git add pkg/settings/settings.go pkg/settings/settings_test.go pkg/runner/stream.go pkg/orchestrator/orchestrator.go
git commit -m "refactor: use os.UserHomeDir for cross-platform home directory

- Replace os.Getenv(\"HOME\") with os.UserHomeDir() in settings, stream, orchestrator
- Add fallback to os.Getenv for legacy systems
- Add unit tests for expandTilde function"
```

---

## Task 2: Unit Tests for Stream Parser

**Files:**
- Create: `pkg/runner/stream_test.go`
- Read: `pkg/runner/stream.go` (reference)

**Step 1: Write tests for ProcessLine**

Create `pkg/runner/stream_test.go`:

```go
package runner

import (
	"bytes"
	"strings"
	"testing"
)

func TestStreamParser_ProcessLine_Empty(t *testing.T) {
	var buf bytes.Buffer
	p := NewStreamParser(&buf)

	p.ProcessLine("")
	p.ProcessLine("   ")

	if buf.Len() != 0 {
		t.Errorf("expected no output for empty lines, got %q", buf.String())
	}
}

func TestStreamParser_ProcessLine_InvalidJSON(t *testing.T) {
	var buf bytes.Buffer
	p := NewStreamParser(&buf)

	p.ProcessLine("not json at all")

	output := buf.String()
	if !strings.Contains(output, "not json at all") {
		t.Errorf("expected invalid JSON to pass through, got %q", output)
	}
}

func TestStreamParser_ProcessLine_SystemInit(t *testing.T) {
	var buf bytes.Buffer
	p := NewStreamParser(&buf)

	p.ProcessLine(`{"type":"system","subtype":"init"}`)

	output := buf.String()
	if !strings.Contains(output, "initialized") {
		t.Errorf("expected initialization message, got %q", output)
	}

	// Second init should not print again
	buf.Reset()
	p.ProcessLine(`{"type":"system","subtype":"init"}`)
	if buf.Len() != 0 {
		t.Errorf("expected no output for second init, got %q", buf.String())
	}
}

func TestStreamParser_ProcessLine_AssistantText(t *testing.T) {
	var buf bytes.Buffer
	p := NewStreamParser(&buf)

	p.ProcessLine(`{"type":"assistant","message":{"content":[{"type":"text","text":"Hello world"}]}}`)

	output := buf.String()
	if !strings.Contains(output, "Hello world") {
		t.Errorf("expected assistant text in output, got %q", output)
	}
}

func TestStreamParser_ProcessLine_Result(t *testing.T) {
	var buf bytes.Buffer
	p := NewStreamParser(&buf)

	p.ProcessLine(`{"type":"result","usage":{"input_tokens":100,"output_tokens":50},"total_cost_usd":0.0025}`)

	if p.Usage == nil {
		t.Fatal("expected usage to be captured")
	}
	if p.Usage.InputTokens != 100 {
		t.Errorf("expected input_tokens=100, got %d", p.Usage.InputTokens)
	}
	if p.Usage.OutputTokens != 50 {
		t.Errorf("expected output_tokens=50, got %d", p.Usage.OutputTokens)
	}
	if p.TotalCostUSD != 0.0025 {
		t.Errorf("expected total_cost_usd=0.0025, got %f", p.TotalCostUSD)
	}
}

func TestStreamParser_ProcessLine_ResultError(t *testing.T) {
	var buf bytes.Buffer
	p := NewStreamParser(&buf)

	p.ProcessLine(`{"type":"result","is_error":true}`)

	output := buf.String()
	if !strings.Contains(output, "failed") {
		t.Errorf("expected error message, got %q", output)
	}
}

func TestStreamParser_ProcessLine_ToolUse(t *testing.T) {
	var buf bytes.Buffer
	p := NewStreamParser(&buf)

	p.ProcessLine(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/foo/bar.go"}}]}}`)

	output := buf.String()
	if !strings.Contains(output, "Reading") {
		t.Errorf("expected 'Reading file' in output, got %q", output)
	}
	if !strings.Contains(output, "bar.go") {
		t.Errorf("expected file path in output, got %q", output)
	}
}
```

**Step 2: Run tests to verify they pass**

Run: `cd /Users/cliff/Desktop/_code/rcodegen && go test ./pkg/runner/... -v -run TestStreamParser`
Expected: All PASS

**Step 3: Add test for extractToolInfo**

Add to `pkg/runner/stream_test.go`:

```go
func TestExtractToolInfo(t *testing.T) {
	tests := []struct {
		name     string
		toolName string
		input    map[string]interface{}
		expected string
	}{
		{
			name:     "Read file path",
			toolName: "Read",
			input:    map[string]interface{}{"file_path": "/home/user/code/main.go"},
			expected: "/home/user/code/main.go",
		},
		{
			name:     "Bash command short",
			toolName: "Bash",
			input:    map[string]interface{}{"command": "ls -la"},
			expected: "ls -la",
		},
		{
			name:     "Bash command long",
			toolName: "Bash",
			input:    map[string]interface{}{"command": strings.Repeat("x", 100)},
			expected: strings.Repeat("x", 57) + "...",
		},
		{
			name:     "Glob pattern",
			toolName: "Glob",
			input:    map[string]interface{}{"pattern": "**/*.go"},
			expected: "**/*.go",
		},
		{
			name:     "TodoWrite items",
			toolName: "TodoWrite",
			input:    map[string]interface{}{"todos": []interface{}{1, 2, 3}},
			expected: "3 items",
		},
		{
			name:     "Unknown tool",
			toolName: "Unknown",
			input:    map[string]interface{}{"foo": "bar"},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractToolInfo(tt.toolName, tt.input)
			if result != tt.expected {
				t.Errorf("extractToolInfo(%q, %v) = %q, want %q", tt.toolName, tt.input, result, tt.expected)
			}
		})
	}
}
```

**Step 4: Run all stream tests**

Run: `cd /Users/cliff/Desktop/_code/rcodegen && go test ./pkg/runner/... -v`
Expected: All PASS

**Step 5: Commit**

```bash
cd /Users/cliff/Desktop/_code/rcodegen
git add pkg/runner/stream_test.go
git commit -m "test: add unit tests for stream parser

- Test empty/invalid line handling
- Test system init events (including idempotency)
- Test assistant text and tool use rendering
- Test result event with usage capture
- Test error result handling
- Test extractToolInfo for various tools"
```

---

## Task 3: Unit Tests for Workspace

**Files:**
- Create: `pkg/workspace/workspace_test.go`
- Read: `pkg/workspace/workspace.go` (reference)

**Step 1: Write tests for GenerateJobID**

Create `pkg/workspace/workspace_test.go`:

```go
package workspace

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

func TestGenerateJobID_Format(t *testing.T) {
	jobID := GenerateJobID()

	// Format: YYYYMMDD-HHMMSS-{8 hex chars}
	pattern := regexp.MustCompile(`^\d{8}-\d{6}-[a-f0-9]{8}$`)
	if !pattern.MatchString(jobID) {
		t.Errorf("job ID %q does not match expected format YYYYMMDD-HHMMSS-{8 hex}", jobID)
	}
}

func TestGenerateJobID_Unique(t *testing.T) {
	ids := make(map[string]bool)
	for i := 0; i < 100; i++ {
		id := GenerateJobID()
		if ids[id] {
			t.Errorf("duplicate job ID generated: %s", id)
		}
		ids[id] = true
	}
}

func TestNew_CreatesDirectories(t *testing.T) {
	// Use a temp directory
	tmpDir := t.TempDir()

	ws, err := New(tmpDir)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	// Check that job directory was created
	if _, err := os.Stat(ws.JobDir); os.IsNotExist(err) {
		t.Errorf("job directory not created: %s", ws.JobDir)
	}

	// Check subdirectories
	for _, subdir := range []string{"outputs", "errors", "logs"} {
		path := filepath.Join(ws.JobDir, subdir)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			t.Errorf("subdirectory not created: %s", path)
		}
	}
}

func TestWorkspace_OutputPath(t *testing.T) {
	ws := &Workspace{
		JobDir: "/tmp/test-job",
	}

	path := ws.OutputPath("step1")
	expected := "/tmp/test-job/outputs/step1.json"
	if path != expected {
		t.Errorf("OutputPath() = %q, want %q", path, expected)
	}
}

func TestWorkspace_WriteOutput(t *testing.T) {
	tmpDir := t.TempDir()
	ws, err := New(tmpDir)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	data := map[string]interface{}{
		"key":   "value",
		"count": 42,
	}

	path, err := ws.WriteOutput("test-step", data)
	if err != nil {
		t.Fatalf("WriteOutput() error: %v", err)
	}

	// Verify file exists
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Errorf("output file not created: %s", path)
	}

	// Verify content
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("could not read output file: %v", err)
	}

	// Should contain our data
	if !regexp.MustCompile(`"key":\s*"value"`).Match(content) {
		t.Errorf("output file missing expected content: %s", content)
	}
}
```

**Step 2: Run tests**

Run: `cd /Users/cliff/Desktop/_code/rcodegen && go test ./pkg/workspace/... -v`
Expected: All PASS

**Step 3: Commit**

```bash
cd /Users/cliff/Desktop/_code/rcodegen
git add pkg/workspace/workspace_test.go
git commit -m "test: add unit tests for workspace package

- Test job ID format (YYYYMMDD-HHMMSS-{hex})
- Test job ID uniqueness
- Test directory creation on New()
- Test OutputPath construction
- Test WriteOutput file creation and content"
```

---

## Task 4: Refactor Runner.Run to Return Errors

**Files:**
- Modify: `pkg/runner/runner.go`
- Create: `pkg/runner/runner_test.go`
- Modify: `cmd/rclaude/main.go` (or equivalent entry point)
- Modify: `cmd/rcodex/main.go` (or equivalent entry point)
- Modify: `cmd/rgemini/main.go` (or equivalent entry point)

**Step 1: Define RunResult type and modify Runner.Run signature**

In `pkg/runner/runner.go`, add after the Runner struct:

```go
// RunResult holds the result of a Run() invocation
type RunResult struct {
	ExitCode     int
	TokenUsage   *TokenUsage
	TotalCostUSD float64
	Error        error
}
```

**Step 2: Create a helper for fatal errors**

In `pkg/runner/runner.go`, add:

```go
// runError creates a RunResult for an error condition
func runError(code int, err error) *RunResult {
	return &RunResult{ExitCode: code, Error: err}
}
```

**Step 3: Modify Run() to return RunResult instead of calling os.Exit**

The key changes (partial - full implementation spans entire function):

Change signature from:
```go
func (r *Runner) Run() {
```

To:
```go
func (r *Runner) Run() *RunResult {
```

Replace each `os.Exit(1)` with `return runError(1, fmt.Errorf("..."))`.

For example, line 48-50:
```go
// OLD:
if !ok {
    fmt.Fprintln(os.Stderr, "Setup cancelled or failed. Exiting.")
    os.Exit(1)
}

// NEW:
if !ok {
    return runError(1, fmt.Errorf("setup cancelled or failed"))
}
```

And line 196:
```go
// OLD:
os.Exit(overallExit)

// NEW:
return &RunResult{
    ExitCode:     overallExit,
    TokenUsage:   cfg.TokenUsage,
    TotalCostUSD: cfg.TotalCostUSD,
}
```

**Step 4: Create a wrapper for backwards compatibility**

In `pkg/runner/runner.go`, add:

```go
// RunAndExit runs the task and exits with the appropriate code
// This is the entry point for CLI binaries
func (r *Runner) RunAndExit() {
	result := r.Run()
	if result.Error != nil {
		fmt.Fprintln(os.Stderr, result.Error)
	}
	os.Exit(result.ExitCode)
}
```

**Step 5: Update main.go entry points**

In each `cmd/*/main.go`, change:
```go
// OLD:
runner.NewRunner(tool).Run()

// NEW:
runner.NewRunner(tool).RunAndExit()
```

**Step 6: Write test for Run() error handling**

Create `pkg/runner/runner_test.go`:

```go
package runner

import (
	"testing"
)

func TestRunResult_ErrorCondition(t *testing.T) {
	result := runError(1, fmt.Errorf("test error"))

	if result.ExitCode != 1 {
		t.Errorf("expected exit code 1, got %d", result.ExitCode)
	}
	if result.Error == nil {
		t.Error("expected error to be set")
	}
	if result.Error.Error() != "test error" {
		t.Errorf("expected error message 'test error', got %q", result.Error.Error())
	}
}
```

**Step 7: Run tests and build**

Run: `cd /Users/cliff/Desktop/_code/rcodegen && go build ./... && go test ./pkg/runner/... -v`
Expected: All builds and tests pass

**Step 8: Commit**

```bash
cd /Users/cliff/Desktop/_code/rcodegen
git add pkg/runner/runner.go pkg/runner/runner_test.go cmd/
git commit -m "refactor: Runner.Run returns errors instead of calling os.Exit

- Add RunResult type to capture exit code, usage, cost, and error
- Refactor Run() to return *RunResult instead of void
- Add RunAndExit() wrapper for CLI backwards compatibility
- Enables unit testing of Runner without process termination
- All os.Exit calls moved to main() entry points"
```

---

## Task 5: Add Dry Run Mode

**Files:**
- Modify: `pkg/runner/config.go`
- Modify: `pkg/runner/runner.go`
- Test: `pkg/runner/runner_test.go`

**Step 1: Add DryRun field to Config**

In `pkg/runner/config.go`, add to Config struct:

```go
type Config struct {
	// ... existing fields ...

	// Execution control
	DryRun bool // If true, show what would be executed without running
}
```

**Step 2: Add -n flag to parseArgs**

In `pkg/runner/runner.go`, in the `parseArgs` function, add with other flag definitions:

```go
flag.BoolVar(&cfg.DryRun, "n", false, "Dry run - show command without executing")
flag.BoolVar(&cfg.DryRun, "dry-run", false, "Dry run - show command without executing")
```

**Step 3: Add dry run handling in runSingleTask**

In `pkg/runner/runner.go`, modify `runSingleTask`:

```go
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
```

**Step 4: Add help text for dry run**

In `pkg/runner/runner.go`, in `printUsage`, add to Execution Options:

```go
fmt.Printf("  %s-n%s, %s--dry-run%s        Show command without executing\n", Green, Reset, Green, Reset)
```

**Step 5: Run build to verify**

Run: `cd /Users/cliff/Desktop/_code/rcodegen && go build ./...`
Expected: Build succeeds

**Step 6: Commit**

```bash
cd /Users/cliff/Desktop/_code/rcodegen
git add pkg/runner/config.go pkg/runner/runner.go
git commit -m "feat: add dry run mode (-n, --dry-run)

- Add DryRun field to Config
- Add -n and --dry-run flags
- In dry run mode, display command details without execution
- Useful for validating task templates and variable substitution"
```

---

## Task 6: Final Verification

**Files:**
- All modified files

**Step 1: Run full test suite**

Run: `cd /Users/cliff/Desktop/_code/rcodegen && go test ./... -v`
Expected: All tests pass

**Step 2: Build all binaries**

Run: `cd /Users/cliff/Desktop/_code/rcodegen && go build ./...`
Expected: All builds succeed

**Step 3: Manual smoke test**

Run: `cd /Users/cliff/Desktop/_code/rcodegen && ./rcodex -n -c rcodegen "test task"`
Expected: Shows dry run output without executing

**Step 4: Final commit (if any fixups needed)**

```bash
cd /Users/cliff/Desktop/_code/rcodegen
git status
# If clean, no action needed
# If changes, commit with appropriate message
```

---

## Verification Summary

After completing all tasks:

1. **Cross-platform**: `os.UserHomeDir()` used everywhere, with fallback
2. **Testable**: `Runner.Run()` returns errors, no `os.Exit` deep in code
3. **Tested**: Stream parser, settings, and workspace have unit tests
4. **DX improved**: Dry run mode available with `-n`

Run the full verification:
```bash
cd /Users/cliff/Desktop/_code/rcodegen
go test ./... -v -cover
go build ./...
```

Expected output: All tests pass, coverage reported, all binaries build.
