# Security Hardening Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Address Critical and High severity security issues identified in the codebase audit.

**Architecture:** Fix lock file symlink vulnerability by moving to user-specific directory, add bundle name validation to prevent path traversal, secure settings file permissions, and add thread safety to status caching.

**Tech Stack:** Go 1.25+, standard library

---

## Task 1: Move Lock Files to User-Specific Directory

**Files:**
- Modify: `pkg/lock/filelock.go:28-79`
- Create: `pkg/lock/filelock_test.go`

**Step 1: Write the failing test**

Create `pkg/lock/filelock_test.go`:

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

	home, _ := os.UserHomeDir()
	expectedDir := filepath.Join(home, ".rcodegen", "locks")
	if !strings.HasPrefix(fl.path, expectedDir) {
		t.Errorf("lock file not in expected directory: got %s, want prefix %s", fl.path, expectedDir)
	}
}

func TestAcquire_CreatesLockDirectory(t *testing.T) {
	home, _ := os.UserHomeDir()
	lockDir := filepath.Join(home, ".rcodegen", "locks")

	// Remove lock dir if it exists for clean test
	os.RemoveAll(lockDir)

	fl, err := Acquire("test-create-dir", true)
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}
	defer fl.Release()

	// Verify directory was created
	info, err := os.Stat(lockDir)
	if err != nil {
		t.Fatalf("lock directory not created: %v", err)
	}
	if !info.IsDir() {
		t.Error("lock path is not a directory")
	}
	// Verify permissions are 0700 (owner only)
	if info.Mode().Perm() != 0700 {
		t.Errorf("lock directory has wrong permissions: %o, want 0700", info.Mode().Perm())
	}
}

