package openai

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// mustMkdir creates a directory under root and returns its path.
func mustMkdir(t *testing.T, root, name string) string {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	return path
}

func TestSweepOrphanedClones_RemovesMatchingDirsOnly(t *testing.T) {
	tmp := t.TempDir()

	orphanA := mustMkdir(t, tmp, cloneDirPrefix+"abc123-9999")
	orphanB := mustMkdir(t, tmp, cloneDirPrefix+"def456-8888")
	// A populated orphan: the husks left behind hold whole cloned trees, so the
	// sweep has to remove recursively rather than rmdir an empty shell.
	if err := os.WriteFile(filepath.Join(orphanB, "main.go"), []byte("package main"), 0o644); err != nil {
		t.Fatalf("seed orphan: %v", err)
	}

	// Neighbours the sweep must not touch: rserve's own file store, an unrelated
	// temp dir, and a regular file wearing the clone prefix.
	fileStoreDir := mustMkdir(t, tmp, "rserve-files")
	unrelatedDir := mustMkdir(t, tmp, "some-other-tool-cache")
	prefixedFile := filepath.Join(tmp, cloneDirPrefix+"notadir")
	if err := os.WriteFile(prefixedFile, []byte("x"), 0o644); err != nil {
		t.Fatalf("write prefixed file: %v", err)
	}

	removed := SweepOrphanedClones(tmp, quietLogger())
	if removed != 2 {
		t.Errorf("removed = %d, want 2", removed)
	}

	for _, gone := range []string{orphanA, orphanB} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Errorf("%s still present after sweep (err = %v)", gone, err)
		}
	}
	for _, kept := range []string{fileStoreDir, unrelatedDir, prefixedFile} {
		if _, err := os.Stat(kept); err != nil {
			t.Errorf("%s was removed by sweep: %v", kept, err)
		}
	}
}

func TestSweepOrphanedClones_EmptyDirRemovesNothing(t *testing.T) {
	if removed := SweepOrphanedClones(t.TempDir(), quietLogger()); removed != 0 {
		t.Errorf("removed = %d, want 0", removed)
	}
}

// The sweep runs before the listeners, so an unreadable or missing temp dir has
// to be survivable rather than fatal.
func TestSweepOrphanedClones_MissingDirIsTolerated(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	if removed := SweepOrphanedClones(missing, quietLogger()); removed != 0 {
		t.Errorf("removed = %d, want 0", removed)
	}
	// Startup may sweep before a logger exists; a nil one must not panic.
	if removed := SweepOrphanedClones(missing, nil); removed != 0 {
		t.Errorf("removed with nil logger = %d, want 0", removed)
	}
}

func TestSweepOrphanedClones_RemovalFailureIsCountedNotFatal(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions, so removal cannot be made to fail")
	}
	tmp := t.TempDir()

	// A child inside a directory with no write permission cannot be unlinked, so
	// RemoveAll on the parent fails.
	stuck := mustMkdir(t, tmp, cloneDirPrefix+"stuck-0001")
	if err := os.WriteFile(filepath.Join(stuck, "pinned.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("seed stuck orphan: %v", err)
	}
	if err := os.Chmod(stuck, 0o555); err != nil {
		t.Fatalf("chmod stuck orphan: %v", err)
	}
	t.Cleanup(func() { os.Chmod(stuck, 0o755) })

	healthy := mustMkdir(t, tmp, cloneDirPrefix+"healthy-0002")

	// One failure must not abort the sweep: the rest of the orphans still go.
	removed := SweepOrphanedClones(tmp, quietLogger())
	if removed != 1 {
		t.Errorf("removed = %d, want 1 (the healthy orphan only)", removed)
	}
	if _, err := os.Stat(healthy); !os.IsNotExist(err) {
		t.Errorf("healthy orphan survived the sweep (err = %v)", err)
	}
	if _, err := os.Stat(stuck); err != nil {
		t.Errorf("stuck orphan should still exist: %v", err)
	}
}

// The sweep and the cloner must agree on the name, so this drives a real clone
// through the temp dir the sweep scans rather than asserting the prefix twice.
func TestSweepOrphanedClones_MatchesDirsCreatedByCloneWorkDirs(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	if os.TempDir() != tmp {
		t.Skipf("os.TempDir() = %q, not redirected by TMPDIR on this platform", os.TempDir())
	}

	src := seedWorkDir(t)
	clone, err := cloneWorkDirs(context.Background(), "run1", []string{src}, quietLogger())
	if err != nil {
		t.Fatalf("cloneWorkDirs: %v", err)
	}
	if filepath.Dir(clone.root) != tmp {
		t.Fatalf("clone root %q is not under the swept temp dir %q", clone.root, tmp)
	}

	// No cleanup call: this is the state a killed process leaves behind.
	if removed := SweepOrphanedClones(os.TempDir(), quietLogger()); removed != 1 {
		t.Errorf("removed = %d, want 1", removed)
	}
	if _, err := os.Stat(clone.root); !os.IsNotExist(err) {
		t.Errorf("clone root %q survived the sweep (err = %v)", clone.root, err)
	}
}
