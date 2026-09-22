# Antigravity CLI (agy) Support via New ragy Binary

**Date:** July 30, 2026

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

## Summary

Google sunset the Gemini CLI for individual accounts on June 18, 2026; the local `gemini` v0.49.0 binary now hard-fails with `IneligibleTierError` (exit 55), which means `rgemini` is currently non-functional against a dead upstream. This plan adds a 7th binary, `ragy`, wrapping Google's replacement `agy` (Antigravity CLI, verified working locally at v1.0.16), including a native parser for agy's `stream-json` envelope so live streaming, token accounting, and session resume all keep working. `rgemini` is left intact for eligible enterprise/Vertex tiers and because its direct-API image path never touched the CLI.

**Goal:** Ship a `ragy` binary that gives rcodegen full feature parity with `rgemini` against the Antigravity CLI, including live streamed output, usage stats, and conversation resume.

**Architecture:** A new `pkg/tools/antigravity` package implements `runner.Tool` following the `pkg/tools/kilocode` template. `pkg/runner/stream.go` gains an agy-envelope branch that translates agy's `{"event": ...}` NDJSON into the existing `StreamEvent` pipeline, so the gRPC callback, session capture, and usage capture all work unchanged. Registration follows the established seven-site pattern used by `rkilo`/`ropencode`.

**Tech Stack:** Go 1.x, `github.com/ai8future/chassis-go/v11`, `agy` CLI ≥ 1.0.16, existing `runner.Tool` interface.

## Global Constraints

- Registry key for the tool is `"agy"`; Go package is `antigravity`; rcodegen binary is `ragy`; underlying CLI binary is `agy`.
- Build only via `make` (never bare `go build`) so `-ldflags` bakes in VERSION.
- Do **not** read VERSION until the very last task, to avoid clobbering concurrent agents' increments.
- Every code change requires a VERSION bump plus a CHANGELOG entry, then commit with `git add -A`.
- Commit messages must identify the agent and model, e.g. `Claude:Opus 5 (1M context)`.
- Do not modify `pkg/tools/gemini/` — `rgemini` stays as-is.
- Never create public GitHub repos.

## File Structure

| File | Responsibility |
|---|---|
| `pkg/runner/stream_agy.go` (create) | agy envelope structs + translation into `StreamEvent`; keeps `stream.go` focused |
| `pkg/runner/stream_agy_test.go` (create) | Table tests for agy event translation |
| `pkg/runner/stream.go` (modify) | Envelope detection hook + `ThinkingTokens` field + `fire()` helper |
| `pkg/runner/tool.go` (modify) | Optional `StdinCloser` interface |
| `pkg/runner/runner.go` (modify:491,513) | Honor `StdinCloser` to avoid the non-TTY hang |
| `pkg/tools/antigravity/antigravity.go` (create) | `runner.Tool` implementation for agy |
| `pkg/tools/antigravity/antigravity_test.go` (create) | Command construction + alias resolution tests |
| `cmd/ragy/main.go` (create) | Binary entrypoint |
| `scripts/launcher-ragy.sh` (create) | Platform-dispatch launcher |
| `Makefile` (modify:8) | Add `ragy` to `BINS` |
| `pkg/settings/settings.go` (modify) | `AntigravityDefaults` + constants + fallback wiring |
| `pkg/orchestrator/orchestrator.go` (modify:92) | Tool registry entry |
| `pkg/orchestrator/progress.go` (modify:131) | Tool display color |
| `pkg/batch/manifest.go` (modify:17) | Allow `agy` as a job tool |
| `pkg/batch/executor_local.go` (modify:39) | Tool factory |
| `cmd/rserve/main.go` (modify:76) | Tool factory |
| `cmd/rbatch/main.go` (modify:669) | Help text |
| `pkg/executor/tool.go` (modify:181,226) | Usage + session-ID extraction for agy |

---

## Verified Reference Data

All of the following was confirmed empirically against `agy` v1.0.16 — do not re-derive, but do re-verify if the CLI has been updated.

**Flag mapping (`gemini` → `agy`):**

| gemini | agy |
|---|---|
| `-p <task>` | `-p <task>` (unchanged) |
| `--output-format stream-json` | same flag, different schema |
| `--yolo` | `--dangerously-skip-permissions` |
| `-m <model>` | `--model` (+ separate `--effort low\|medium\|high`) |
| `--resume <id>` | `--conversation <id>` |
| — | `--print-timeout` (**defaults to 5m**), `--mode`, `--sandbox`, `--add-dir`, `--agent`, `--json-schema` |

**`agy models` output (authoritative model list):**

```
Gemini 3.6 Flash (High)     Gemini 3.5 Flash (High)     Gemini 3.1 Pro (High)
Gemini 3.6 Flash (Medium)   Gemini 3.5 Flash (Medium)   Gemini 3.1 Pro (Low)
Gemini 3.6 Flash (Low)      Gemini 3.5 Flash (Low)
Claude Sonnet 4.6 (Thinking)   Claude Opus 4.6 (Thinking)   GPT-OSS 120B (Medium)
```

Both display names (`"Gemini 3.1 Pro (High)"`) and slugs (`"gemini-3.5-flash"`) are accepted. An unknown model exits with `status: "ERROR"` and an `error` field listing valid names — agy self-validates, so rcodegen should not hard-code a whitelist.

**Captured stream-json (real run, abridged):**

```json
{"event":"init","conversation_id":"d334109a-…","init":{"cwd":"…","tools":["…"],"permission_mode":"always-proceed"}}
{"event":"step_update","step_update":{"conversation_id":"d334109a-…","step_index":3,"state":"ACTIVE","step_type":"agent_response","text_delta":"pong"}}
{"event":"step_update","step_update":{"conversation_id":"d334109a-…","step_index":3,"state":"DONE","step_type":"agent_response","text_delta":"\n","duration_seconds":3.58,"usage":{"input_tokens":17323,"output_tokens":291,"thinking_tokens":290,"cache_read_tokens":0,"total_tokens":17614}}}
{"event":"result","result":{"conversation_id":"d334109a-…","status":"SUCCESS","response":"pong\n","duration_seconds":4.44,"num_turns":1,"usage":{"input_tokens":17422,"output_tokens":295,"thinking_tokens":290,"cache_read_tokens":0,"total_tokens":17717}}}
```

Observed `step_type` values: `user_input`, `agent_response`, `checkpoint`, `unknown`. `state` is `ACTIVE` or `DONE`. **`tool_call` / `tool_info` shape was not observed** and must be discovered in Task 2.

