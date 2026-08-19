package openai

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// SweepOrphanedClones removes the work_dir clone scratch roots left in dir by a
// previous process, returning how many it removed.
//
// Clone roots are cleaned up by a deferred call on the run that owns them, and a
// defer cannot run when the process is killed. Every kill therefore strands the
// scratch roots of whatever was in flight, and they accumulate across restarts
// until something else deletes them.
//
// At startup every matching directory is stale by definition: retention of async
// run results is in-memory only, so no run survives a restart and nothing left on
// disk can still be owned. This assumes one rserve per machine, which the fixed
// gRPC and HTTP ports already enforce — a second instance cannot bind, so it
// cannot be sweeping a live instance's scratch dirs out from under it.
//
// Call this before the listeners start. Failures are logged and counted as
// not-removed: leftover scratch space is a disk-usage problem, never a reason to
// refuse to serve.
func SweepOrphanedClones(dir string, logger *slog.Logger) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if logger != nil {
			logger.Warn("orphaned clone sweep could not read temp dir", "dir", dir, "error", err)
		}
		return 0
	}

	removed := 0
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), cloneDirPrefix) {
			continue
		}
		// Directories only. A regular file or a symlink wearing the prefix is not
		// something this server created, and RemoveAll on a symlink would delete
		// the link while leaving whatever it aimed at — so neither is swept.
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		if err := os.RemoveAll(path); err != nil {
			if logger != nil {
				logger.Warn("orphaned clone sweep failed to remove scratch dir", "path", path, "error", err)
			}
			continue
		}
		removed++
	}
	return removed
}