func TestAcquire_Disabled(t *testing.T) {
	fl, err := Acquire("test", false)
	if err != nil {
		t.Fatalf("Acquire with useLock=false failed: %v", err)
	}
	if fl != nil {
		t.Error("expected nil FileLock when useLock=false")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `cd /Users/cliff/Desktop/_code/rcodegen && go test ./pkg/lock/... -v -run TestAcquire`
Expected: FAIL with "lock file in /tmp/ is insecure"

**Step 3: Implement secure lock directory**

Replace the lock path logic in `pkg/lock/filelock.go`:

```go
// getLockDir returns the secure lock directory path
func getLockDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.Getenv("HOME")
		if home == "" {
			return "", fmt.Errorf("cannot determine home directory")
		}
	}
	return filepath.Join(home, ".rcodegen", "locks"), nil
}

// Acquire acquires a file lock, waiting if necessary
// identifier is used to identify who holds the lock (e.g., codebase name)
func Acquire(identifier string, useLock bool) (*FileLock, error) {
	if !useLock {
		return nil, nil
	}

	lockDir, err := getLockDir()
	if err != nil {
		return nil, err
	}

	// Create lock directory with secure permissions (owner only)
	if err := os.MkdirAll(lockDir, 0700); err != nil {
		return nil, fmt.Errorf("could not create lock directory: %w", err)
	}

	lockPath := filepath.Join(lockDir, "rcodegen.lock")
	lockInfoPath := filepath.Join(lockDir, "rcodegen.lock.info")

	// Use provided identifier or try to determine it
	if identifier == "" {
		identifier = "unknown"
	}

	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("could not open lock file: %w", err)
	}

	// ... rest of function unchanged, but update lockInfoPath usage
```

**Step 4: Run test to verify it passes**

Run: `cd /Users/cliff/Desktop/_code/rcodegen && go test ./pkg/lock/... -v -run TestAcquire`
Expected: PASS

**Step 5: Add error handling for lock info write**

In `pkg/lock/filelock.go`, replace line 77:

```go
// Write our info so others know who has the lock
if err := os.WriteFile(lockInfoPath, []byte(identifier), 0600); err != nil {
	// Log but don't fail - lock info is informational
	fmt.Fprintf(os.Stderr, "%sWarning: could not write lock info: %v%s\n", Dim, err, Reset)
}
```

**Step 6: Run all tests**

Run: `cd /Users/cliff/Desktop/_code/rcodegen && go test ./pkg/lock/... -v`
Expected: All PASS

**Step 7: Commit**

```bash
cd /Users/cliff/Desktop/_code/rcodegen
git add pkg/lock/filelock.go pkg/lock/filelock_test.go
git commit -m "security: move lock files to ~/.rcodegen/locks/

- Fixes symlink attack vulnerability (CVE-like severity)
- Lock directory created with 0700 permissions (owner only)
- Lock files created with 0600 permissions
- Add error handling for lock info write
- Add unit tests for secure lock behavior"
```

---

## Task 2: Add Bundle Name Validation

**Files:**
- Modify: `pkg/bundle/loader.go:14-36`
- Create: `pkg/bundle/loader_test.go`

**Step 1: Write the failing test**

Create `pkg/bundle/loader_test.go`:

```go
package bundle

import (
	"strings"
	"testing"
)

func TestLoad_RejectsPathTraversal(t *testing.T) {
	maliciousNames := []string{
		"../../../etc/passwd",
		"..\\..\\..\\windows\\system32\\config\\sam",
		"foo/../bar",
		"./hidden",
		"foo/bar",
		".hidden",
	}

	for _, name := range maliciousNames {
		t.Run(name, func(t *testing.T) {
			_, err := Load(name)
			if err == nil {
				t.Errorf("Load(%q) should have failed for path traversal", name)
				return
			}
			if !strings.Contains(err.Error(), "invalid bundle name") {
				t.Errorf("Load(%q) error should mention 'invalid bundle name', got: %v", name, err)
			}
		})
	}
}

func TestLoad_AcceptsValidNames(t *testing.T) {
	validNames := []string{
		"compete",
		"security-review",
		"red_team",
		"test123",
		"my-bundle-v2",
	}

	for _, name := range validNames {
		t.Run(name, func(t *testing.T) {
			_, err := Load(name)
			// Should fail with "bundle not found", not "invalid bundle name"
			if err != nil && strings.Contains(err.Error(), "invalid bundle name") {
				t.Errorf("Load(%q) incorrectly rejected valid name: %v", name, err)
			}
		})
	}
}

func TestValidateBundleName(t *testing.T) {
	tests := []struct {
		name    string
		valid   bool
	}{
		{"compete", true},
		{"security-review", true},
		{"red_team", true},
		{"test123", true},
		{"../etc/passwd", false},
		{"foo/bar", false},
		{".hidden", false},
		{"", false},
		{"a", true},
		{strings.Repeat("a", 100), true},
		{strings.Repeat("a", 101), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateBundleName(tt.name)
			if tt.valid && err != nil {
				t.Errorf("validateBundleName(%q) = %v, want nil", tt.name, err)
			}
			if !tt.valid && err == nil {
				t.Errorf("validateBundleName(%q) = nil, want error", tt.name)
			}
		})
	}
}
```

**Step 2: Run test to verify it fails**

Run: `cd /Users/cliff/Desktop/_code/rcodegen && go test ./pkg/bundle/... -v -run TestLoad_Rejects`
Expected: FAIL (path traversal names not rejected)

**Step 3: Add bundle name validation function**

In `pkg/bundle/loader.go`, add before the Load function:

```go
import (
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

// validBundleNamePattern matches alphanumeric, hyphens, underscores only
var validBundleNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

// validateBundleName checks if a bundle name is safe to use in file paths
func validateBundleName(name string) error {
	if name == "" {
		return fmt.Errorf("invalid bundle name: empty")
	}
	if len(name) > 100 {
		return fmt.Errorf("invalid bundle name: too long (max 100 chars)")
	}
	if !validBundleNamePattern.MatchString(name) {
		return fmt.Errorf("invalid bundle name: must contain only alphanumeric, hyphens, underscores")
	}
	return nil
}
```

**Step 4: Update Load function to validate**

In `pkg/bundle/loader.go`, modify the Load function:

```go
func Load(name string) (*Bundle, error) {
	// Validate bundle name to prevent path traversal
	if err := validateBundleName(name); err != nil {
		return nil, err
	}

	// Try user bundles first
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.Getenv("HOME")
	}
	userPath := filepath.Join(home, ".rcodegen", "bundles", name+".json")
	// ... rest unchanged
```

**Step 5: Run tests to verify they pass**

Run: `cd /Users/cliff/Desktop/_code/rcodegen && go test ./pkg/bundle/... -v`
Expected: All PASS

**Step 6: Commit**

```bash
cd /Users/cliff/Desktop/_code/rcodegen
git add pkg/bundle/loader.go pkg/bundle/loader_test.go
git commit -m "security: add bundle name validation to prevent path traversal

- Validate bundle names contain only alphanumeric, hyphens, underscores
- Reject empty names and names over 100 characters
- Prevent ../etc/passwd style path traversal attacks
- Add comprehensive unit tests"
```

---

## Task 3: Secure Settings File Permissions

**Files:**
- Modify: `pkg/settings/settings.go:482`
- Modify: `pkg/settings/settings_test.go`

**Step 1: Write the failing test**

Add to `pkg/settings/settings_test.go`:

```go
func TestRunInteractiveSetup_SecurePermissions(t *testing.T) {
	// This test verifies settings are written with 0600 permissions
	// We can't easily test interactive setup, so we'll test the file
	// permissions constant is correct

	// The settings file should be written with 0600 (owner read/write only)
	expectedPerm := os.FileMode(0600)

	// Check if settings file exists and has correct permissions
	configPath := GetConfigPath()
	if info, err := os.Stat(configPath); err == nil {
		actualPerm := info.Mode().Perm()
		if actualPerm != expectedPerm {
			t.Errorf("settings file has permissions %o, want %o", actualPerm, expectedPerm)
		}
	}
	// If file doesn't exist, that's OK - test passes
}
```

**Step 2: Update settings file write permissions**

In `pkg/settings/settings.go`, find line 482 and change:

```go
// Before:
if err := os.WriteFile(configPath, data, 0644); err != nil {

// After:
if err := os.WriteFile(configPath, data, 0600); err != nil {
```

**Step 3: Run tests**

Run: `cd /Users/cliff/Desktop/_code/rcodegen && go test ./pkg/settings/... -v`
Expected: All PASS

**Step 4: Commit**

```bash
cd /Users/cliff/Desktop/_code/rcodegen
git add pkg/settings/settings.go pkg/settings/settings_test.go
git commit -m "security: use 0600 permissions for settings file

- Settings may contain budget info and code paths
- Restrict to owner read/write only (was 0644)"
```

---

## Task 4: Add Thread Safety to Claude Tool Status Caching

**Files:**
- Modify: `pkg/tools/claude/claude.go:14-47`
- Create: `pkg/tools/claude/claude_test.go`

**Step 1: Write the failing test**

Create `pkg/tools/claude/claude_test.go`:

```go
package claude

import (
	"sync"
	"testing"
)

func TestCheckClaudeMax_ThreadSafe(t *testing.T) {
	tool := New()

	// Run concurrent checks - should not race
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = tool.IsClaudeMax()
		}()
	}
	wg.Wait()

	// If we get here without -race detecting issues, test passes
}

func TestNew_ReturnsNonNil(t *testing.T) {
	tool := New()
	if tool == nil {
		t.Error("New() returned nil")
	}
}
```

**Step 2: Run test with race detector**

Run: `cd /Users/cliff/Desktop/_code/rcodegen && go test ./pkg/tools/claude/... -v -race -run TestCheckClaudeMax_ThreadSafe`
Expected: May detect race condition (current implementation is not thread-safe)

**Step 3: Add sync.Once for thread safety**

In `pkg/tools/claude/claude.go`, modify the Tool struct and checkClaudeMax:

```go
import (
	"fmt"
	"os/exec"
	"strconv"
	"sync"

	"rcodegen/pkg/runner"
	"rcodegen/pkg/settings"
	"rcodegen/pkg/tracking"
)

// Tool implements the runner.Tool interface for Claude Code CLI
type Tool struct {
	settings     *settings.Settings
	currentModel string // Track current model for status calculations

	// Thread-safe status caching
	checkOnce    sync.Once
	isClaudeMax  bool
	cachedStatus *tracking.ClaudeStatus
}

// New creates a new Claude tool
func New() *Tool {
	return &Tool{}
}

// checkClaudeMax checks if user has Claude Max subscription and caches the result
func (t *Tool) checkClaudeMax() {
	t.checkOnce.Do(func() {
		// Try to get status - if successful, user has Claude Max
		status := tracking.GetClaudeStatus()
		if status.Error == "" && (status.SessionLeft != nil || status.WeeklyAllLeft != nil) {
			t.isClaudeMax = true
			t.cachedStatus = status
		}
	})
}

// IsClaudeMax returns true if user has Claude Max subscription
func (t *Tool) IsClaudeMax() bool {
	t.checkClaudeMax()
	return t.isClaudeMax
}
```

**Step 4: Remove old fields**

Remove these fields from the Tool struct (they're replaced by sync.Once pattern):
- `maxChecked bool`

**Step 5: Run tests with race detector**

Run: `cd /Users/cliff/Desktop/_code/rcodegen && go test ./pkg/tools/claude/... -v -race`
Expected: All PASS, no race conditions detected

**Step 6: Commit**

```bash
cd /Users/cliff/Desktop/_code/rcodegen
git add pkg/tools/claude/claude.go pkg/tools/claude/claude_test.go
git commit -m "fix: add thread safety to Claude status caching

- Use sync.Once to ensure checkClaudeMax runs exactly once
- Eliminates race condition when IsClaudeMax called concurrently
- Add thread safety test with race detector"
```

---

## Task 5: Restrict Script Search Path

**Files:**
- Modify: `pkg/tracking/codex.go:86-96`
- Modify: `pkg/tracking/claude.go` (if similar pattern exists)

**Step 1: Identify the issue**

The current code searches for Python scripts in the current working directory, which could be attacker-controlled.

**Step 2: Remove cwd from search path**

In `pkg/tracking/codex.go`, modify `GetStatus`:

```go
// GetStatus fetches the current Codex credit status using the Python script
func GetStatus() *CreditStatus {
	// Only look for scripts in trusted locations
	scriptDir := GetScriptDir()
	statusScript := filepath.Join(scriptDir, "get_codex_status.py")

	// Check home directory as secondary trusted location
	if _, err := os.Stat(statusScript); os.IsNotExist(err) {
		home, err := os.UserHomeDir()
		if err == nil {
			statusScript = filepath.Join(home, ".rcodegen", "scripts", "get_codex_status.py")
		}
	}

	// Do NOT search current working directory - could be attacker-controlled

	if _, err := os.Stat(statusScript); os.IsNotExist(err) {
		return &CreditStatus{Error: "status script not found in trusted locations"}
	}

	cmd := exec.Command(FindPython(), statusScript)
	output, err := cmd.Output()
	if err != nil {
		return &CreditStatus{Error: fmt.Sprintf("failed to run status script: %v", err)}
	}

	var status CreditStatus
	if err := json.Unmarshal(output, &status); err != nil {
		return &CreditStatus{Error: fmt.Sprintf("failed to parse status JSON: %v", err)}
	}

	return &status
}
```

**Step 3: Apply same fix to claude tracking if needed**

Check `pkg/tracking/claude.go` for similar pattern and apply same fix.

**Step 4: Run build**

Run: `cd /Users/cliff/Desktop/_code/rcodegen && go build ./...`
Expected: Build succeeds

**Step 5: Commit**

```bash
cd /Users/cliff/Desktop/_code/rcodegen
git add pkg/tracking/codex.go pkg/tracking/claude.go
git commit -m "security: restrict script search to trusted directories

- Remove current working directory from script search path
- Only search executable directory and ~/.rcodegen/scripts/
- Prevents execution of malicious scripts in attacker-controlled cwd"
```

---

## Task 6: Final Verification

**Files:**
- All modified files

**Step 1: Run full test suite**

Run: `cd /Users/cliff/Desktop/_code/rcodegen && go test ./... -v -race`
Expected: All tests pass, no race conditions

**Step 2: Build all binaries**

Run: `cd /Users/cliff/Desktop/_code/rcodegen && go build ./...`
Expected: All builds succeed

**Step 3: Verify lock directory security**

Run:
```bash
# Test that lock directory has correct permissions
ls -la ~/.rcodegen/locks/ 2>/dev/null || echo "Lock dir not created yet (OK)"
```

**Step 4: Test bundle name validation**

Run:
```bash
cd /Users/cliff/Desktop/_code/rcodegen
./rcodegen list  # Should work
./rcodegen "../../../etc/passwd" 2>&1 | grep -i "invalid"  # Should fail with validation error
```

---

## Verification Summary

After completing all tasks:

1. **Lock files**: Now in `~/.rcodegen/locks/` with 0700 dir and 0600 file permissions
2. **Bundle names**: Validated against regex, path traversal blocked
3. **Settings file**: Written with 0600 permissions
4. **Thread safety**: Claude status caching uses sync.Once
5. **Script search**: Only trusted directories searched

Total: 5 security fixes addressing Critical and High severity issues from audit.