---

## Task 1: agy stream event translation

**Files:**
- Create: `pkg/runner/stream_agy.go`
- Create: `pkg/runner/stream_agy_test.go`
- Modify: `pkg/runner/stream.go:31-36` (add `ThinkingTokens`), `pkg/runner/stream.go:107-140` (detection hook)

**Interfaces:**
- Consumes: existing `StreamEvent`, `TokenUsage`, `AssistantMsg`, `ContentBlock`, `StreamParser` from `stream.go`.
- Produces: `AgyEnvelope`, `AgyInit`, `AgyStepUpdate`, `AgyToolInfo`, `AgyResult`, `AgyUsage` structs; `(*StreamParser).processAgyLine(raw []byte)`; `(*StreamParser).fire(ev *StreamEvent)`. Task 3 relies on `UsesStreamOutput() == true` working end-to-end; Task 7 relies on the `result` envelope field names.

- [ ] **Step 1: Write the failing test**

Create `pkg/runner/stream_agy_test.go`:

```go
package runner

import (
	"bytes"
	"strings"
	"testing"
)

func TestProcessLineAgyInitCapturesConversationID(t *testing.T) {
	var buf bytes.Buffer
	p := NewStreamParser(&buf)
	p.ProcessLine(`{"event":"init","conversation_id":"abc-123","init":{"cwd":"/tmp","tools":["view_file"],"permission_mode":"always-proceed"}}`)

	if p.SessionID != "abc-123" {
		t.Errorf("SessionID = %q, want %q", p.SessionID, "abc-123")
	}
}

func TestProcessLineAgyTextDeltaHasNoTrailingNewline(t *testing.T) {
	var buf bytes.Buffer
	p := NewStreamParser(&buf)
	p.ProcessLine(`{"event":"step_update","step_update":{"step_index":3,"state":"ACTIVE","step_type":"agent_response","text_delta":"pon"}}`)
	p.ProcessLine(`{"event":"step_update","step_update":{"step_index":3,"state":"ACTIVE","step_type":"agent_response","text_delta":"g"}}`)

	got := stripANSI(buf.String())
	if got != "pong" {
		t.Errorf("output = %q, want %q (deltas must concatenate without newlines)", got, "pong")
	}
}

func TestProcessLineAgyResultCapturesUsage(t *testing.T) {
	var buf bytes.Buffer
	p := NewStreamParser(&buf)
	p.ProcessLine(`{"event":"result","result":{"conversation_id":"abc-123","status":"SUCCESS","response":"pong\n","num_turns":1,"usage":{"input_tokens":17422,"output_tokens":295,"thinking_tokens":290,"cache_read_tokens":7,"total_tokens":17717}}}`)

	if p.Usage == nil {
		t.Fatal("Usage was not captured")
	}
	if p.Usage.InputTokens != 17422 {
		t.Errorf("InputTokens = %d, want 17422", p.Usage.InputTokens)
	}
	if p.Usage.OutputTokens != 295 {
		t.Errorf("OutputTokens = %d, want 295", p.Usage.OutputTokens)
	}
	if p.Usage.ThinkingTokens != 290 {
		t.Errorf("ThinkingTokens = %d, want 290", p.Usage.ThinkingTokens)
	}
	if p.Usage.CacheReadInputTokens != 7 {
		t.Errorf("CacheReadInputTokens = %d, want 7", p.Usage.CacheReadInputTokens)
	}
}

func TestProcessLineAgyErrorResultMarksFailure(t *testing.T) {
	var buf bytes.Buffer
	var got *StreamEvent
	p := NewStreamParserWithCallback(&buf, nil, func(ev *StreamEvent) {
		if ev.Type == "result" {
			got = ev
		}
	})
	p.ProcessLine(`{"event":"result","result":{"conversation_id":"","status":"ERROR","response":"","error":"invalid model selection"}}`)

	if got == nil {
		t.Fatal("no result event delivered to callback")
	}
	if !got.IsError {
		t.Error("IsError = false, want true for status ERROR")
	}
	if !strings.Contains(stripANSI(buf.String()), "invalid model selection") {
		t.Errorf("error text missing from output: %q", buf.String())
	}
}

func TestProcessLineClaudeFormatStillWorks(t *testing.T) {
	var buf bytes.Buffer
	p := NewStreamParser(&buf)
	p.ProcessLine(`{"type":"system","subtype":"init","session_id":"claude-999"}`)

	if p.SessionID != "claude-999" {
		t.Errorf("SessionID = %q, want %q (regression: Claude path broken)", p.SessionID, "claude-999")
	}
}

// stripANSI removes ANSI color escapes so assertions compare plain text.
func stripANSI(s string) string {
	var out strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b {
			for i < len(s) && s[i] != 'm' {
				i++
			}
			continue
		}
		out.WriteByte(s[i])
	}
	return out.String()
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/runner/ -run TestProcessLineAgy -v`
Expected: FAIL — compile error, `p.Usage.ThinkingTokens` undefined and agy lines fall through to the `default:` branch producing empty output.

- [ ] **Step 3: Add the ThinkingTokens field**

In `pkg/runner/stream.go`, replace the `TokenUsage` struct (lines 31-36) with:

```go
// TokenUsage represents token usage from a Claude run
type TokenUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
	ThinkingTokens           int `json:"thinking_tokens,omitempty"` // Antigravity CLI reports reasoning tokens
}
```

- [ ] **Step 4: Add the envelope detection hook**

In `pkg/runner/stream.go`, inside `ProcessLine`, insert this block immediately after the `if line == ""` guard (i.e. before `var event StreamEvent`):

```go
	// agy (Antigravity CLI) uses a different envelope: {"event": "...", ...}.
	// Detect it before falling back to the Claude/Gemini "type" schema.
	var probe struct {
		Event string `json:"event"`
	}
	if err := json.Unmarshal([]byte(line), &probe); err == nil && probe.Event != "" {
		p.processAgyLine([]byte(line))
		return
	}
```

Then add the `fire` helper at the end of `stream.go`:

```go
// fire delivers an event to the registered callback, if any.
func (p *StreamParser) fire(ev *StreamEvent) {
	if p.callback != nil {
		p.callback(ev)
	}
}
```

- [ ] **Step 5: Create the agy translation file**

Create `pkg/runner/stream_agy.go`:

