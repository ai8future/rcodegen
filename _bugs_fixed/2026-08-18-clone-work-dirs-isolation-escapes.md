# clone_work_dirs isolation escapes and run-slot defects

External audit of rserve's `clone_work_dirs` feature found five defects: three high, two medium.
All were empirically reproduced before being fixed.

## High: symlinks escaped the clone

`/bin/cp -Rc` copies symlinks as symlinks. A cloned tree therefore kept any link that pointed
outside itself, and a write through that link inside the "isolated" clone landed in the caller's
original tree — the exact thing cloning exists to prevent.

Fix (`pkg/server/openai/workdirclone.go`): the source root is resolved with `filepath.EvalSymlinks`
first (a symlinked root is fine), then the tree is walked with `Lstat`. Any absolute symlink is
rejected; a relative symlink is allowed only when its target resolves inside the source root.
Rejection is `400 unsafe_symlink` naming the offending path relative to the root. `WalkDir` never
descends through a symlink, so every parent of a link under inspection is a real directory and the
target resolves lexically.

## High: linked git worktrees were not isolated

A linked worktree's `.git` is a regular *file* holding `gitdir: /elsewhere/.git/worktrees/<name>`.
Copying the tree copies that pointer, so the clone kept using the original repository's index and
refs — staging inside the clone mutated the caller's repository.

Fix: a source root whose `.git` is a regular file is rejected with `400 unsupported_git_worktree`.
A `.git` directory is self-contained and still clones normally.

## High: NAME_MAX made the collision suffixer spin forever

`destFor` probed candidate destinations with `Lstat` and treated every non-`IsNotExist` result as
"try the next suffix". A basename longer than NAME_MAX (255 bytes) returns `ENAMETOOLONG`, which is
neither "exists" nor "does not exist", so the loop never terminated — and it held no context, so
the run was uncancellable.

Fix: only `IsNotExist` means the name is free; any other `Lstat` error fails the clone. The suffix
search is bounded at 1000 attempts, and the base is truncated (on a rune boundary) so basename plus
suffix always fits 255 bytes.

## Medium: direct-API runs ignored cancellation

`Runner.executeCommandWithContext` dropped its context when dispatching to `DirectAPIRunner`, and
gemini's image path used bare `http.Post`. A disconnected client's run kept its concurrency slot and
its scratch clone until the API answered.

Fix: `DirectAPIRunner.RunDirectAPI` now takes a `context.Context`, threaded from the run context.
Gemini builds its request with `http.NewRequestWithContext` and a client bounded at 5 minutes. The
API key rides in the query string and transport errors quote the URL, so error output is scrubbed
before it reaches stderr.

## Medium: validation ran after the run slot was acquired

`RunRegistry.Acquire` blocks until a slot frees up. Validation happened after it, so a request that
could never run waited behind real work and consumed a slot just to return its 400.

Fix: all `work_dirs` validation now runs before `Acquire`. Sources are re-checked once the slot is
held, closing the window in between; a source that vanished during the wait fails the run with
`500 clone_failed` rather than hanging or being misreported as a bad request.
