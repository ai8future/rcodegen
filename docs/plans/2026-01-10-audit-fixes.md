# Audit Fixes Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Fix the 5 critical bugs and code quality issues identified in the January 2026 audit.

**Architecture:** Direct bug fixes to existing code with no new dependencies. Each fix is isolated and can be committed independently.

**Tech Stack:** Go 1.21+, standard library only

---

## Issues to Fix

| # | Issue | Severity | File |
|---|-------|----------|------|
| 1 | `--no-status` flag parsed but never applied | BUG | `pkg/runner/runner.go` |
| 2 | Hardcoded `/Users/cliff/` path | BUG | `pkg/runner/stream.go` |
| 3 | No budget validation for Claude | BUG | `pkg/tools/claude/claude.go` |
| 4 | Dead/duplicate code in reports | CODE SMELL | `pkg/reports/manager.go` |
| 5 | Scanner error not checked | BUG | `pkg/runner/stream.go` |

---

### Task 1: Fix --no-status Flag Bug

**Files:**
- Modify: `pkg/runner/runner.go:595-631`

**Problem:** The `-S/--no-status` flag is parsed into a local `noStatus` variable that is never used to update `cfg.TrackStatus`.

**Step 1: Understand current broken code**

Read lines 620-629 in `runner.go`:
```go
case "NoTrackStatus":
    var noStatus bool
    if fd.Short != "" {
        flag.BoolVar(&noStatus, strings.TrimPrefix(fd.Short, "-"), false, fd.Description)
    }
    if fd.Long != "" {
        flag.BoolVar(&noStatus, strings.TrimPrefix(fd.Long, "--"), false, fd.Description)
    }
    // Note: noStatus handling needs to be done after Parse
```

The `noStatus` variable is scoped inside the switch case and lost after the loop.

**Step 2: Fix by moving variable to function scope**

In `parseArgs()`, add a variable at function scope (around line 448, after other local vars):

```go
var codePath, dirPath string
var showTasks, showHelp bool
var noTrackStatus bool  // ADD THIS LINE
```

**Step 3: Update the flag binding**

Change lines 620-629 to use the new variable:

```go
case "NoTrackStatus":
    if fd.Short != "" {
        flag.BoolVar(&noTrackStatus, strings.TrimPrefix(fd.Short, "-"), false, fd.Description)
    }
    if fd.Long != "" {
        flag.BoolVar(&noTrackStatus, strings.TrimPrefix(fd.Long, "--"), false, fd.Description)
    }
```

**Step 4: Apply the flag after Parse()**

After `flag.Parse()` (around line 479), add:

```go
flag.Parse()

// Handle --no-status flag (must be after Parse)
if noTrackStatus {
    cfg.TrackStatus = false
}
```

**Step 5: Test manually**

```bash
go build ./cmd/rclaude
./rclaude -c rcodegen -S "say hello" 2>&1 | grep -i status
# Should NOT show "Capturing credit status"
./rclaude -c rcodegen -s "say hello" 2>&1 | grep -i status
# Should show "Capturing credit status"
```

**Step 6: Commit**

```bash
git add pkg/runner/runner.go
git commit -m "fix: --no-status flag now properly disables status tracking

The -S/--no-status flag was being parsed but never applied.
Moved noTrackStatus variable to function scope and apply it after flag.Parse()."
```

---

### Task 2: Fix Hardcoded Username in Stream Parser

**Files:**
- Modify: `pkg/runner/stream.go:239-253`

**Problem:** `shortenPath()` has hardcoded `/Users/cliff/` which won't work for other users.

**Step 1: Add os import if not present**

Check imports at top of `stream.go`. Add `"os"` if missing:
```go
import (
    "bufio"
    "encoding/json"
    "fmt"
    "io"
    "os"      // ADD IF MISSING
    "strings"
)
```

**Step 2: Replace hardcoded paths with dynamic detection**

Replace the `shortenPath` function (lines 239-253):

```go
// shortenPath shortens a file path for display
func shortenPath(path string) string {
    home := os.Getenv("HOME")
    if home == "" {
        return path
    }

    // Remove home directory prefix
    if strings.HasPrefix(path, home+"/") {
        return "~" + path[len(home):]
    }

    return path
}
```

**Step 3: Test manually**

```bash
go build ./cmd/rclaude
./rclaude -c rcodegen "read the README" 2>&1 | head -20
# Tool use output should show paths like ~/Desktop/_code/... not full paths
```

**Step 4: Commit**

```bash
git add pkg/runner/stream.go
git commit -m "fix: replace hardcoded username with dynamic HOME detection

shortenPath() now uses \$HOME environment variable instead of
hardcoded /Users/cliff/ path."
```

---

### Task 3: Add Budget Validation for Claude

**Files:**
- Modify: `pkg/tools/claude/claude.go:284-291`

**Problem:** Budget is a string that's never validated. Invalid input like "abc" or "-10" gets passed to Claude CLI.

**Step 1: Add strconv import**

Add to imports at top of `claude.go`:
```go
import (
    "fmt"
    "os/exec"
    "strconv"  // ADD THIS

    "rcodegen/pkg/runner"
    "rcodegen/pkg/settings"
    "rcodegen/pkg/tracking"
)
```

**Step 2: Update ValidateConfig to validate budget**

Replace the `ValidateConfig` function (lines 284-291):

