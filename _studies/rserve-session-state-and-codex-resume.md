# rserve Session State and Codex Resume Support

**Date:** April 20, 2026

## Summary

Audit of how rserve maintains multi-turn session state across requests, discovery that Codex sessions are broken, and proposed solutions using Codex's native `--json` and `exec resume` capabilities.

## How rserve Session State Works

rserve is a headless gRPC + HTTP server. No iTerm windows, no PTY, no persistent terminal sessions. The session lifecycle:

1. Client sends request (optionally with a `session_id` from a previous run)
2. Server looks up client-facing session ID in `SessionStore` to get the **tool-native session ID** (e.g., Claude's internal UUID)
3. `cfg.SessionID` is set to the tool-native ID and passed to `BuildCommand`
4. Each tool spawns a **child subprocess** with piped stdout/stderr — the CLI binary runs to completion and exits
5. The `StreamParser` extracts the new `session_id` from the tool's JSONL output (e.g., `system.init` event for Claude)
6. Server stores the mapping: `runID` (client-facing) -> `result.SessionID` (tool-native)
7. On subsequent requests, the tool-native ID is passed via `--resume` to the CLI

**Session continuity** comes from the CLI tools persisting conversation state to disk:
- Claude: `~/.claude/`
- Gemini: its own persistence
- Codex: `~/.codex/sessions/YYYY/MM/DD/rollout-...-<UUID>.jsonl`

rserve just remembers which UUID to pass via `--resume` next time.

## How Each Tool Handles Resume

| Tool | New Session | Resume | Stream Format |
|------|------------|--------|---------------|
| **Claude** | `claude -p <task> --output-format stream-json ...` | `claude --resume <id> -p <task> ...` | JSONL (`system`, `assistant`, `result`) |
| **Gemini** | `gemini -p <task> --output-format stream-json ...` | `gemini --resume <id> -p <task> ...` | JSONL (same pattern) |
| **Codex** | `codex exec <task> ...` (raw terminal output) | PTY wrapper via `python3 codex_pty_wrapper.py <id> <task> ...` | Raw terminal (no JSONL) |

## The Codex Problem

Codex sessions through rserve are **completely broken**:

1. `UsesStreamOutput()` returns `false` for Codex
2. This means `executeCommandWithContext` takes the direct passthrough path (no stream parser)
3. The stream parser never runs, so `cfg.SessionID` is never populated from output
4. `result.SessionID` is always empty
5. `sessions.Store()` is never called
6. The PTY wrapper resume path is dead code in the rserve context

Additionally, the PTY wrapper is unique to Codex — Claude and Gemini don't need one because they work fine with piped I/O and `--resume` directly.

## Key Discovery: Codex Already Has What We Need

Two capabilities in the Codex CLI that solve this:

### 1. `codex exec --json`
Outputs JSONL events to stdout, similar to Claude/Gemini. The first event is:
```json
{"type":"session_meta","payload":{"id":"019d5a8a-d723-7762-ac8f-...",...}}
```
This contains the session UUID we need.

### 2. `codex exec resume <SESSION_ID> <PROMPT>`
Native session resume by UUID — no PTY wrapper needed:
```
codex exec resume <SESSION_ID> <PROMPT> --dangerously-bypass-approvals-and-sandbox --model <model>
```

## Proposed Solutions

### Approach A: Make Codex Use the Stream Parser Path (Recommended)

1. Add `--json` to Codex's `BuildCommand` args
2. Change `UsesStreamOutput()` to return `true`
3. Teach `StreamParser` to recognize Codex's `session_meta` event type and extract `payload.id`
4. Change resume path from PTY wrapper to native `codex exec resume <SESSION_ID> <PROMPT>`

**Concern:** Changing `UsesStreamOutput()` affects BOTH rserve AND the CLI (`rcodex`). The CLI path in `runner.go` also checks this flag:
```go
if r.Tool.UsesStreamOutput() && !cfg.OutputJSON {
    return r.executeWithStreamParser(cfg, cmd)
}
```
If flipped, `rcodex` on the command line would also start getting `--json` output through the stream parser. Codex's JSONL format (`session_meta`, `event_msg`, `response_item`) differs from Claude/Gemini's (`system`, `assistant`, `result`), so the parser would need to handle both formats.

### Approach B: rserve-Only Change

Keep `UsesStreamOutput() = false` but add rserve-specific logic:
- In `RunWithContext` or `executeCommandWithContext`, detect Codex and add `--json` flag
- Use a Codex-specific parser or teach the existing parser to handle both formats
- Only affects the rserve path; CLI `rcodex` remains unchanged

### Approach C: Scan Session Directory (Fragile)

After a Codex run, find the newest file in `~/.codex/sessions/` and extract the UUID from the filename. Race condition risk with concurrent runs. Not recommended.

## Other Audit Findings

- **`cfg.SessionID` dual-purpose** (input + output): If a tool crashes before emitting init, the old session ID gets returned. Fragile but accidentally correct.
- **No session count limit**: Unbounded entries possible with TTL=0. Low risk at expected scale.
- **Silent tool mismatch**: If client sends a Claude session ID but requests Gemini, the session is silently dropped. Could confuse API consumers.
- **Thread safety**: All good — proper mutex usage throughout.
