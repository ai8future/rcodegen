package openai

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"unicode/utf8"
)

// cpBinary is the system copy tool used to clone work directories. An absolute
// path keeps the clone independent of the request's PATH.
const cpBinary = "/bin/cp"

// cloneUseCOW enables the APFS copy-on-write flag (cp -c). Tests flip it to
// exercise the plain-copy fallback.
var cloneUseCOW = runtime.GOOS == "darwin"

// maxBasename is NAME_MAX on both darwin and linux: the byte limit for a single
// path component. A longer name can never be created, and probing for one
// returns ENAMETOOLONG rather than "does not exist".
const maxBasename = 255

// maxDestAttempts bounds the basename-collision search so a scratch root that
// cannot yield a free name fails the clone instead of spinning.
const maxDestAttempts = 1000

var (
	// errInvalidWorkDir marks a clone failure caused by the request (a missing or
	// non-directory source) rather than by the server.
	errInvalidWorkDir = errors.New("invalid work_dirs entry")

	// errUnsafeSymlink marks a source tree holding a symlink a copy cannot
	// contain, so a write through the clone would reach outside it.
	errUnsafeSymlink = errors.New("unsafe symlink in work_dirs entry")

	// errGitWorktree marks a source holding a gitdir pointer file — a linked
	// worktree or a submodule checkout — which copying cannot isolate from the
	// repository it points at.
	errGitWorktree = errors.New("work_dirs entry contains a git pointer file")
)

// workDirClone holds the scratch copies of one run's work directories.
type workDirClone struct {
	root string   // scratch root, removed by cleanup
	dirs []string // cloned directories, in the same order as the request's work_dirs
}

// checkWorkDirSources validates every source against the clone safety policies
// and returns the symlink-resolved roots in request order. It creates nothing,
// so the handler can run it before committing a run slot and cloneWorkDirs can
// run it again once the slot is held.
func checkWorkDirSources(srcs []string) ([]string, error) {
	roots := make([]string, 0, len(srcs))
	for _, src := range srcs {
		// A symlinked root is fine once resolved; everything below is judged
		// against the resolved path.
		root, err := filepath.EvalSymlinks(src)
		if err != nil {
			return nil, fmt.Errorf("%w: %s: %v", errInvalidWorkDir, src, err)
		}
		info, err := os.Stat(root)
		if err != nil {
			return nil, fmt.Errorf("%w: %s: %v", errInvalidWorkDir, src, err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("%w: %s is not a directory", errInvalidWorkDir, src)
		}
		if err := checkTree(src, root); err != nil {
			return nil, err
		}
		roots = append(roots, root)
	}
	return roots, nil
}

// checkTree walks the whole source tree once and enforces both clone-safety
// policies on it: no gitdir pointer file at any depth, and no symlink the
// clone cannot contain.
func checkTree(src, root string) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("%w: %s: %v", errInvalidWorkDir, src, err)
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = d.Name()
		}
		// A .git regular file is a gitdir pointer: the root's own (a linked
		// worktree) or a nested one (a submodule checkout). Either way the copy
		// keeps using the original repository's index and refs, so work inside
		// the "isolated" clone mutates the caller's repository. A .git
		// directory is self-contained and copies cleanly.
		if d.Name() == ".git" && d.Type().IsRegular() {
			return fmt.Errorf("%w: %s: %s is a file pointing at another repository's gitdir, "+
				"which copying cannot isolate — linked worktrees and submodule checkouts must be "+
				"cloned by git, not by copying; point work_dirs at a main worktree with no "+
				"submodule checkouts instead",
				errGitWorktree, src, rel)
		}
		if d.Type()&fs.ModeSymlink == 0 {
			return nil
		}
		target, err := os.Readlink(path)
		if err != nil {
			return fmt.Errorf("%w: %s: cannot read symlink %s: %v", errInvalidWorkDir, src, rel, err)
		}
		if filepath.IsAbs(target) {
			return fmt.Errorf("%w: %s: %s is an absolute symlink", errUnsafeSymlink, src, rel)
		}
		// WalkDir never descends through a symlink and root is already resolved,
		// so every parent component here is a real directory and the target
		// resolves lexically.
		resolved := filepath.Clean(filepath.Join(filepath.Dir(path), target))
		if resolved != root && !strings.HasPrefix(resolved, root+string(filepath.Separator)) {
			return fmt.Errorf("%w: %s: %s resolves outside the work_dir", errUnsafeSymlink, src, rel)
		}
		return nil
	})
}