```go
// ValidateConfig validates Claude-specific configuration
func (t *Tool) ValidateConfig(cfg *runner.Config) error {
    // Validate model
    validModels := map[string]bool{"opus": true, "sonnet": true, "haiku": true}
    if !validModels[cfg.Model] {
        return fmt.Errorf("invalid model '%s'. Valid options: opus, sonnet, haiku", cfg.Model)
    }

    // Validate budget is a positive number
    budget, err := strconv.ParseFloat(cfg.MaxBudget, 64)
    if err != nil {
        return fmt.Errorf("invalid budget '%s': must be a number (e.g., 10.00)", cfg.MaxBudget)
    }
    if budget <= 0 {
        return fmt.Errorf("invalid budget '%s': must be greater than 0", cfg.MaxBudget)
    }
    if budget > 1000 {
        return fmt.Errorf("invalid budget '%s': maximum is 1000.00", cfg.MaxBudget)
    }

    return nil
}
```

**Step 3: Test validation**

```bash
go build ./cmd/rclaude
./rclaude -c rcodegen -b abc "test" 2>&1
# Should show: Error: invalid budget 'abc': must be a number
./rclaude -c rcodegen -b -5 "test" 2>&1
# Should show: Error: invalid budget '-5': must be greater than 0
./rclaude -c rcodegen -b 10.00 "test" 2>&1
# Should proceed normally
```

**Step 4: Commit**

```bash
git add pkg/tools/claude/claude.go
git commit -m "fix: add budget validation for Claude

Validates that --budget is a positive number between 0 and 1000.
Previously invalid values like 'abc' or '-10' were passed directly to Claude CLI."
```

---

### Task 4: Fix Dead/Duplicate Code in Reports

**Files:**
- Modify: `pkg/reports/manager.go:32-38, 112-118`
- Modify: `pkg/runner/output.go:22-26`

**Problem 1:** Duplicate glob pattern logic does nothing:
```go
if strings.Contains(pattern, "*") {
    globPattern = filepath.Join(reportDir, pattern+"*.md")
} else {
    globPattern = filepath.Join(reportDir, pattern+"*.md")  // IDENTICAL
}
```

**Problem 2:** Dead code references non-existent shortcuts:
```go
if cfg.TaskShortcut == "all" {        // "all" doesn't exist
    fmt.Printf(" %s(audit → test → fix → refactor)%s", Dim, Reset)
} else if cfg.TaskShortcut == "complete" {  // "complete" doesn't exist
    fmt.Printf(" %s(all_small + all)%s", Dim, Reset)
}
```

**Step 1: Fix ShouldSkipTask glob logic**

In `manager.go`, replace lines 32-38:

```go
// Find reports matching pattern
globPattern := filepath.Join(reportDir, pattern+"*.md")
```

**Step 2: Fix DeleteOldReports glob logic**

In `manager.go`, replace lines 112-118:

```go
// Build glob pattern
globPattern := filepath.Join(reportDir, pattern+"*.md")
```

**Step 3: Fix dead shortcut references in output.go**

Replace lines 20-26 in `output.go`:

```go
// Task
fmt.Printf("  %s%sTask:%s          ", Bold, Green, Reset)
if cfg.TaskShortcut != "" {
    fmt.Printf("%s%s%s", Yellow, cfg.TaskShortcut, Reset)
    if cfg.TaskShortcut == "suite" {
        fmt.Printf(" %s(audit → test → fix → refactor)%s", Dim, Reset)
    }
} else {
```

**Step 4: Build and verify**

```bash
go build ./cmd/rclaude && go build ./cmd/rcodex
```

**Step 5: Commit**

```bash
git add pkg/reports/manager.go pkg/runner/output.go
git commit -m "fix: remove dead and duplicate code

- Removed duplicate if/else branches in glob pattern logic
- Fixed banner to reference 'suite' instead of non-existent 'all'/'complete'"
```

---

### Task 5: Add Scanner Error Checking

**Files:**
- Modify: `pkg/runner/stream.go:278-288`

**Problem:** `ProcessReader` doesn't check for scanner errors after the loop.

**Step 1: Update ProcessReader to check errors**

Replace the `ProcessReader` function (lines 278-288):

```go
// ProcessReader processes a stream of JSON lines from a reader
func (p *StreamParser) ProcessReader(r io.Reader) error {
    scanner := bufio.NewScanner(r)
    // Handle very long lines from stream output
    buf := make([]byte, 0, 64*1024)
    scanner.Buffer(buf, 1024*1024) // 1MB max line size

    for scanner.Scan() {
        p.ProcessLine(scanner.Text())
    }

    return scanner.Err()
}
```

**Step 2: Update caller in runner.go**

Find `executeWithStreamParser` (around line 307-309) and update:

```go
// Parse and format the output
parser := NewStreamParser(os.Stdout)
if err := parser.ProcessReader(stdout); err != nil {
    fmt.Fprintf(os.Stderr, "%sWarning:%s Stream parsing error: %v\n", Yellow, Reset, err)
}
```

**Step 3: Build and verify**

```bash
go build ./cmd/rclaude
```

**Step 4: Commit**

```bash
git add pkg/runner/stream.go pkg/runner/runner.go
git commit -m "fix: check scanner errors in stream parser

ProcessReader now returns scanner.Err() and caller logs any errors.
Previously scanner errors were silently ignored."
```

---

## Final Verification

After all tasks:

```bash
# Build both tools
go build ./cmd/rclaude && go build ./cmd/rcodex

# Test key functionality
./rclaude -h
./rclaude -c rcodegen -S "say hello"  # Should NOT track status
./rclaude -c rcodegen -b abc "test"    # Should show validation error

# Run a real task to verify everything works
./rclaude -c rcodegen "say hello"
```

---

## Summary

| Task | Issue | Status |
|------|-------|--------|
| 1 | --no-status flag bug | [ ] |
| 2 | Hardcoded username | [ ] |
| 3 | Budget validation | [ ] |
| 4 | Dead/duplicate code | [ ] |
| 5 | Scanner error checking | [ ] |
