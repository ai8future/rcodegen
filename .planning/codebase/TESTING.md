# Testing Patterns

**Analysis Date:** 2026-01-14

## Test Framework

**Runner:**
- Go standard `testing` package (no external framework)
- No external test dependencies

**Assertion Library:**
- Go built-in comparisons
- Manual assertions with `t.Errorf()`, `t.Fatalf()`

**Run Commands:**
```bash
make test                              # Run all tests
go test ./pkg/...                      # Run all package tests
go test ./pkg/runner/...               # Run runner tests only
go test -v ./pkg/lock/...              # Verbose output
```

## Test File Organization

**Location:**
- `*_test.go` co-located with source files
- No separate tests/ directory

**Naming:**
- `{package}_test.go` for main tests
- `{feature}_test.go` for focused tests

**Structure:**
```
pkg/
  runner/
    runner.go
    runner_test.go      (68 lines)
    stream.go
    stream_test.go      (164 lines)
  settings/
    settings.go
    settings_test.go    (51 lines)
  tools/claude/
    claude.go
    claude_test.go      (30 lines)
  lock/
    filelock.go
    filelock_test.go    (64 lines)
  workspace/
    workspace.go
    workspace_test.go   (98 lines)
  bundle/
    loader.go
    loader_test.go      (81 lines)
```

**Total Test Files:** 7
**Total Test Lines:** 556 lines

## Test Structure

**Suite Organization:**
```go
package lock

import (
    "os"
    "path/filepath"
    "strings"
    "testing"
)

func TestAcquire_UsesUserDirectory(t *testing.T) {
    // Test that lock files are created in ~/.rcodegen/locks/ not /tmp/
    fl, err := Acquire("test-lock", true)
    if err != nil {
        t.Fatalf("Acquire failed: %v", err)
    }
    defer fl.Release()

    // Verify lock path is in user directory, not /tmp
    if strings.HasPrefix(fl.path, "/tmp/") {
        t.Errorf("lock file in /tmp/ is insecure: %s", fl.path)
    }
}
```

**Patterns:**
- One test function per scenario
- Use `t.Run()` for subtests when grouping related tests
- `defer` for cleanup
- `t.Fatalf()` for fatal errors, `t.Errorf()` for assertions

## Mocking

**Framework:**
- No mocking framework
- Manual test doubles where needed

**Patterns:**
- Test against real files with cleanup
- Use temp directories for file operations
- No external service mocking needed (CLI wrappers)

**What to Mock:**
- File system: Use temp directories, defer cleanup
- Environment: Set and restore env vars

**What NOT to Mock:**
- Internal logic
- Pure functions

## Fixtures and Factories

**Test Data:**
```go
// Inline test data
func TestExpandTilde(t *testing.T) {
    home, _ := os.UserHomeDir()
    cases := []struct {
        input    string
        expected string
    }{
        {"~/test", filepath.Join(home, "test")},
        {"/absolute/path", "/absolute/path"},
    }
    // ...
}
```

**Location:**
- Inline in test files
- No separate fixtures directory

## Coverage

**Requirements:**
- No enforced coverage target
- Focus on critical paths (runner, lock, settings, bundle)

**Configuration:**
- Built-in Go coverage via `go test -cover`

**View Coverage:**
```bash
go test -cover ./pkg/...
go test -coverprofile=coverage.out ./pkg/...
go tool cover -html=coverage.out
```

## Test Types

**Unit Tests:**
- Test single functions in isolation
- Examples: `pkg/runner/runner_test.go`, `pkg/settings/settings_test.go`
- Fast execution (<1s per test)

**Integration Tests:**
- Test package interactions
- Examples: `pkg/lock/filelock_test.go` (file system operations)
- Examples: `pkg/workspace/workspace_test.go` (directory management)

**E2E Tests:**
- Not present
- CLI tools tested manually or via integration

## Common Patterns

**Basic Test:**
```go
func TestRunError(t *testing.T) {
    result := runError(1, fmt.Errorf("test error"))

    if result.ExitCode != 1 {
        t.Errorf("expected exit code 1, got %d", result.ExitCode)
    }
    if result.Error == nil {
        t.Error("expected error to be set")
    }
}
```

**Table-Driven Tests:**
```go
func TestExpandTilde(t *testing.T) {
    cases := []struct {
        input    string
        expected string
    }{
        {"~/test", filepath.Join(home, "test")},
        {"~", home},
    }
    for _, tc := range cases {
        result := expandTilde(tc.input)
        if result != tc.expected {
            t.Errorf("expandTilde(%q) = %q, want %q", tc.input, result, tc.expected)
        }
    }
}
```

**Subtests:**
```go
func TestSettings(t *testing.T) {
    t.Run("FilePermissions", func(t *testing.T) {
        // Test file permission handling
    })
    t.Run("DefaultValues", func(t *testing.T) {
        // Test default value application
    })
}
```

**File System Tests:**
```go
func TestWorkspace_Creation(t *testing.T) {
    ws, err := workspace.New("test-job")
    if err != nil {
        t.Fatalf("New() failed: %v", err)
    }
    defer ws.Cleanup() // Clean up temp directory

    // Verify directory exists
    if _, err := os.Stat(ws.JobDir); os.IsNotExist(err) {
        t.Error("workspace directory not created")
    }
}
```

**Security-Focused Tests:**
```go
func TestAcquire_UsesUserDirectory(t *testing.T) {
    // Verify security requirement: locks not in world-writable /tmp/
    fl, _ := Acquire("test", true)
    defer fl.Release()

    if strings.HasPrefix(fl.path, "/tmp/") {
        t.Errorf("lock file in /tmp/ is insecure: %s", fl.path)
    }
}

func TestSettingsFilePermissions(t *testing.T) {
    // Verify settings file has restricted permissions
    info, _ := os.Stat(settingsPath)
    if info.Mode().Perm() != 0600 {
        t.Errorf("settings file has wrong permissions: %o, want 0600", info.Mode().Perm())
    }
}
```

**Snapshot Testing:**
- Not used
- Prefer explicit assertions

---

*Testing analysis: 2026-01-14*
*Update when test patterns change*