// cloneWorkDirs copies each source directory into a fresh per-run scratch root
// so the CLI subprocess writes its state and temp files there instead of into
// shared source trees. Returns nil when srcs is empty. The caller must call
// cleanup on the returned clone once the run is done.
func cloneWorkDirs(ctx context.Context, runID string, srcs []string, logger *slog.Logger) (*workDirClone, error) {
	if len(srcs) == 0 {
		return nil, nil
	}

	// Validate every source before creating any scratch state, so a bad request
	// leaves nothing behind. The handler already ran this check before taking a
	// run slot; repeating it here closes the window between the two, where a
	// source can vanish or change shape while the request waits for a slot.
	roots, err := checkWorkDirSources(srcs)
	if err != nil {
		return nil, err
	}

	root, err := os.MkdirTemp("", "rserve-clone-"+runID+"-")
	if err != nil {
		return nil, fmt.Errorf("create clone scratch root: %w", err)
	}
	clone := &workDirClone{root: root}

	for i, src := range srcs {
		dst, err := clone.destFor(src)
		if err != nil {
			clone.cleanup(logger)
			return nil, fmt.Errorf("clone work_dir %s: %w", src, err)
		}
		method, err := copyDir(ctx, roots[i], dst)
		if err != nil {
			clone.cleanup(logger)
			return nil, fmt.Errorf("clone work_dir %s: %w", src, err)
		}
		clone.dirs = append(clone.dirs, dst)
		logger.Info("cloned work_dir", "run_id", runID, "src", src, "dst", dst, "method", method)
	}

	return clone, nil
}

// destFor builds the scratch path for a source directory, keeping its basename
// and disambiguating sources that share one.
func (c *workDirClone) destFor(src string) (string, error) {
	base := filepath.Base(filepath.Clean(src))
	if base == "." || base == string(filepath.Separator) {
		base = "workdir"
	}
	for i := 1; i <= maxDestAttempts; i++ {
		name := fitBasename(base, 0)
		if i > 1 {
			suffix := fmt.Sprintf("-%d", i)
			name = fitBasename(base, len(suffix)) + suffix
		}
		dst := filepath.Join(c.root, name)
		_, err := os.Lstat(dst)
		switch {
		case os.IsNotExist(err):
			return dst, nil
		case err != nil:
			// Only "does not exist" means the name is free. Treating every other
			// error as free-to-retry is what let an unprobeable name loop.
			return "", fmt.Errorf("check clone destination: %w", err)
		}
	}
	return "", fmt.Errorf("no free clone destination for %q after %d attempts", base, maxDestAttempts)
}

// fitBasename truncates name so it plus a suffix of suffixLen bytes fits within
// the filesystem's per-component limit, cutting on a rune boundary.
func fitBasename(name string, suffixLen int) string {
	limit := maxBasename - suffixLen
	if limit < 1 {
		limit = 1
	}
	if len(name) <= limit {
		return name
	}
	trimmed := name[:limit]
	for len(trimmed) > 0 && !utf8.ValidString(trimmed) {
		trimmed = trimmed[:len(trimmed)-1]
	}
	if trimmed == "" {
		trimmed = "workdir"
	}
	return trimmed
}

// cleanup removes the scratch root. A failure is logged, never returned: it
// must not fail the run.
func (c *workDirClone) cleanup(logger *slog.Logger) {
	if c == nil || c.root == "" {
		return
	}
	if err := os.RemoveAll(c.root); err != nil && logger != nil {
		logger.Warn("work_dir clone cleanup failed", "root", c.root, "error", err)
	}
}

// count returns the number of cloned directories.
func (c *workDirClone) count() int {
	if c == nil {
		return 0
	}
	return len(c.dirs)
}

// copyDir recursively copies src to dst (which must not exist), including
// dotfiles. On APFS it first tries a copy-on-write clone, which is near-instant
// and space-free; anything that rejects -c falls back to a real copy. The
// method used ("cow" or "copy") is returned for logging.
func copyDir(ctx context.Context, src, dst string) (string, error) {
	if cloneUseCOW {
		if err := runCP(ctx, "-Rc", src, dst); err == nil {
			return "cow", nil
		}
		// A rejected or partial clone leaves debris that would break the retry.
		os.RemoveAll(dst)
	}
	if err := runCP(ctx, "-R", src, dst); err != nil {
		return "", err
	}
	return "copy", nil
}

// runCP runs the system copy tool with the given flag.
func runCP(ctx context.Context, flag, src, dst string) error {
	out, err := exec.CommandContext(ctx, cpBinary, flag, src, dst).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			return err
		}
		return fmt.Errorf("%w: %s", err, msg)
	}
	return nil
}