```go
// Package runner: Antigravity CLI (agy) stream-json translation.
//
// agy emits NDJSON with a different envelope than Claude/Gemini:
//
//	{"event":"init","conversation_id":"…","init":{…}}
//	{"event":"step_update","step_update":{…,"text_delta":"…"}}
//	{"event":"result","result":{…,"status":"SUCCESS","usage":{…}}}
//
// These are translated into the shared StreamEvent shape so the gRPC
// callback, session capture, and usage capture all work unchanged.
package runner

import (
	"encoding/json"
	"fmt"
)

// AgyEnvelope is the top-level agy stream-json line.
type AgyEnvelope struct {
	Event          string         `json:"event"`
	ConversationID string         `json:"conversation_id,omitempty"`
	Init           *AgyInit       `json:"init,omitempty"`
	StepUpdate     *AgyStepUpdate `json:"step_update,omitempty"`
	Result         *AgyResult     `json:"result,omitempty"`
}

// AgyInit describes the run configuration emitted at startup.
type AgyInit struct {
	Cwd            string   `json:"cwd,omitempty"`
	Tools          []string `json:"tools,omitempty"`
	PermissionMode string   `json:"permission_mode,omitempty"`
}

// AgyStepUpdate is a single step transition. State is ACTIVE or DONE.
type AgyStepUpdate struct {
	ConversationID  string       `json:"conversation_id,omitempty"`
	StepIndex       int          `json:"step_index"`
	State           string       `json:"state,omitempty"`
	StepType        string       `json:"step_type,omitempty"`
	TextDelta       string       `json:"text_delta,omitempty"`
	DurationSeconds float64      `json:"duration_seconds,omitempty"`
	ToolInfo        *AgyToolInfo `json:"tool_info,omitempty"`
	Usage           *AgyUsage    `json:"usage,omitempty"`
}

// AgyToolInfo describes a tool invocation. Field names confirmed in Task 2.
type AgyToolInfo struct {
	Name string          `json:"name,omitempty"`
	Args json.RawMessage `json:"args,omitempty"`
}

// AgyResult is the terminal event, mirroring --output-format json.
type AgyResult struct {
	ConversationID   string          `json:"conversation_id,omitempty"`
	Status           string          `json:"status,omitempty"`
	Response         string          `json:"response,omitempty"`
	Error            string          `json:"error,omitempty"`
	DurationSeconds  float64         `json:"duration_seconds,omitempty"`
	NumTurns         int             `json:"num_turns,omitempty"`
	StructuredOutput json.RawMessage `json:"structured_output,omitempty"`
	Usage            *AgyUsage       `json:"usage,omitempty"`
}

// AgyUsage is agy's token accounting block.
type AgyUsage struct {
	InputTokens     int `json:"input_tokens"`
	OutputTokens    int `json:"output_tokens"`
	ThinkingTokens  int `json:"thinking_tokens"`
	CacheReadTokens int `json:"cache_read_tokens"`
	TotalTokens     int `json:"total_tokens"`
}

// processAgyLine parses and dispatches one agy NDJSON line.
func (p *StreamParser) processAgyLine(raw []byte) {
	var env AgyEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		fmt.Fprintln(p.writer, string(raw))
		return
	}

	switch env.Event {
	case "init":
		p.handleAgyInit(env)
	case "step_update":
		p.handleAgyStep(env.StepUpdate)
	case "result":
		p.handleAgyResult(env.Result)
	default:
		if p.logger != nil {
			p.logger.Debug("unknown agy stream event", "event", env.Event)
		}
	}
}

// handleAgyInit captures the conversation ID used for --conversation resume.
func (p *StreamParser) handleAgyInit(env AgyEnvelope) {
	if env.ConversationID != "" {
		p.SessionID = env.ConversationID
	}
	if !p.initialized {
		fmt.Fprintf(p.writer, "%s%s⚡ Antigravity initialized%s\n", Dim, Cyan, Reset)
		p.initialized = true
	}
	p.fire(&StreamEvent{Type: "system", Subtype: "init", SessionID: env.ConversationID})
}

// handleAgyStep renders incremental agent output and tool calls.
func (p *StreamParser) handleAgyStep(step *AgyStepUpdate) {
	if step == nil {
		return
	}
	if step.ConversationID != "" && p.SessionID == "" {
		p.SessionID = step.ConversationID
	}

	switch step.StepType {
	case "agent_response":
		if step.TextDelta == "" {
			return
		}
		if p.inToolUse {
			fmt.Fprintln(p.writer)
			p.inToolUse = false
		}
		// Deltas are partial tokens: never append a newline.
		fmt.Fprintf(p.writer, "%s%s%s", White, step.TextDelta, Reset)
		p.lastType = "assistant"
		p.fire(&StreamEvent{
			Type:    "assistant",
			Message: &AssistantMsg{Content: []ContentBlock{{Type: "text", Text: step.TextDelta}}},
		})
	case "tool_call":
		if step.State != "ACTIVE" || step.ToolInfo == nil {
			return
		}
		block := ContentBlock{Type: "tool_use", Name: step.ToolInfo.Name, Input: step.ToolInfo.Args}
		p.handleToolUse(block)
		p.fire(&StreamEvent{
			Type:    "assistant",
			Message: &AssistantMsg{Content: []ContentBlock{block}},
		})
	}
}

// handleAgyResult captures final usage and reports failures.
func (p *StreamParser) handleAgyResult(res *AgyResult) {
	if res == nil {
		return
	}
	if res.ConversationID != "" {
		p.SessionID = res.ConversationID
	}
	if res.Usage != nil {
		p.Usage = &TokenUsage{
			InputTokens:          res.Usage.InputTokens,
			OutputTokens:         res.Usage.OutputTokens,
			CacheReadInputTokens: res.Usage.CacheReadTokens,
			ThinkingTokens:       res.Usage.ThinkingTokens,
		}
	}

	isErr := res.Status != "" && res.Status != "SUCCESS"
	if isErr {
		fmt.Fprintf(p.writer, "\n%s%s⚠️  Task failed (%s)%s\n", Bold, Red, res.Status, Reset)
		if res.Error != "" {
			fmt.Fprintf(p.writer, "%s%s%s\n", Red, res.Error, Reset)
		}
	}

	p.fire(&StreamEvent{
		Type:      "result",
		Result:    res.Response,
		IsError:   isErr,
		Usage:     p.Usage,
		SessionID: res.ConversationID,
	})
}
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./pkg/runner/ -v`
Expected: PASS, including the pre-existing `stream_test.go` cases (no Claude/Gemini regression).

- [ ] **Step 7: Commit**

