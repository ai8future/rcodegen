# Orphaned `rserve-clone-*` scratch dirs survive process restarts

**Fixed in:** 4.3.4
**Severity:** low (unbounded disk growth, no correctness impact)
**Observed:** production — two 4KB `rserve-clone-*` husks in `$TMPDIR` left over
from pre-4.3.3 runs.

## What happened

`clone_work_dirs` copies each `work_dirs` entry into a per-run scratch root under
`os.TempDir()` named `rserve-clone-<run_id>-<random>`. Cleanup is a deferred
`os.RemoveAll` owned by the run.

A deferred call does not run when the process is killed. Every `SIGKILL`, crash,
or hard restart therefore stranded the scratch roots of whatever runs were in
flight, and nothing ever collected them. They accumulate across restarts, on a
path that no operator thinks to check.

Empty-looking husks understate it: what leaks is a full recursive copy of the
caller's work directories. A restart during a run against a large repository
strands that whole tree, not 4KB.

## Fix

`openai.SweepOrphanedClones` (`pkg/server/openai/clonesweep.go`) scans
`os.TempDir()` at startup and removes every **directory** whose name starts with
`rserve-clone-`. It logs one INFO line with the count removed (including zero);
a removal failure is a WARN and the sweep continues. Leftover disk is never a
reason to refuse to serve.

Two properties make "delete everything that matches" safe:

- **Nothing on disk can still be owned.** Async run retention is in-memory only,
  so no run survives a restart. At startup every matching directory is stale by
  definition.
- **There is only one instance.** Enforced by the fixed gRPC/HTTP ports — a
  second rserve cannot bind.

That second property is why **the sweep runs after `net.Listen` succeeds and
before anything is served** (`cmd/rserve/main.go`). Sweeping before the bind
would let a second, doomed process delete the *running* instance's live scratch
dirs on its way to failing to start. Holding the port is the proof that the
leftovers are ours to delete.

The prefix is now the shared constant `cloneDirPrefix` in `workdirclone.go`, so
the sweeper and the cloner cannot drift apart.

## Scope

Only directories, and only exact-prefix matches. A regular file or symlink
wearing the prefix is skipped: this server never creates one, and `RemoveAll` on
a symlink would unlink the link while leaving its target. Neighbours like
`rserve-files` (the upload store, which has its own 24h purge) do not match.

## Tests

`pkg/server/openai/clonesweep_test.go` — removes matching dirs including
populated ones; leaves `rserve-files`, unrelated dirs, and prefixed regular files
alone; tolerates a missing temp dir and a nil logger; counts a removal failure as
not-removed and keeps sweeping the rest; and drives a real `cloneWorkDirs` result
through the sweep (via `TMPDIR`) so the two halves are proven to agree on the
name.
