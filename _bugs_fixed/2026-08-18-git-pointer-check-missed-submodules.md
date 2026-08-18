# Clone policy: the git pointer check only looked at the root

**Date:** 2026-08-18
**Where:** `pkg/server/openai/workdirclone.go`
**Shipped in:** 4.2.13

## What was wrong

`clone_work_dirs` refused a linked git worktree by `Lstat`-ing `{root}/.git` and
rejecting it when it was a regular file. That is the right rule applied to one
path. A submodule checkout puts exactly the same gitdir pointer file at
`vendor/lib/.git`, several levels down, where the check never looked.

So a source tree with submodule checkouts passed validation and was cloned. The
copy kept the pointer file, which still named the *original* repository's
`.git/modules/...` directory, so any git operation the agent ran inside the
"isolated" clone — staging, committing, checking out — mutated the caller's
repository. That is precisely the failure `unsupported_git_worktree` exists to
prevent, reached by a different path.

## Fix

The root-only `Lstat` is gone. The tree walk that already checked every symlink
now checks every entry for a regular file named `.git` at any depth and rejects
the source with `400 unsupported_git_worktree`, naming the path relative to the
source root. `.git` *directories* remain fine anywhere — they are
self-contained and copy cleanly, so a vendored repository still works — and
files that merely look git-adjacent (`.gitignore`, `.gitmodules`, `notes.git`)
are untouched.

Merging the two checks into one walk also means one traversal instead of two.

## Tests

`TestCloneWorkDirs_RejectsNestedGitPointerFile` (one and three levels deep),
`TestCloneWorkDirs_AcceptsNestedGitDirectory`,
`TestCloneWorkDirs_AcceptsFilesNamedLikeGit`, and
`TestHandleChatCompletions_CloneWorkDirsRejectsNestedGitPointer` for the HTTP
path. The original root-file test still passes unchanged.