```bash
git add pkg/runner/stream.go pkg/runner/stream_agy.go pkg/runner/stream_agy_test.go
git commit -m "feat(runner): parse Antigravity CLI (agy) stream-json envelope"
```

---

## Task 2: Confirm the tool_call event shape

`tool_info` was never observed in the smoke test, so Task 1 guessed its field names. This task replaces the guess with observed fact.

**Files:**
- Modify: `pkg/runner/stream_agy.go` (the `AgyToolInfo` struct and the `case "tool_call"` branch)
- Modify: `pkg/runner/stream_agy_test.go` (add a real-shape test)

**Interfaces:**
- Consumes: `AgyToolInfo`, `handleAgyStep` from Task 1.
- Produces: a verified `AgyToolInfo` definition. No signature changes.

- [ ] **Step 1: Capture a real tool-calling run**

```bash
cd "$(mktemp -d)" && printf 'hello\n' > sample.txt
agy -p "Read sample.txt and tell me what it contains." \
    --output-format stream-json --dangerously-skip-permissions \
    --print-timeout 5m < /dev/null > agy-tools.ndjson 2>/dev/null
grep -o '"step_type":"[^"]*"' agy-tools.ndjson | sort -u
python3 -c "import json,sys
for l in open('agy-tools.ndjson'):
    e=json.loads(l)
    su=e.get('step_update') or {}
    if su.get('tool_info'): print(json.dumps(su['tool_info'],indent=2)); break"
```

Record the exact `step_type` string used for tool calls and the exact keys inside `tool_info`.

- [ ] **Step 2: Write a test using the captured JSON**

Add to `pkg/runner/stream_agy_test.go`, substituting the real field names and step_type observed in Step 1:

```go
func TestProcessLineAgyToolCallRenders(t *testing.T) {
	var buf bytes.Buffer
	p := NewStreamParser(&buf)
	// Replace this literal with a line copied verbatim from agy-tools.ndjson.
	p.ProcessLine(`{"event":"step_update","step_update":{"step_index":2,"state":"ACTIVE","step_type":"tool_call","tool_info":{"name":"view_file","args":{"file_path":"sample.txt"}}}}`)

	got := stripANSI(buf.String())
	if !strings.Contains(got, "view_file") && !strings.Contains(got, "Reading file") {
		t.Errorf("tool call not rendered: %q", got)
	}
}
```

- [ ] **Step 3: Run the test**

Run: `go test ./pkg/runner/ -run TestProcessLineAgyToolCall -v`
Expected: FAIL if the guessed field names differ from reality; PASS if the guess was right.

- [ ] **Step 4: Correct the struct to match observed reality**

Update `AgyToolInfo` field tags in `pkg/runner/stream_agy.go` to the observed keys, and update the `case "tool_call":` label in `handleAgyStep` if agy uses a different `step_type` string. If agy nests tool names differently, map them in `handleAgyStep` before constructing the `ContentBlock`.

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./pkg/runner/ -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add pkg/runner/stream_agy.go pkg/runner/stream_agy_test.go
git commit -m "fix(runner): match agy tool_info schema to observed output"
```

---

## Task 3: Closed-stdin guard

`pkg/runner/runner.go:491` and `:513` set `cmd.Stdin = os.Stdin`. agy has a [known hang](https://github.com/google-antigravity/antigravity-cli/issues/318) in print mode when stdin stays open in non-TTY contexts — which is exactly how `rserve` and `rbatch` invoke tools. This adds an opt-in escape hatch.

**Files:**
- Modify: `pkg/runner/tool.go` (append interface after `DirectAPIRunner`)
- Modify: `pkg/runner/runner.go:491`, `pkg/runner/runner.go:513`

**Interfaces:**
- Produces: `runner.StdinCloser` interface with `WantsClosedStdin() bool`. Task 4's `antigravity.Tool` implements it.

- [ ] **Step 1: Add the optional interface**

Append to `pkg/runner/tool.go`:

```go
// StdinCloser is an optional interface tools can implement to run with stdin
// detached. agy's print mode can hang waiting on an open stdin in non-TTY
// contexts (rserve/rbatch), so it opts in.
type StdinCloser interface {
	WantsClosedStdin() bool
}
```

- [ ] **Step 2: Honor it at both call sites**

In `pkg/runner/runner.go`, replace the line `cmd.Stdin = os.Stdin` at **both** line 491 and line 513 with:

```go
	if sc, ok := r.Tool.(StdinCloser); ok && sc.WantsClosedStdin() {
		cmd.Stdin = nil
	} else {
		cmd.Stdin = os.Stdin
	}
```

- [ ] **Step 3: Verify it compiles and nothing regressed**

Run: `go build ./... && go test ./pkg/runner/ -v`
Expected: PASS — no existing tool implements `StdinCloser`, so behavior is unchanged.

- [ ] **Step 4: Commit**

```bash
git add pkg/runner/tool.go pkg/runner/runner.go
git commit -m "feat(runner): add optional StdinCloser to detach stdin per tool"
```

---

## Task 4: Antigravity tool implementation

**Files:**
- Create: `pkg/tools/antigravity/antigravity.go`
- Create: `pkg/tools/antigravity/antigravity_test.go`
- Modify: `pkg/settings/settings.go`

**Interfaces:**
- Consumes: `runner.Tool`, `runner.FlagDef`, `runner.HelpSection`, `runner.StdinCloser` (Task 3).
- Produces: `antigravity.New() *antigravity.Tool`; settings constants `settings.DefaultAntigravityModel`, `settings.DefaultAgyPrintTimeout`; `settings.AntigravityDefaults{Model, Effort, PrintTimeout string}` reachable at `settings.Defaults.Antigravity`. Tasks 5-7 consume `antigravity.New()`.

- [ ] **Step 1: Add settings support**

In `pkg/settings/settings.go`, add to the `const` block (after `DefaultKiloCodeProvider` on line 24):

```go
	DefaultAntigravityModel = "Gemini 3.1 Pro (High)"
	DefaultAgyPrintTimeout  = "24h"
```

Add the struct after `KiloCodeDefaults` (line ~62):

```go
// AntigravityDefaults holds default settings for ragy (Antigravity CLI).
type AntigravityDefaults struct {
	Model        string `json:"model,omitempty"`         // Display name or slug, e.g. "Gemini 3.1 Pro (High)"
	Effort       string `json:"effort,omitempty"`        // low, medium, high
	PrintTimeout string `json:"print_timeout,omitempty"` // agy --print-timeout (agy default 5m is too short)
}
```

Add the field to the `Defaults` struct:

```go
	Antigravity AntigravityDefaults `json:"antigravity,omitempty"`
