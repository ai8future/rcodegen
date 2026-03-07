# nested claude session error when run inside rserve

**Date:** 2026-03-02
**Severity:** High (rclaude completely non-functional via rserve)

## Problem

When `rserve` runs inside a Claude Code session (e.g., invoked from Claude Code's
terminal), it inherits the `CLAUDECODE` environment variable. The child `claude`
process sees this variable and refuses to launch:

```
Error: Claude Code cannot be launched inside another Claude Code session.
Nested sessions share runtime resources and will crash all active sessions.
To bypass this check, unset the CLAUDECODE environment variable.
Exit 1
```

## Fix

`BuildCommand` in `pkg/tools/claude/claude.go` now builds a filtered copy of
`os.Environ()` with `CLAUDECODE` removed before assigning it to `cmd.Env`.
This allows the child `claude` process to start cleanly regardless of what the
parent environment contains.
