package openai

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// seedWorkDir builds a source tree with the shapes that matter: a plain file, a
// dotfile, and a dot-directory holding agent state.
func seedWorkDir(t *testing.T) string {
	t.Helper()
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "main.go"), []byte("package main"), 0o644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, ".env"), []byte("SECRET=1"), 0o600); err != nil {
		t.Fatalf("write .env: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(src, ".omc", "state"), 0o755); err != nil {
		t.Fatalf("mkdir .omc/state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, ".omc", "state", "run.json"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("write run.json: %v", err)
	}
	return src
}

func TestCloneWorkDirs_CopiesDotfilesAndIsolatesWrites(t *testing.T) {
	src := seedWorkDir(t)

	clone, err := cloneWorkDirs(context.Background(), "run1", []string{src}, quietLogger())
	if err != nil {
		t.Fatalf("cloneWorkDirs: %v", err)
	}
	defer clone.cleanup(quietLogger())

	if clone.count() != 1 {
		t.Fatalf("count = %d, want 1", clone.count())
	}
	dst := clone.dirs[0]
	if dst == src {
		t.Fatal("clone path equals source path")
	}
	if got, want := filepath.Base(dst), filepath.Base(src); got != want {
		t.Errorf("clone basename = %q, want %q", got, want)
	}
	if !strings.Contains(filepath.Base(clone.root), "rserve-clone-run1-") {
		t.Errorf("scratch root %q missing rserve-clone-run1- prefix", clone.root)
	}

	for _, rel := range []string{"main.go", ".env", filepath.Join(".omc", "state", "run.json")} {
		if _, err := os.Stat(filepath.Join(dst, rel)); err != nil {
			t.Errorf("cloned %s missing: %v", rel, err)
		}
	}
	if body, err := os.ReadFile(filepath.Join(dst, ".env")); err != nil || string(body) != "SECRET=1" {
		t.Errorf("cloned .env = %q, %v; want SECRET=1", body, err)
	}

	// Agent state written into the clone must not reach the source tree.
	if err := os.WriteFile(filepath.Join(dst, ".omc", "scratch.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write into clone: %v", err)
	}
	if _, err := os.Stat(filepath.Join(src, ".omc", "scratch.txt")); !os.IsNotExist(err) {
		t.Errorf("write leaked into source tree (err = %v)", err)
	}
}

func TestCloneWorkDirs_CopyOnWriteWithFallback(t *testing.T) {
	src := seedWorkDir(t)

	// Default path on darwin: APFS clonefile.
	if cloneUseCOW {
		method, err := copyDir(context.Background(), src, filepath.Join(t.TempDir(), "cow"))
		if err != nil {
			t.Fatalf("copyDir (cow): %v", err)
		}
		if method != "cow" {
			t.Errorf("method = %q, want cow", method)
		}
	}

	// Fallback path: same result, reported as a plain copy.
	restore := cloneUseCOW
	cloneUseCOW = false
	t.Cleanup(func() { cloneUseCOW = restore })

	dst := filepath.Join(t.TempDir(), "plain")
	method, err := copyDir(context.Background(), src, dst)
	if err != nil {
		t.Fatalf("copyDir (fallback): %v", err)
	}
	if method != "copy" {
		t.Errorf("method = %q, want copy", method)
	}
	if _, err := os.Stat(filepath.Join(dst, ".env")); err != nil {
		t.Errorf("fallback copy dropped dotfile: %v", err)
	}
}

func TestCloneWorkDirs_CleanupRemovesScratchRoot(t *testing.T) {
	src := seedWorkDir(t)

	clone, err := cloneWorkDirs(context.Background(), "run2", []string{src}, quietLogger())
	if err != nil {
		t.Fatalf("cloneWorkDirs: %v", err)
	}
	root := clone.root

	clone.cleanup(quietLogger())
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Errorf("scratch root still present after cleanup (err = %v)", err)
	}
	if _, err := os.Stat(src); err != nil {
		t.Errorf("cleanup damaged the source tree: %v", err)
	}

	// Cleanup must stay safe when nothing was cloned.
	var nilClone *workDirClone
	nilClone.cleanup(quietLogger())
	if nilClone.count() != 0 {
		t.Errorf("nil clone count = %d, want 0", nilClone.count())
	}
}