```

In both default-construction blocks (near lines 246 and 671) add alongside the `KiloCode:` entry:

```go
			Antigravity: AntigravityDefaults{
				Model:        DefaultAntigravityModel,
				PrintTimeout: DefaultAgyPrintTimeout,
			},
```

And in the fallback-normalization block (near line 291) add:

```go
	if settings.Defaults.Antigravity.Model == "" {
		settings.Defaults.Antigravity.Model = DefaultAntigravityModel
	}
	if settings.Defaults.Antigravity.PrintTimeout == "" {
		settings.Defaults.Antigravity.PrintTimeout = DefaultAgyPrintTimeout
	}
```

- [ ] **Step 2: Write the failing test**

Create `pkg/tools/antigravity/antigravity_test.go`:

```go
package antigravity

import (
	"strings"
	"testing"

	"rcodegen/pkg/runner"
)

// argsOf joins a command's args (minus argv[0]) for substring assertions.
func argsOf(parts []string) string {
	return strings.Join(parts, " ")
}

func TestBuildCommandUsesPrintAndStreamJSON(t *testing.T) {
	tool := New()
	cfg := &runner.Config{Model: "Gemini 3.1 Pro (High)", Task: "audit this"}
	cmd := tool.BuildCommand(cfg, "/tmp/work", "audit this")

	if !strings.HasSuffix(cmd.Path, "agy") {
		t.Errorf("binary = %q, want agy", cmd.Path)
	}
	joined := argsOf(cmd.Args)
	for _, want := range []string{"-p", "audit this", "--output-format", "stream-json", "--dangerously-skip-permissions"} {
		if !strings.Contains(joined, want) {
			t.Errorf("args missing %q: %s", want, joined)
		}
	}
	if cmd.Dir != "/tmp/work" {
		t.Errorf("Dir = %q, want /tmp/work", cmd.Dir)
	}
}

func TestBuildCommandRaisesPrintTimeout(t *testing.T) {
	tool := New()
	cmd := tool.BuildCommand(&runner.Config{Model: "x"}, "", "t")

	joined := argsOf(cmd.Args)
	if !strings.Contains(joined, "--print-timeout") {
		t.Fatalf("args missing --print-timeout (agy default 5m truncates long runs): %s", joined)
	}
	if strings.Contains(joined, "--print-timeout 5m") {
		t.Error("--print-timeout must be raised above agy's 5m default")
	}
}

func TestBuildCommandResumesWithConversationFlag(t *testing.T) {
	tool := New()
	cmd := tool.BuildCommand(&runner.Config{Model: "x", SessionID: "abc-123"}, "", "t")

	joined := argsOf(cmd.Args)
	if !strings.Contains(joined, "--conversation abc-123") {
		t.Errorf("args missing --conversation abc-123: %s", joined)
	}
	if strings.Contains(joined, "--resume") {
		t.Error("--resume is the dead gemini CLI flag; agy uses --conversation")
	}
}

func TestBuildCommandPassesEffortOnlyWhenSet(t *testing.T) {
	tool := New()
	without := argsOf(tool.BuildCommand(&runner.Config{Model: "x"}, "", "t").Args)
	if strings.Contains(without, "--effort") {
		t.Errorf("--effort should be omitted when unset: %s", without)
	}

	with := argsOf(tool.BuildCommand(&runner.Config{Model: "x", Effort: "high"}, "", "t").Args)
	if !strings.Contains(with, "--effort high") {
		t.Errorf("args missing --effort high: %s", with)
	}
}

func TestPrepareForExecutionResolvesAliases(t *testing.T) {
	cases := map[string]string{
		"pro":    "Gemini 3.1 Pro (High)",
		"flash":  "Gemini 3.6 Flash (Medium)",
		"sonnet": "Claude Sonnet 4.6 (Thinking)",
		"opus":   "Claude Opus 4.6 (Thinking)",
	}
	for alias, want := range cases {
		tool := New()
		cfg := &runner.Config{Model: alias}
		tool.PrepareForExecution(cfg)
		if cfg.Model != want {
			t.Errorf("alias %q resolved to %q, want %q", alias, cfg.Model, want)
		}
	}
}

func TestPrepareForExecutionFlashFlagOverridesModel(t *testing.T) {
	tool := New()
	cfg := &runner.Config{Model: "Gemini 3.1 Pro (High)", Flash: true}
	tool.PrepareForExecution(cfg)

	if cfg.Model != "Gemini 3.6 Flash (Medium)" {
		t.Errorf("Model = %q, want flash model", cfg.Model)
	}
}

func TestPassthroughModelIsNotRewritten(t *testing.T) {
	tool := New()
	cfg := &runner.Config{Model: "gemini-3.5-flash"}
	tool.PrepareForExecution(cfg)

	if cfg.Model != "gemini-3.5-flash" {
		t.Errorf("Model = %q, want slug passed through untouched (agy self-validates)", cfg.Model)
	}
}

func TestWantsClosedStdin(t *testing.T) {
	if !New().WantsClosedStdin() {
		t.Error("agy must run with stdin detached to avoid the non-TTY print-mode hang")
	}
}

