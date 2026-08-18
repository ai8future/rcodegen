package openai

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// cpBinary is the system copy tool used to clone work directories. An absolute
// path keeps the clone independent of the request's PATH.
const cpBinary = "/bin/cp"

// cloneUseCOW enables the APFS copy-on-write flag (cp -c). Tests flip it to
// exercise the plain-copy fallback.
var cloneUseCOW = runtime.GOOS == "darwin"

// errInvalidWorkDir marks a clone failure caused by the request (a missing or
// non-directory source) rather than by the server.
var errInvalidWorkDir = errors.New("invalid work_dirs entry")

// workDirClone holds the scratch copies of one run's work directories.
type workDirClone struct {
	root string   // scratch root, removed by cleanup
	dirs []string // cloned directories, in the same order as the request's work_dirs
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
	// leaves nothing behind.
	for _, src := range srcs {
		info, err := os.Stat(src)
		if err != nil {
			return nil, fmt.Errorf("%w: %s: %v", errInvalidWorkDir, src, err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("%w: %s is not a directory", errInvalidWorkDir, src)
		}
	}

	root, err := os.MkdirTemp("", "rserve-clone-"+runID+"-")
	if err != nil {
		return nil, fmt.Errorf("create clone scratch root: %w", err)
	}
	clone := &workDirClone{root: root}

	for _, src := range srcs {
		dst := clone.destFor(src)
		method, err := copyDir(ctx, src, dst)
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
func (c *workDirClone) destFor(src string) string {
	base := filepath.Base(filepath.Clean(src))
	if base == "." || base == string(filepath.Separator) {
		base = "workdir"
	}
	dst := filepath.Join(c.root, base)
	for i := 2; ; i++ {
		if _, err := os.Lstat(dst); os.IsNotExist(err) {
			return dst
		}
		dst = filepath.Join(c.root, fmt.Sprintf("%s-%d", base, i))
	}
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
