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

	// We won't remove the dir in tests to avoid conflicts

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