func TestUsesStreamOutput(t *testing.T) {
	if !New().UsesStreamOutput() {
		t.Error("agy emits stream-json and must use the stream parser")
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./pkg/tools/antigravity/ -v`
Expected: FAIL — package `antigravity` does not exist.

- [ ] **Step 4: Write the implementation**

Create `pkg/tools/antigravity/antigravity.go`:

```go
// Package antigravity provides the Antigravity CLI (agy) tool implementation
// for the runner framework. agy replaced the Gemini CLI, which Google
// sunset for individual accounts on 2026-06-18.
package antigravity

import (
	"fmt"
	"os/exec"

	"rcodegen/pkg/runner"
	"rcodegen/pkg/settings"
)

// Compile-time interface satisfaction checks
var (
	_ runner.Tool        = (*Tool)(nil)
	_ runner.StdinCloser = (*Tool)(nil)
)

// modelAliases maps short names to agy's canonical display names.
// Slugs like "gemini-3.5-flash" are passed through untouched — agy
// validates models itself and returns a helpful list on mismatch.
var modelAliases = map[string]string{
	"pro":     "Gemini 3.1 Pro (High)",
	"flash":   "Gemini 3.6 Flash (Medium)",
	"sonnet":  "Claude Sonnet 4.6 (Thinking)",
	"opus":    "Claude Opus 4.6 (Thinking)",
	"gpt-oss": "GPT-OSS 120B (Medium)",
}

const flashModel = "Gemini 3.6 Flash (Medium)"

// Tool implements runner.Tool for the Antigravity CLI.
type Tool struct {
	settings *settings.Settings
}

// New creates a new Antigravity tool.
func New() *Tool {
	return &Tool{}
}

// SetSettings sets the settings (called by runner after loading).
func (t *Tool) SetSettings(s *settings.Settings) {
	t.settings = s
}

func (t *Tool) Name() string {
	return "ragy"
}

func (t *Tool) BinaryName() string {
	return "agy"
}

func (t *Tool) ReportDir() string {
	return "_rcodegen"
}

func (t *Tool) ReportPrefix() string {
	return "agy-"
}

// ValidModels returns nil: agy validates models itself and its catalog
// changes server-side, so a hard-coded whitelist would go stale.
func (t *Tool) ValidModels() []string {
	return nil
}

func (t *Tool) DefaultModel() string {
	return settings.DefaultAntigravityModel
}

func (t *Tool) DefaultModelSetting() string {
	if t.settings != nil && t.settings.Defaults.Antigravity.Model != "" {
		return t.settings.Defaults.Antigravity.Model
	}
	return t.DefaultModel()
}

// printTimeout returns the --print-timeout value. agy defaults to 5m,
// which truncates rcodegen's long audit runs.
func (t *Tool) printTimeout() string {
	if t.settings != nil && t.settings.Defaults.Antigravity.PrintTimeout != "" {
		return t.settings.Defaults.Antigravity.PrintTimeout
	}
	return settings.DefaultAgyPrintTimeout
}

// BuildCommand constructs the exec.Cmd for running a task.
func (t *Tool) BuildCommand(cfg *runner.Config, workDir, task string) *exec.Cmd {
	args := []string{
		"-p", task,
		"--output-format", "stream-json",
		"--dangerously-skip-permissions",
		"--print-timeout", t.printTimeout(),
	}

	if cfg.Model != "" {
		args = append(args, "--model", cfg.Model)
	}
	if cfg.Effort != "" {
		args = append(args, "--effort", cfg.Effort)
	}
	if cfg.SessionID != "" {
		args = append(args, "--conversation", cfg.SessionID)
	}

	cmd := exec.Command("agy", args...)
	if workDir != "" {
		cmd.Dir = workDir
	}
	return cmd
}

// WantsClosedStdin detaches stdin: agy's print mode can hang waiting on an
// open stdin in non-TTY contexts (rserve, rbatch).
func (t *Tool) WantsClosedStdin() bool {
	return true
}

// ShowStatus is a no-op: agy exposes no quota endpoint. Per-run token
// usage comes from the stream-json result event instead.
func (t *Tool) ShowStatus() {}

func (t *Tool) SupportsStatusTracking() bool {
	return false
}

func (t *Tool) CaptureStatusBefore() interface{} {
	return nil
}

func (t *Tool) CaptureStatusAfter() interface{} {
	return nil
}

func (t *Tool) PrintStatusSummary(before, after interface{}) {}

func (t *Tool) ToolSpecificFlags() []runner.FlagDef {
	return []runner.FlagDef{
		{
			Long:        "--flash",
			Description: "Use " + flashModel,
			TakesArg:    false,
			Target:      "Flash",
		},
		{
			Short:       "-e",
			Long:        "--effort",
			Description: "Reasoning effort (low, medium, high)",
			TakesArg:    true,
			Target:      "Effort",
		},
	}
}

func (t *Tool) ApplyToolDefaults(cfg *runner.Config) {
	if cfg.Model == "" {
		cfg.Model = t.DefaultModelSetting()
	}
	if cfg.Effort == "" && t.settings != nil {
		cfg.Effort = t.settings.Defaults.Antigravity.Effort
	}
}

// PrepareForExecution resolves aliases and the --flash override.
func (t *Tool) PrepareForExecution(cfg *runner.Config) {
	if cfg.Flash {
		cfg.Model = flashModel
		return
	}
	if resolved, ok := modelAliases[cfg.Model]; ok {
		cfg.Model = resolved
	}
}

// ValidateConfig only checks presence — agy validates the model itself and
// returns the authoritative list of names when it rejects one.
func (t *Tool) ValidateConfig(cfg *runner.Config) error {
	if cfg.Model == "" {
		return fmt.Errorf("model must be specified (e.g. -m %q, or run `agy models`)", settings.DefaultAntigravityModel)
	}
	switch cfg.Effort {
	case "", "low", "medium", "high":
	default:
		return fmt.Errorf("invalid effort %q (want low, medium, or high)", cfg.Effort)
	}
	return nil
}

func (t *Tool) BannerTitle() string {
	return "RAGY"
}

func (t *Tool) BannerSubtitle() string {
	return "Antigravity Code Assistant"
}

func (t *Tool) PrintToolSpecificBannerFields(cfg *runner.Config) {}

func (t *Tool) PrintToolSpecificSummaryFields(cfg *runner.Config) {}

func (t *Tool) SecurityWarning() []string {
	return []string{
		"This tool runs the Antigravity CLI with --dangerously-skip-permissions,",
		"which auto-approves all tool operations.",
		"Use with caution and only on trusted codebases.",
	}
}

func (t *Tool) ToolSpecificHelpSections() []runner.HelpSection {
	return []runner.HelpSection{
		{
			Title: "Antigravity Options",
			Lines: []string{
				"  " + runner.Green + "--flash" + runner.Reset + "            Use " + flashModel,
				fmt.Sprintf("  %s-e%s, %s--effort%s <level>  Reasoning effort (low, medium, high)",
					runner.Green, runner.Reset, runner.Green, runner.Reset),
				"  Aliases: " + runner.Yellow + "pro, flash, sonnet, opus, gpt-oss" + runner.Reset,
				"  Run " + runner.Green + "agy models" + runner.Reset + " for the full model list.",
			},
		},
	}
}

func (t *Tool) StatsJSONFields(cfg *runner.Config) map[string]interface{} {
	return map[string]interface{}{
		"model":  cfg.Model,
		"effort": cfg.Effort,
	}
}

// UsesStreamOutput returns true — agy emits stream-json NDJSON.
func (t *Tool) UsesStreamOutput() bool {
	return true
}

func (t *Tool) RunLogFields(cfg *runner.Config) []string {
	fields := []string{"Model: " + cfg.Model}
	if cfg.Effort != "" {
		fields = append(fields, "Effort: "+cfg.Effort)
	}
	return fields
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./pkg/tools/antigravity/ ./pkg/settings/ -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add pkg/tools/antigravity/ pkg/settings/settings.go
git commit -m "feat(antigravity): add agy tool implementation with settings defaults"
```

---

## Task 5: ragy binary, Makefile, launcher

**Files:**
- Create: `cmd/ragy/main.go`
- Create: `scripts/launcher-ragy.sh`
- Modify: `Makefile:8`

**Interfaces:**
- Consumes: `antigravity.New()` from Task 4.
- Produces: a `bin/ragy` executable.

- [ ] **Step 1: Create the entrypoint**

Create `cmd/ragy/main.go`:

```go
package main

import (
	"fmt"
	"os"

	rcodegenpkg "rcodegen"
	"rcodegen/pkg/runner"
	"rcodegen/pkg/tools/antigravity"

	chassis "github.com/ai8future/chassis-go/v11"
	"github.com/ai8future/chassis-go/v11/logz"
	"github.com/ai8future/chassis-go/v11/registry"
)

func main() {
	chassis.SetAppVersion(rcodegenpkg.AppVersion)
	chassis.RequireMajor(11)
	logger := logz.New("info")
	if err := registry.InitCLI(chassis.Version); err != nil {
		logger.Error("registry init failed", "error", err)
		os.Exit(1)
	}
	tool := antigravity.New()
	r := runner.NewRunner(tool)
	result := r.Run()
	if result.Error != nil {
		fmt.Fprintln(os.Stderr, result.Error)
	}
	registry.ShutdownCLI(result.ExitCode)
	os.Exit(result.ExitCode)
}
```

- [ ] **Step 2: Create the launcher script**

Create `scripts/launcher-ragy.sh`:

```bash
#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"

case "$ARCH" in
  x86_64)  ARCH="amd64" ;;
  aarch64) ARCH="arm64" ;;
esac

BINARY="${SCRIPT_DIR}/ragy-${OS}-${ARCH}"

if [ ! -f "$BINARY" ]; then
  echo "error: no binary for ${OS}/${ARCH} (expected ${BINARY})" >&2
  exit 1
fi

exec "$BINARY" "$@"
```

Then: `chmod +x scripts/launcher-ragy.sh`

- [ ] **Step 3: Register in the Makefile**

In `Makefile` line 8, change:

```make
BINS := rclaude rcodex rgemini ropencode rkilo rcodegen rserve rbatch
```

to:

```make
BINS := rclaude rcodex rgemini ragy ropencode rkilo rcodegen rserve rbatch
```

- [ ] **Step 4: Build and smoke-test**

```bash
make ragy && ./bin/ragy --help
```

Expected: help output showing `RAGY` / `Antigravity Code Assistant` and the Antigravity Options section.

- [ ] **Step 5: Commit**

```bash
git add cmd/ragy/ scripts/launcher-ragy.sh Makefile
git commit -m "feat(ragy): add ragy binary, launcher, and Makefile target"
```

---

## Task 6: Register agy across orchestrator, batch, and server

**Files:**
- Modify: `pkg/orchestrator/orchestrator.go:92`, `pkg/orchestrator/progress.go:131`
- Modify: `pkg/batch/manifest.go:17`, `pkg/batch/executor_local.go:39`
- Modify: `cmd/rserve/main.go:76`, `cmd/rbatch/main.go:669`

**Interfaces:**
- Consumes: `antigravity.New()` from Task 4.
- Produces: registry key `"agy"` usable in bundles and batch manifests.

- [ ] **Step 1: Write the failing test**

Add to `pkg/batch/manifest_test.go`:

```go
func TestAgyIsAValidTool(t *testing.T) {
	if !validTools["agy"] {
		t.Error("agy must be accepted as a batch job tool")
	}
}
```

Add to `pkg/orchestrator/orchestrator_test.go`:

```go
func TestAgyRegisteredInToolRegistry(t *testing.T) {
	o := New(nil)
	if _, ok := o.tools["agy"]; !ok {
		t.Error("agy missing from orchestrator tool registry")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/batch/ ./pkg/orchestrator/ -run Agy -v`
Expected: FAIL — `agy` not registered.

- [ ] **Step 3: Add the registry entries**

In `pkg/orchestrator/orchestrator.go` (line 92 block), add `"agy": antigravity.New(),` to the `tools` map and add the `"rcodegen/pkg/tools/antigravity"` import.

In `pkg/orchestrator/progress.go` `toolColor` (line ~131), add before `case "parallel":`:

```go
	case "agy":
		return colorGreen
```

In `pkg/batch/manifest.go` `validTools` (line 17 block), add:

```go
	"agy":      true,
```

In `pkg/batch/executor_local.go` `ToolFactories` (line 39 block), add `"agy": func() runner.Tool { return antigravity.New() },` plus the import. Update the doc comment on `NewLocalExecutor` to include agy.

In `cmd/rserve/main.go` `toolFactories` (line 76 block), add `"agy": func() runner.Tool { return antigravity.New() },` plus the import. Update the file header comment on line 1 to mention `ragy`.

In `cmd/rbatch/main.go` line 669, change the slice to:

```go
strings.Join([]string{"claude", "codex", "gemini", "agy", "opencode", "kilocode"}, ", "),
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go build ./... && go test ./pkg/batch/ ./pkg/orchestrator/ -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/orchestrator/ pkg/batch/ cmd/rserve/main.go cmd/rbatch/main.go
git commit -m "feat: register agy in orchestrator, batch, and rserve registries"
```

---

## Task 7: Usage and session extraction in pkg/executor

`pkg/executor/tool.go` parses raw stdout independently of the stream parser (used by the bundle dispatcher). It needs an `agy` branch or agy runs will report zero tokens and lose session continuity.

**Files:**
- Modify: `pkg/executor/tool.go:181` (`extractUsage`), `pkg/executor/tool.go:226` (`extractSessionID`)
- Modify: `pkg/executor/tool_test.go`

**Interfaces:**
- Consumes: agy `result` envelope field names verified in Task 1.
- Produces: no new signatures.

- [ ] **Step 1: Write the failing test**

Add to `pkg/executor/tool_test.go`:

```go
func TestExtractUsageAgy(t *testing.T) {
	stdout := `{"event":"init","conversation_id":"abc-123"}
{"event":"result","result":{"conversation_id":"abc-123","status":"SUCCESS","response":"ok","usage":{"input_tokens":17422,"output_tokens":295,"thinking_tokens":290,"cache_read_tokens":7,"total_tokens":17717}}}`

	usage := extractUsage("agy", stdout, "")
	if usage.InputTokens != 17422 {
		t.Errorf("InputTokens = %d, want 17422", usage.InputTokens)
	}
	if usage.OutputTokens != 295 {
		t.Errorf("OutputTokens = %d, want 295", usage.OutputTokens)
	}
	if usage.CacheReadTokens != 7 {
		t.Errorf("CacheReadTokens = %d, want 7", usage.CacheReadTokens)
	}
}

func TestExtractSessionIDAgy(t *testing.T) {
	stdout := `{"event":"init","conversation_id":"abc-123","init":{"cwd":"/tmp"}}`
	if got := extractSessionID("agy", stdout, ""); got != "abc-123" {
		t.Errorf("session ID = %q, want abc-123", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/executor/ -run Agy -v`
Expected: FAIL — zero usage and empty session ID.

- [ ] **Step 3: Add the agy branch to extractUsage**

In `pkg/executor/tool.go`, add this case to the `extractUsage` switch, after the existing `case "gemini":` block:

```go
	case "agy":
		// agy emits {"event":"result","result":{…,"usage":{…}}} as the last line.
		lines := strings.Split(stdout, "\n")
		for i := len(lines) - 1; i >= 0; i-- {
			line := strings.TrimSpace(lines[i])
			if line == "" {
				continue
			}
			var obj map[string]interface{}
			if err := json.Unmarshal([]byte(line), &obj); err != nil {
				continue
			}
			if ev, _ := obj["event"].(string); ev != "result" {
				continue
			}
			result, ok := obj["result"].(map[string]interface{})
			if !ok {
				continue
			}
			u, ok := result["usage"].(map[string]interface{})
			if !ok {
				return usage
			}
			if v, ok := u["input_tokens"].(float64); ok {
				usage.InputTokens = int(v)
			}
			if v, ok := u["output_tokens"].(float64); ok {
				usage.OutputTokens = int(v)
			}
			if v, ok := u["cache_read_tokens"].(float64); ok {
				usage.CacheReadTokens = int(v)
			}
			// Gemini 3 pricing (estimates), matching the gemini branch above.
			usage.CostUSD = float64(usage.InputTokens)*0.0000005 + float64(usage.OutputTokens)*0.0000015
			return usage
		}
```

- [ ] **Step 4: Add the agy branch to extractSessionID**

In `extractSessionID`, add a new case after the existing `case "claude", "gemini":` block:

```go
	case "agy":
		// agy carries conversation_id on the init envelope.
		for _, line := range strings.Split(stdout, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var obj map[string]interface{}
			if err := json.Unmarshal([]byte(line), &obj); err != nil {
				continue
			}
			if id, ok := obj["conversation_id"].(string); ok && id != "" {
				return id
			}
		}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./pkg/executor/ -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add pkg/executor/
git commit -m "feat(executor): extract agy token usage and conversation ID"
```

---

## Task 8: End-to-end verification, docs, release

**Files:**
- Modify: `README.md`, `API.md`, `PRODUCT.md`, `AGENTS.md`, `settings.json.example`
- Modify: `VERSION`, `CHANGELOG.md`

**Interfaces:**
- Consumes: everything above.

- [ ] **Step 1: Full build and test**

```bash
go mod vendor 2>/dev/null || true
make && make test
```

Expected: all 9 binaries build; full suite passes.

- [ ] **Step 2: Real end-to-end run against agy**

```bash
cd "$(mktemp -d)" && git init -q .
/Users/cliff/Desktop/_code/codegen_suite/rcodegen/bin/ragy "Reply with exactly: pong"
```

Expected: the `⚡ Antigravity initialized` line, `pong` streamed as one line (not one line per token), a normal summary, and exit 0. Confirm a `_rcodegen/agy-*.md` report was written.

- [ ] **Step 3: Verify session resume**

Run `ragy` a second time in the same directory with a follow-up prompt and confirm from `--verbose` output that `--conversation <id>` was passed with the ID captured from the first run.

- [ ] **Step 4: Update documentation**

- `README.md`: add `ragy` to the binary list/table alongside `rgemini`; note that `rgemini` targets the sunset Gemini CLI and `ragy` is the supported path for individual accounts.
- `API.md`: document the `agy` tool key for rserve/rbatch.
- `PRODUCT.md`: add ragy to the tool lineup.
- `AGENTS.md`: add `make ragy` to the build list.
- `settings.json.example`: add the `antigravity` defaults block:

```json
    "antigravity": {
      "model": "Gemini 3.1 Pro (High)",
      "effort": "",
      "print_timeout": "24h"
    }
```

- [ ] **Step 5: Write the bug note**

Create `_bugs_fixed/2026-07-30-gemini-cli-sunset-ineligible-tier-bug.md` documenting: the `IneligibleTierError` exit-55 failure, the June 18 2026 sunset, that `rgemini` was silently dead, and that `ragy` is the replacement path.

- [ ] **Step 6: Bump VERSION and CHANGELOG**

Only now read `VERSION`, increment it (roll the minor if the revision has reached 15), and add a CHANGELOG entry describing agy support, the new `ragy` binary, and the stream parser addition.

- [ ] **Step 7: Final build and commit**

```bash
make
git add -A
git commit -m "vX.Y.Z: add ragy binary wrapping Antigravity CLI (agy)

Google sunset the Gemini CLI for individual accounts on 2026-06-18;
rgemini now fails with IneligibleTierError. ragy wraps the replacement
agy CLI with native stream-json parsing, conversation resume, and
token accounting.

Agent: Claude:Opus 5 (1M context)"
git push
```

---

## Open Questions

1. **Should `rgemini` print a deprecation hint?** Not included above to keep `pkg/tools/gemini/` untouched per the chosen approach. A one-line stderr note pointing at `ragy` when `IneligibleTierError` appears in output would be cheap and kind — worth a follow-up.
2. **`--mode accept-edits`** was not added. `--dangerously-skip-permissions` already sets `permission_mode: always-proceed` (confirmed in the captured init event), so it appears redundant. Revisit if agy soft-denies shell commands in practice.
3. **Image support.** `rgemini`'s `-i/--image` and `banana` alias run through `image_api.go` via `DirectAPIRunner`, never the CLI, so they are unaffected and were deliberately not ported. agy exposes a `generate_image` tool internally, which is a different mechanism.