func TestCloneWorkDirs_ConcurrentClonesOfSameSourceDoNotCollide(t *testing.T) {
	src := seedWorkDir(t)

	const runs = 4
	var wg sync.WaitGroup
	clones := make([]*workDirClone, runs)
	errs := make([]error, runs)

	for i := 0; i < runs; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			clones[i], errs[i] = cloneWorkDirs(context.Background(), fmt.Sprintf("run%d", i), []string{src}, quietLogger())
		}(i)
	}
	wg.Wait()

	seen := map[string]bool{}
	for i, clone := range clones {
		if errs[i] != nil {
			t.Fatalf("clone %d: %v", i, errs[i])
		}
		defer clone.cleanup(quietLogger())
		if seen[clone.dirs[0]] {
			t.Fatalf("clone %d reused path %s", i, clone.dirs[0])
		}
		seen[clone.dirs[0]] = true
		marker := filepath.Join(clone.dirs[0], fmt.Sprintf("worker-%d.txt", i))
		if err := os.WriteFile(marker, []byte("x"), 0o644); err != nil {
			t.Fatalf("write marker %d: %v", i, err)
		}
	}

	// Each worker sees only its own marker, and the source stays clean.
	for i, clone := range clones {
		for j := range clones {
			_, err := os.Stat(filepath.Join(clone.dirs[0], fmt.Sprintf("worker-%d.txt", j)))
			if i == j && err != nil {
				t.Errorf("clone %d lost its own marker: %v", i, err)
			}
			if i != j && !os.IsNotExist(err) {
				t.Errorf("clone %d sees worker-%d marker (err = %v)", i, j, err)
			}
		}
		if _, err := os.Stat(filepath.Join(src, fmt.Sprintf("worker-%d.txt", i))); !os.IsNotExist(err) {
			t.Errorf("marker %d leaked into source (err = %v)", i, err)
		}
	}
}

func TestCloneWorkDirs_SharedBasenamesGetDistinctPaths(t *testing.T) {
	parentA, parentB := t.TempDir(), t.TempDir()
	srcA, srcB := filepath.Join(parentA, "proj"), filepath.Join(parentB, "proj")
	for _, dir := range []string{srcA, srcB} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(srcA, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcB, "b.txt"), []byte("b"), 0o644); err != nil {
		t.Fatalf("write b.txt: %v", err)
	}

	clone, err := cloneWorkDirs(context.Background(), "run3", []string{srcA, srcB}, quietLogger())
	if err != nil {
		t.Fatalf("cloneWorkDirs: %v", err)
	}
	defer clone.cleanup(quietLogger())

	if clone.count() != 2 {
		t.Fatalf("count = %d, want 2", clone.count())
	}
	if clone.dirs[0] == clone.dirs[1] {
		t.Fatalf("both sources cloned to %s", clone.dirs[0])
	}
	if _, err := os.Stat(filepath.Join(clone.dirs[0], "a.txt")); err != nil {
		t.Errorf("first clone missing a.txt: %v", err)
	}
	if _, err := os.Stat(filepath.Join(clone.dirs[1], "b.txt")); err != nil {
		t.Errorf("second clone missing b.txt: %v", err)
	}
}

func TestCloneWorkDirs_NoSourcesIsNoOp(t *testing.T) {
	clone, err := cloneWorkDirs(context.Background(), "run4", nil, quietLogger())
	if err != nil {
		t.Fatalf("cloneWorkDirs(nil) = %v, want no error", err)
	}
	if clone != nil {
		t.Fatalf("clone = %+v, want nil", clone)
	}
}

func TestCloneWorkDirs_RejectsBadSources(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "notadir")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	cases := map[string]string{
		"missing":     filepath.Join(dir, "does-not-exist"),
		"regularFile": file,
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			clone, err := cloneWorkDirs(context.Background(), "run5", []string{src}, quietLogger())
			if !errors.Is(err, errInvalidWorkDir) {
				t.Fatalf("err = %v, want errInvalidWorkDir", err)
			}
			if clone != nil {
				clone.cleanup(quietLogger())
				t.Fatal("clone should be nil when validation fails")
			}
			if !strings.Contains(err.Error(), src) {
				t.Errorf("error %q does not name the offending path", err)
			}
		})
	}
}
