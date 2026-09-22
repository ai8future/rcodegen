# TypeSafe Jev Decisions in rserve and Bundles

**Date:** September 22, 2026

**Status:** Implemented in version 4.4.3 by Codex:GPT-6. The task checklist below is retained as the implementation recipe; completion is established by the code and verification recorded here.

**Verification:** `go vet ./...`, `make test` (race detector and coverage), full Linux amd64 / Darwin arm64 `make` builds, all eight launcher version checks outside the repository, and a binary smoke test using a local fake Jev server passed. The fake-server smoke covered authentication, typed results, usage/cost, correlation, permanent error retryability, null probabilities, bundle discovery, and health counters. Live vendor verification was skipped because no `TYPESAFE_API_KEY` was available. `make lint` could not run because `golangci-lint` is not installed; changed Go files pass `gofmt` and static analysis.

## Summary

Add TypeSafe's Jev decision model to rcodegen at two points: a `POST /v1/decisions` endpoint on rserve so any client in the suite can get typed, calibrated answers over HTTP without holding the vendor key, and a `decide` step type in the bundle engine so workflows can gate and branch on those answers with the existing `if` grammar. Both share one small Go client in `pkg/jev`. No dispatch dependency, no new binary, no change to the tool interface.

---

# TypeSafe Jev Decisions Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Expose TypeSafe Jev typed decisions through rserve's HTTP API and through a bundle `decide` step, backed by one shared Go client.

**Architecture:** A new `pkg/jev` package owns the single upstream call (`POST https://api.typesafe.ai/v1/systemone`), question validation, state scrubbing and capping, retries, and cost math. `pkg/server/openai/decisions.go` adds the HTTP route beside `/v1/bundles`, with its own concurrency budget and health counters. `pkg/bundle` gains a `DecideDef` on `Step` plus validation, `pkg/executor` gains a `DecideExecutor` wired into the dispatcher, and the orchestrator learns to label decide steps as `jev` for display, events, and cost grouping.

**Tech Stack:** Go 1.26.5 stdlib, chassis-go v11 (already vendored), TypeSafe System One HTTP API. No new dependencies.

**Spec:** The Design section of this document. Executors read both the Design and the tasks.

## Global Constraints

- Go `1.26.5` per `go.mod`; chassis-go `v11` via local replace; run `go mod vendor` before building if `vendor/` is stale.
- Build with `make` only, never bare `go build`, so `-v` reports the embedded version.
- Do not read `VERSION` until the final task. Bump it last. When the revision would exceed 15, bump the minor version instead.
- Commit only after `VERSION` and `CHANGELOG.md` are updated, then `git add -A`, commit, push. Intermediate tasks end with passing tests, not commits.
- CHANGELOG entry names the agent, for example `Agent: Codex:GPT-6`.
- Every state string sent upstream passes through `jev.ScrubState` first. The scrubber masks the home directory path (plain and JSON-escaped forms) and the whole-word account name. It is a narrow guard, not a guarantee: bundle authors and API callers must not put identifying text into questions or state, and reports that embed other identifiers are their responsibility.
- The vendor key is resolved at client construction from the `TYPESAFE_API_KEY` environment variable first and `defaults.jev.api_key` second. The environment value is never copied into the settings struct, because `LoadOrSetup` at `pkg/settings/settings.go:798` writes the whole struct to disk when no settings file exists. The key is never accepted from a client request and never appears in an error message, log, or output file.
- Pin the model to `jev-1.13.0` by default. `jev-latest` is allowed only as an explicit override.
- No response caching or replay in v1: TypeSafe does not document that Jev is deterministic.
- Stay out of `_studies`, `_proposals`, `_rcodegen`, `_bugs_open`, `_bugs_fixed` except to write this file's successors.

## Review Focus

Inputs the spec implies but a reader could still be bitten by. Each has a test pinned to the owning task.

1. **State that carries the home directory in a nested or escaped form**, such as `file:///Users/name/x`, `\/Users\/name` inside embedded JSON, or the bare account name. Task 2 tests `ScrubState` on plain, URL, JSON-escaped, and bare-username forms.
2. **Upstream answers that are present but unusable**: a missing question, a type that does not match the question, a choice outside the supplied options, a noul or probability outside 0 to 1, a score outside the level range, or an empty `answers` object. The client must reject these before the endpoint or a bundle treats them as a decision. Task 2 tests `ValidateResponse`; Task 5 tests the executor surfaces the failure.
3. **A noul answer compared as a number in an `if` condition.** `${steps.gate.result.done} >= 0.6` must parse. Task 5 tests `EvaluateCondition` against a real envelope from the executor.
4. **State larger than the budget.** The objective sits at the start and the completion summary at the end, so the cap must keep both head and tail, land under the limit including its marker, and leave room for the longest question. Task 2 tests `CapState` and `StateBudget`.
5. **All decision slots busy.** A caller must get a fast retryable 503, not a hang behind a slow upstream. Task 3 tests a one-slot gate with a blocked upstream.
6. **A decide step placed inside `parallel`.** The parallel executor turns a child failure into `partial`, and the orchestrator stops only on `failure`, so a gate inside a parallel group could not fail closed. Task 4 rejects it at load time.
7. **A question named like a reserved result key** (`cost_usd`, `model`, `stdout`, or anything ending in `_confidence` or `_probabilities`) would overwrite accounting or collide with derived keys. Task 4 rejects those names at load time.
8. **The env key landing on disk.** `LoadOrSetup` writes settings when none exist. Task 1 tests that the auto-created file never contains `TYPESAFE_API_KEY`'s value.
9. **A developer's real key reaching a test.** `ResolveAPIKey` prefers the environment, so every fake-upstream fixture clears `TYPESAFE_API_KEY`, and no assertion prints an `Authorization` header. Tasks 2, 3, and 5.
10. **A `null` score or a choice with no distribution.** Go leaves a `float64` untouched on JSON `null`, so score parsing goes through a pointer, and choice and score answers must carry `probabilities` and `confidence`. Task 2 tests both.
11. **`decide` on a step that also has `then`.** The orchestrator runs the chosen branch and skips the container's own body. Task 4 rejects the combination.
12. **Null probability entries.** `jev.ProbabilityMap` rejects JSON null values before they can become numeric zeroes. Test both choice and score replies through the client.
13. **Permanent upstream failures.** HTTP preserves the client retryability classification: 401/403 map to `decision_upstream_auth`, other non-retryable failures to `decision_upstream_rejected`.
14. **Negative fixtures must have one defect.** The missing-question fixture supplies every expected probability key for its remaining answer.
15. **Cancellation during backoff and oversized trailing whitespace.** Tests synchronize with an entered backoff wait, and both decoder calls map body-limit errors to 413.

---

## Design

### What Jev is

Jev is TypeSafe AI's "System One" model. It does not generate text. One endpoint, `POST https://api.typesafe.ai/v1/systemone`, takes `state` (text) plus a map of typed `questions` and returns typed `answers` with probabilities and a confidence value.

| Question type | Returns | Criteria shape |
|---|---|---|
| `noul` | probability 0 to 1 that the statement is true | optional `{"true": "...", "false": "..."}` |
| `choice` | one option name, plus `probabilities` and `confidence` | object of option name to description, 2 to 255 keys |
| `score` | a fractional position from 0 to the top level index (probability-weighted, for example `1.43`), plus `legend` (level index to text), `probabilities` keyed by level index, `confidence` | array of 2 to 10 level descriptions |

| Limit | Value |
|---|---|
| State size | 32k tokens for state plus the longest question; 64k per request |
| Price | $0.042 per million input tokens; output tokens free |
| Rate limit | 1,200 requests per minute, "adjusting dynamically" |
| Latency | 70 to 500 ms |
| Errors | 401 bad key, 422 validation, 429 rate limit, 529 overloaded |
| Documented weaknesses | literal reading, no counting or date math, not robust to instructions inside state |

Sources: `https://docs.typesafe.ai/api.md`, `https://docs.typesafe.ai/models.md`, `https://docs.typesafe.ai/model-jaggedness/jev-1.13.md`.

### Why rserve and bundles, not a wrapper binary

rcodegen's wrapper binaries run an agentic CLI for minutes and produce a report. Jev cannot do that, so it is not a `runner.Tool`. rserve is the suite's gateway, and its `/v1/bundles` route already proves the pattern of an extension endpoint beside the OpenAI shim. The bundle engine already has `if`/`then`/`else` and a resolver for `${steps.x.result.key}`, so a decision step only needs to write result keys.

### HTTP contract: `POST /v1/decisions`

Request body (512 KiB cap):

```json
{
  "state": "OBJECTIVE: ...\n\nREPORT: ...",
  "questions": {
    "verdict": {
      "type": "choice",
      "instructions": "Compare the report against the objective.",
      "criteria": {"complete": "All requirements built and verified.", "incomplete": "Something is missing."}
    },
    "has_gaps": {"type": "noul", "instructions": "Does the report list known gaps?"}
  },
  "model": "jev-1.13.0"
}
```

`model` is optional; when present it must be the configured model or start with `jev-`.

Response body:

```json
{
  "object": "decision",
  "model": "jev-1.13.0",
  "answers": {
    "verdict": {"type": "choice", "choice": "incomplete", "probabilities": {"complete": 0.21, "incomplete": 0.79}, "confidence": 0.58},
    "has_gaps": {"type": "noul", "noul": 0.93}
  },
  "usage": {"prompt_tokens": 412, "completion_tokens": 0, "total_tokens": 412},
  "cost_usd": 0.0000173,
  "state_bytes": 1532,
  "latency_ms": 143,
  "correlation_id": "wm-job-123"
}
```

Errors use the package's existing `{error: {message, type, code, retryable}}` shape. New codes:

| Code | HTTP | Retryable | When |
|---|---|---|---|
| `invalid_decision` | 400 | no | empty state, bad questions, upstream 422 |
| `state_too_large` | 413 | no | state exceeds `jev.StateBudget(questions)` after scrubbing (64 KiB minus the longest question), or the request body exceeds 512 KiB |
| `decisions_unconfigured` | 503 | no | no API key |
| `decision_capacity` | 503 | yes | all decision slots busy |
| `decision_upstream` | 503 | yes | upstream 429, 529, network error after retries |
| `decision_upstream_invalid` | 502 | yes | upstream replied 200 but the answers failed validation (missing, mistyped, out of range, or outside the options) |
| `decision_upstream_auth` | 502 | no | upstream 401 or 403 |
| `decision_upstream_rejected` | 502 | no | another upstream failure classified as non-retryable |

`RSERVE_TOKEN` bearer auth applies exactly as it does to every route except `/health`. The endpoint never touches the run registry: decisions have their own semaphore (`max_concurrent`, default 8) so they never queue behind multi-minute CLI runs.

`/health` gains a `decisions` object: `configured`, `model`, `total`, `failed`, `in_flight`, `max_concurrent`, `cost_usd`.

### Bundle contract: the `decide` step

```json
{
  "name": "gate",
  "decide": {
    "state": "OBJECTIVE:\n${inputs.task}\n\nBUILDER REPORT:\n${steps.build.stdout}",
    "questions": {
      "verdict": {"type": "choice", "instructions": "...", "criteria": {"complete": "...", "verify_more": "...", "incomplete": "..."}},
      "has_known_gaps": {"type": "noul", "instructions": "..."}
    },
    "model": "jev-1.13.0"
  }
}
```

`state` is resolved with the normal `${...}` resolver, scrubbed, then capped to `jev.StateBudget(questions)`. The cap keeps the head and the tail with a marker between them, because the objective sits at the start of a typical state and the conclusion at the end; a flag records truncation. Result keys written to the envelope:

| Key | Type | Value |
|---|---|---|
| `<question>` | string | `noul` and `score` formatted `%.4f`; `choice` option name |
| `<question>_confidence` | string | `%.4f` when the answer carries one |
| `<question>_probabilities` | map | as returned |
| `stdout` | string | compact JSON `{"answers": {question: value}}`, so `StepOutput`, `/v1/bundles` responses, and `${steps.gate.stdout}` show the decision; the fixed wrapper key keeps the streaming-result parser from ever mistaking it for a `type: result` record |
| `model`, `cost_usd`, `input_tokens`, `output_tokens` | string, float64, int, int | feed existing cost totals and events |
| `state_truncated` | bool | true when the cap applied |

There is no synthesized `decision` key. Conditions name the question: `"if": "${steps.gate.result.verdict} != 'complete'"` or `"if": "${steps.gate.result.has_known_gaps} >= 0.5"`.

Load-time validation enforces the contract. A step may not combine `decide` with `tool`, `parallel`, `merge`, `vote`, or `then`/`else` (a branch container never runs its own body; put the decide step inside a branch). A decide step may not appear anywhere inside a `parallel` group, because the parallel executor reports a failed child as `partial` and the orchestrator continues past `partial`; a gate that cannot stop the bundle is not a gate. Question names must match `^[A-Za-z][A-Za-z0-9_-]{0,63}$` (no dots, since the resolver reads one component after `result`), may not be a reserved result key (`stdout`, `stderr`, `model`, `cost_usd`, `total_cost_usd`, `input_tokens`, `output_tokens`, `cache_read_tokens`, `cache_write_tokens`, `output_length`, `output_truncated`, `state_truncated`, `steps`, `completed`, `merged`, `decision`, `votes`), and may not end in `_confidence` or `_probabilities`.

A decide step that cannot reach Jev, is unconfigured, or receives answers that fail `jev.ValidateResponse` produces a failure envelope, and the orchestrator stops the bundle exactly as it does for any failed step at `pkg/orchestrator/orchestrator.go:489`. Fail closed is the right default for a gate.

The judge's reliability on real reports is unmeasured. The demonstration bundle branches on the builder's own account of its work and does not judge again after remediation. Treat it as completion triage until a measurement exists (see Out of scope).

For display, events, final-report cost grouping, and `StepStats`, a decide step is labeled `jev`, with the model taken from the step or `defaults.jev.model`.

### Settings

`~/.rcodegen/settings.json` gains:

```json
"jev": {
  "base_url": "https://api.typesafe.ai",
  "model": "jev-1.13.0",
  "timeout_seconds": 30,
  "max_concurrent": 8,
  "api_key": ""
}
```

Environment overrides: `RCODEGEN_JEV_MODEL` and `RCODEGEN_JEV_BASE_URL` are applied into the struct like the other `RCODEGEN_*` overrides. `TYPESAFE_API_KEY` is different: it is read by `JevDefaults.ResolveAPIKey()` at client construction and wins over `api_key`, but is never written into the struct, so the auto-created settings file cannot capture it. `RCODEGEN_MODEL` must not fan out into the Jev model, matching how Ollama and LM Studio are protected.

Note for a follow-up outside this plan: `RCODEGEN_OLLAMA_API_KEY` and `RCODEGEN_LMSTUDIO_API_KEY` already have the persistence problem described above, since `applyEnvOverrides` copies them into the struct before `LoadOrSetup` writes it.

### Out of scope for v1

- gRPC `Decide` RPC. The HTTP route covers Windmill and services; add the RPC when a gRPC client needs it.
- Named question presets (a `completion` preset and so on). Callers send explicit questions.
- Caching, replay, and idempotency keys.
- OTel spans and Kafka events for decisions. Note as a follow-up; the chassis wiring in `cmd/rserve/main.go` is available.
- Changing existing builtin bundles. One new `gated-build` bundle demonstrates the step.
- Measuring the judge. Before any bundle relies on a gate for more than triage, run the gate over a sample of real reports with known outcomes and record agreement. The same run answers whether Jev is deterministic enough for caching.
- Stopping a bundle when a tool step fails inside `parallel`. Today that yields `partial` and the run continues; this plan only keeps decide steps out of parallel groups.

---

## File Map

| Action | Path | Responsibility |
|---|---|---|
| Modify | `pkg/settings/settings.go` | `JevDefaults`, constants, env overrides, defaults, fill |
| Modify | `pkg/settings/settings_test.go` | defaults, fill, env override, no-poison tests |
| Modify | `settings.json.example` | `jev` block |
| Create | `pkg/jev/types.go` | `Question`, `Request`, `Answer`, `Response`, `ValidateQuestions`, `Answer.Value` |
| Create | `pkg/jev/state.go` | `ScrubState`, `CapState` |
| Create | `pkg/jev/client.go` | `Client`, `NewClient`, `Decide`, `APIError`, `CostUSD` |
| Create | `pkg/jev/types_test.go`, `pkg/jev/state_test.go`, `pkg/jev/client_test.go` | unit tests with `httptest` |
| Create | `pkg/server/openai/decisions.go` | route, gate, validation, error mapping, health stats |
| Modify | `pkg/server/openai/errorcodes.go` | eight new codes and their retryability |
| Modify | `pkg/server/openai/types.go` | `HealthResponse.Decisions` |
| Modify | `pkg/server/openai/handler.go` | register route, construct gate, health fields |
| Create | `pkg/server/openai/decisions_test.go` | endpoint tests |
| Modify | `pkg/bundle/bundle.go` | `Decide *DecideDef`, `DecideDef`, `Step.ToolLabel()` |
| Create | `pkg/bundle/validate.go` | `Bundle.Validate()` |
| Modify | `pkg/bundle/loader.go` | call `Validate` after both unmarshal sites |
| Create | `pkg/bundle/validate_test.go` | validation and label tests |
| Create | `pkg/executor/decide.go` | `DecideExecutor` |
| Modify | `pkg/executor/dispatcher.go` | wire executor, `SetSettings` |
| Create | `pkg/executor/decide_test.go` | executor tests with fake upstream |
| Modify | `pkg/orchestrator/orchestrator.go` | hand settings to dispatcher, `ToolLabel` in loop, jev model |
| Modify | `pkg/orchestrator/progress.go`, `pkg/orchestrator/live_display.go` | `ToolLabel` in constructors, `jev` color |
| Modify | `pkg/orchestrator/step_events_test.go`, `pkg/orchestrator/orchestrator_test.go` | decide step label and model tests |
| Create | `pkg/bundle/builtin/gated-build.json` | demonstration bundle |
| Modify | `README.md`, `API.md`, `PRODUCT.md` | endpoint, step type, bundle, settings |
| Modify | `CHANGELOG.md`, `VERSION` | final task only |

---

## Pre-flight

- [ ] **Step 0.1: Confirm the tree builds and tests pass before touching anything**

```bash
cd /Users/cliff/Desktop/_code/codegen_suite/rcodegen
git fetch && git status -sb | head -3
go mod vendor
make
go test ./...
```

Expected: `ok` for every package. Note the working tree already carries uncommitted changes from other agents; do not revert them.

- [ ] **Step 0.2: Confirm the API key is available for the optional live check**

```bash
printenv TYPESAFE_API_KEY | wc -c
```

Expected: a nonzero count. If zero, every task still completes with fake upstreams; only the live smoke check in Task 7 is skipped.

---

### Task 1: Settings for Jev

**Files:**
- Modify: `pkg/settings/settings.go:17-28` (constants), `:53-84` (structs), `:96-110` (`EnvOverrides`), `:113-145` (`applyEnvOverrides`), `:284-300` (`GetDefaultSettings`), `:333-345` (`LoadWithFallback`), `:367-374` (fill helper)
- Modify: `pkg/settings/settings_test.go`
- Modify: `settings.json.example`

**Interfaces:**
- Produces: `settings.JevDefaults{BaseURL, Model, TimeoutSeconds, MaxConcurrent, APIKey}` with `(JevDefaults).ResolveAPIKey() string`, `Settings.Defaults.Jev`, constants `DefaultJevBaseURL`, `DefaultJevModel`, `DefaultJevTimeoutSeconds`, `DefaultJevMaxConcurrent`, `JevAPIKeyEnv`, and `fillJevDefaults(*JevDefaults)`.

- [ ] **Step 1.1: Write the failing tests**

Append to `pkg/settings/settings_test.go`:

```go
func TestDefaultSettingsJev(t *testing.T) {
	s := GetDefaultSettings()
	j := s.Defaults.Jev
	if j.BaseURL != DefaultJevBaseURL || j.Model != DefaultJevModel ||
		j.TimeoutSeconds != DefaultJevTimeoutSeconds || j.MaxConcurrent != DefaultJevMaxConcurrent {
		t.Fatalf("jev defaults = %+v", j)
	}
	if j.APIKey != "" {
		t.Fatalf("default settings must not carry an API key, got %q", j.APIKey)
	}
}

func TestFillJevDefaultsPreservesOperatorFields(t *testing.T) {
	j := JevDefaults{Model: "jev-latest", TimeoutSeconds: 5, MaxConcurrent: 2, APIKey: "k"}
	fillJevDefaults(&j)
	if j.BaseURL != DefaultJevBaseURL {
		t.Fatalf("base url not filled: %+v", j)
	}
	if j.Model != "jev-latest" || j.TimeoutSeconds != 5 || j.MaxConcurrent != 2 || j.APIKey != "k" {
		t.Fatalf("operator fields overwritten: %+v", j)
	}
	empty := JevDefaults{}
	fillJevDefaults(&empty)
	if empty.Model != DefaultJevModel || empty.TimeoutSeconds != DefaultJevTimeoutSeconds || empty.MaxConcurrent != DefaultJevMaxConcurrent {
		t.Fatalf("empty not filled: %+v", empty)
	}
}

func TestApplyEnvOverridesJev(t *testing.T) {
	testkit.SetEnv(t, map[string]string{
		"TYPESAFE_API_KEY":      "env-key",
		"RCODEGEN_JEV_MODEL":    "jev-latest",
		"RCODEGEN_JEV_BASE_URL": "https://example.test",
	})
	s := GetDefaultSettings()
	s.Defaults.Jev.APIKey = "file-key"
	applyEnvOverrides(s)
	j := s.Defaults.Jev
	if j.Model != "jev-latest" || j.BaseURL != "https://example.test" {
		t.Fatalf("jev overrides = %+v", j)
	}
	if j.APIKey != "file-key" {
		t.Fatalf("env key must not be copied into the struct, got %q", j.APIKey)
	}
	if got := j.ResolveAPIKey(); got != "env-key" {
		t.Fatalf("ResolveAPIKey = %q, want env value", got)
	}
	testkit.SetEnv(t, map[string]string{"TYPESAFE_API_KEY": ""})
	if got := j.ResolveAPIKey(); got != "file-key" {
		t.Fatalf("ResolveAPIKey without env = %q, want file value", got)
	}
}

func TestLoadOrSetupNeverPersistsEnvKey(t *testing.T) {
	home := t.TempDir()
	testkit.SetEnv(t, map[string]string{"HOME": home, "TYPESAFE_API_KEY": "must-not-land-on-disk"})
	if _, _, err := LoadOrSetup(); err != nil {
		t.Fatalf("LoadOrSetup: %v", err)
	}
	data, err := os.ReadFile(GetConfigPath())
	if err != nil {
		t.Fatalf("settings file not auto-created: %v", err)
	}
	if strings.Contains(string(data), "must-not-land-on-disk") {
		t.Fatalf("env key persisted to %s", GetConfigPath())
	}
}

func TestGlobalModelDoesNotPoisonJev(t *testing.T) {
	testkit.SetEnv(t, map[string]string{"RCODEGEN_MODEL": "global-model"})
	s := GetDefaultSettings()
	applyEnvOverrides(s)
	if s.Defaults.Jev.Model != DefaultJevModel {
		t.Fatalf("global model leaked into jev: %+v", s.Defaults.Jev)
	}
}
```

- [ ] **Step 1.2: Run the tests to confirm they fail**

Run: `go test ./pkg/settings/ -run 'Jev' -v`
Expected: compile error, `Defaults.Jev undefined`. A compile error counts as failing.

- [ ] **Step 1.3: Add constants, struct, and field**

In the `const` block near `settings.go:17`, add:

```go
	DefaultJevBaseURL        = "https://api.typesafe.ai"
	DefaultJevModel          = "jev-1.13.0"
	DefaultJevTimeoutSeconds = 30
	DefaultJevMaxConcurrent  = 8
	JevAPIKeyEnv             = "TYPESAFE_API_KEY"
```

After `LocalAIDefaults` (around `:66-72`), add:

```go
// JevDefaults configures the TypeSafe Jev decision model behind
// POST /v1/decisions and bundle "decide" steps. APIKey is the file-configured
// key only; ResolveAPIKey consults TYPESAFE_API_KEY first. The environment
// value is deliberately never copied into this struct, because LoadOrSetup
// serializes the whole struct to disk when no settings file exists.
type JevDefaults struct {
	BaseURL        string `json:"base_url,omitempty"`
	Model          string `json:"model,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
	MaxConcurrent  int    `json:"max_concurrent,omitempty"`
	APIKey         string `json:"api_key,omitempty"`
}

// ResolveAPIKey returns the key to use: TYPESAFE_API_KEY when set, else the
// file-configured api_key. It never mutates the receiver.
func (j JevDefaults) ResolveAPIKey() string {
	if v := strings.TrimSpace(os.Getenv(JevAPIKeyEnv)); v != "" {
		return v
	}
	return strings.TrimSpace(j.APIKey)
}
```

`settings.go` already imports `os` and `strings`.

In `Defaults` (around `:75-83`), add the last field:

```go
	Jev      JevDefaults      `json:"jev,omitempty"`
```

- [ ] **Step 1.4: Add env overrides**

In `EnvOverrides` (around `:96-110`), add only the two non-secret overrides. `TYPESAFE_API_KEY` is intentionally absent here; it is read by `ResolveAPIKey`.

```go
	JevModel   string `env:"RCODEGEN_JEV_MODEL" required:"false"`
	JevBaseURL string `env:"RCODEGEN_JEV_BASE_URL" required:"false"`
```

At the end of `applyEnvOverrides` (after the LM Studio block), add:

```go
	if env.JevModel != "" {
		s.Defaults.Jev.Model = env.JevModel
	}
	if env.JevBaseURL != "" {
		s.Defaults.Jev.BaseURL = env.JevBaseURL
	}
```

Do not add `Jev` to the `RCODEGEN_MODEL` fan-out block.

- [ ] **Step 1.5: Add defaults and fill**

In `GetDefaultSettings` after the `LMStudio` entry, add:

```go
			Jev: JevDefaults{
				BaseURL:        DefaultJevBaseURL,
				Model:          DefaultJevModel,
				TimeoutSeconds: DefaultJevTimeoutSeconds,
				MaxConcurrent:  DefaultJevMaxConcurrent,
			},
```

In `LoadWithFallback` directly after the two `fillLocalAIDefaults` calls, add:

```go
	fillJevDefaults(&settings.Defaults.Jev)
```

After `fillLocalAIDefaults` (around `:374`), add:

```go
func fillJevDefaults(defaults *JevDefaults) {
	if defaults.BaseURL == "" {
		defaults.BaseURL = DefaultJevBaseURL
	}
	if defaults.Model == "" {
		defaults.Model = DefaultJevModel
	}
	if defaults.TimeoutSeconds <= 0 {
		defaults.TimeoutSeconds = DefaultJevTimeoutSeconds
	}
	if defaults.MaxConcurrent <= 0 {
		defaults.MaxConcurrent = DefaultJevMaxConcurrent
	}
}
```

- [ ] **Step 1.6: Run the tests to confirm they pass**

Run: `go test ./pkg/settings/ -v`
Expected: all PASS, including the pre-existing suite.

- [ ] **Step 1.7: Update `settings.json.example`**

After the `lmstudio` block, add:

```json
    "jev": {
      "base_url": "https://api.typesafe.ai",
      "model": "jev-1.13.0",
      "timeout_seconds": 30,
      "max_concurrent": 8,
      "api_key": ""
    }
```

Keep the JSON valid (comma after the `lmstudio` block). Verify: `/opt/homebrew/bin/python3.13 -m json.tool settings.json.example > /dev/null && echo ok`.

---

### Task 2: The `pkg/jev` client

**Files:**
- Create: `pkg/jev/types.go`, `pkg/jev/state.go`, `pkg/jev/client.go`
- Test: `pkg/jev/types_test.go`, `pkg/jev/state_test.go`, `pkg/jev/client_test.go`

**Interfaces:**
- Consumes: `settings.JevDefaults` from Task 1.
- Produces:
  - `jev.Question{Type, Instructions string; Criteria json.RawMessage}`
  - `jev.Answer{Type string; Noul *float64; Choice string; Score json.RawMessage; Legend map[string]string; Probabilities ProbabilityMap; Confidence *float64}` with `(Answer).Value() string`
  - `jev.Response{Model string; Answers map[string]Answer; Usage Usage}`, `jev.Usage{InputTokens, OutputTokens int}`
  - `jev.ValidateQuestions(map[string]Question) error`, `jev.ValidateResponse(map[string]Question, *Response) error`
  - `jev.ScrubState(state, home, username string) string`, `jev.CapState(state string, maxBytes int) (string, bool)`, `jev.StateBudget(map[string]Question) int`, `jev.MaxStateBytes`
  - `jev.NewClient(settings.JevDefaults) (*Client, error)`, `(*Client).Model() string`, `(*Client).Decide(ctx, model, state string, questions map[string]Question) (*Response, error)`
  - `jev.CostUSD(inputTokens int) float64`, `jev.ErrNotConfigured`, `jev.ErrStateTooLarge`, `jev.ErrInvalidResponse`, `*jev.APIError{Status int; Message string; Retryable bool}`

- [ ] **Step 2.1: Write the failing type and validation tests**

`pkg/jev/types_test.go`:

```go
package jev

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestValidateQuestions(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	_ = f
	cases := []struct {
		name    string
		qs      map[string]Question
		wantErr string
	}{
		{"empty", map[string]Question{}, "at least one"},
		{"bad name", map[string]Question{"has space": {Type: "noul", Instructions: "x"}}, "question name"},
		{"dotted name", map[string]Question{"a.b": {Type: "noul", Instructions: "x"}}, "question name"},
		{"leading digit", map[string]Question{"1st": {Type: "noul", Instructions: "x"}}, "question name"},
		{"bad type", map[string]Question{"q": {Type: "essay", Instructions: "x"}}, "type"},
		{"no instructions", map[string]Question{"q": {Type: "noul"}}, "instructions"},
		{"noul ok", map[string]Question{"q": {Type: "noul", Instructions: "x"}}, ""},
		{"noul bad criteria", map[string]Question{"q": {Type: "noul", Instructions: "x", Criteria: json.RawMessage(`{"maybe":"m"}`)}}, "criteria"},
		{"choice one option", map[string]Question{"q": {Type: "choice", Instructions: "x", Criteria: json.RawMessage(`{"a":"A"}`)}}, "2 to 255"},
		{"choice ok", map[string]Question{"q": {Type: "choice", Instructions: "x", Criteria: json.RawMessage(`{"a":"A","b":"B"}`)}}, ""},
		{"score one level", map[string]Question{"q": {Type: "score", Instructions: "x", Criteria: json.RawMessage(`["low"]`)}}, "2 to 10"},
		{"score ok", map[string]Question{"q": {Type: "score", Instructions: "x", Criteria: json.RawMessage(`["low","high"]`)}}, ""},
		{"one question too large", map[string]Question{"q": {Type: "choice", Instructions: "x", Criteria: json.RawMessage(`{"a":"` + strings.Repeat("d", MaxQuestionBytes) + `","b":"B"}`)}}, "limit is"},
		{"questions too large together", func() map[string]Question {
			qs := map[string]Question{}
			for i := 0; i < 4; i++ {
				qs[fmt.Sprintf("q%d", i)] = Question{Type: "choice", Instructions: "x", Criteria: json.RawMessage(`{"a":"` + strings.Repeat("d", MaxQuestionBytes-64) + `","b":"B"}`)}
			}
			return qs
		}(), "across all questions"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateQuestions(tc.qs)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestAnswerValue(t *testing.T) {
	n := 0.87
	if got := (Answer{Type: "noul", Noul: &n}).Value(); got != "0.8700" {
		t.Fatalf("noul value = %q", got)
	}
	if got := (Answer{Type: "choice", Choice: "incomplete"}).Value(); got != "incomplete" {
		t.Fatalf("choice value = %q", got)
	}
	if got := (Answer{Type: "score", Score: json.RawMessage(`1.43`)}).Value(); got != "1.4300" {
		t.Fatalf("score value = %q", got)
	}
	if got := (Answer{Type: "score", Score: json.RawMessage(`2`)}).Value(); got != "2.0000" {
		t.Fatalf("integer score value = %q", got)
	}
	if got := (Answer{Type: "score", Score: json.RawMessage(`null`)}).Value(); got != "" {
		t.Fatalf("null score must be empty, got %q", got)
	}
	if got := (Answer{Type: "noul"}).Value(); got != "" {
		t.Fatalf("missing noul should be empty, got %q", got)
	}
}

func TestValidateResponse(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	qs := map[string]Question{
		"done":    {Type: TypeNoul, Instructions: "x"},
		"verdict": {Type: TypeChoice, Instructions: "x", Criteria: json.RawMessage(`{"complete":"c","incomplete":"i"}`)},
		"sev":     {Type: TypeScore, Instructions: "x", Criteria: json.RawMessage(`["a","b","c"]`)},
	}
	good := func() *Response {
		return &Response{Model: "jev-1.13.0", Answers: map[string]Answer{
			"done":    {Type: TypeNoul, Noul: f(0.9)},
			"verdict": {Type: TypeChoice, Choice: "incomplete", Probabilities: map[string]float64{"complete": 0.2, "incomplete": 0.8}, Confidence: f(0.6)},
			"sev":     {Type: TypeScore, Score: json.RawMessage(`1.43`), Probabilities: map[string]float64{"0": 0, "1": 0.57, "2": 0.43}, Confidence: f(0.35)},
		}}
	}
	if err := ValidateResponse(qs, good()); err != nil {
		t.Fatalf("good response rejected: %v", err)
	}
	cases := []struct {
		name string
		mut  func(*Response)
		want string
	}{
		{"empty answers", func(r *Response) { r.Answers = map[string]Answer{} }, "no answer"},
		{"missing question", func(r *Response) { delete(r.Answers, "sev") }, `"sev"`},
		{"type mismatch", func(r *Response) { a := r.Answers["done"]; a.Type = TypeChoice; r.Answers["done"] = a }, "type"},
		{"noul missing", func(r *Response) { r.Answers["done"] = Answer{Type: TypeNoul} }, "noul"},
		{"noul out of range", func(r *Response) { r.Answers["done"] = Answer{Type: TypeNoul, Noul: f(1.2)} }, "0 to 1"},
		{"choice empty", func(r *Response) { a := r.Answers["verdict"]; a.Choice = ""; r.Answers["verdict"] = a }, "choice"},
		{"choice outside options", func(r *Response) { a := r.Answers["verdict"]; a.Choice = "maybe"; r.Answers["verdict"] = a }, "not one of"},
		{"probability out of range", func(r *Response) { a := r.Answers["verdict"]; a.Probabilities["complete"] = 7; r.Answers["verdict"] = a }, "probabilit"},
		{"score missing", func(r *Response) { r.Answers["sev"] = Answer{Type: TypeScore} }, "score"},
		{"score null", func(r *Response) { a := r.Answers["sev"]; a.Score = json.RawMessage(`null`); r.Answers["sev"] = a }, "numeric"},
		{"score not numeric", func(r *Response) { a := r.Answers["sev"]; a.Score = json.RawMessage(`"b"`); r.Answers["sev"] = a }, "numeric"},
		{"score above top level", func(r *Response) { a := r.Answers["sev"]; a.Score = json.RawMessage(`2.5`); r.Answers["sev"] = a }, "0 to 2"},
		{"score without probabilities", func(r *Response) { a := r.Answers["sev"]; a.Probabilities = nil; r.Answers["sev"] = a }, "no probabilities"},
		{"choice without confidence", func(r *Response) { a := r.Answers["verdict"]; a.Confidence = nil; r.Answers["verdict"] = a }, "no confidence"},
		{"confidence out of range", func(r *Response) { a := r.Answers["verdict"]; a.Confidence = f(-0.1); r.Answers["verdict"] = a }, "confidence"},
		{"probabilities over unrelated keys", func(r *Response) {
			a := r.Answers["verdict"]
			a.Probabilities = map[string]float64{"unrelated": 1}
			r.Answers["verdict"] = a
		}, "expected 2"},
		{"probabilities missing an option", func(r *Response) {
			a := r.Answers["verdict"]
			a.Probabilities = map[string]float64{"incomplete": 1}
			r.Answers["verdict"] = a
		}, "expected 2"},
		{"probabilities with a wrong key", func(r *Response) {
			a := r.Answers["verdict"]
			a.Probabilities = map[string]float64{"incomplete": 0.5, "maybe": 0.5}
			r.Answers["verdict"] = a
		}, "missing key"},
		{"score probabilities with bad level key", func(r *Response) {
			a := r.Answers["sev"]
			a.Probabilities = map[string]float64{"0": 0.2, "1": 0.3, "5": 0.5}
			r.Answers["sev"] = a
		}, "missing key"},
		{"probabilities not normalized", func(r *Response) {
			a := r.Answers["verdict"]
			a.Probabilities = map[string]float64{"complete": 0.6, "incomplete": 0.6}
			r.Answers["verdict"] = a
		}, "sum to"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := good()
			tc.mut(r)
			err := ValidateResponse(qs, r)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
			if !errors.Is(err, ErrInvalidResponse) {
				t.Fatalf("err must wrap ErrInvalidResponse: %v", err)
			}
		})
	}
	extra := good()
	extra.Answers["unrequested"] = Answer{Type: TypeNoul, Noul: f(0.1)}
	if err := ValidateResponse(qs, extra); err != nil {
		t.Fatalf("extra answers must be tolerated: %v", err)
	}
}
```

- [ ] **Step 2.2: Run to confirm failure**

Run: `go test ./pkg/jev/ -v`
Expected: compile error, package missing.

- [ ] **Step 2.3: Write `pkg/jev/types.go`**

```go
// Package jev is a minimal client for TypeSafe's Jev decision model
// (System One API). It validates questions, scrubs and caps state, performs
// the single upstream POST with retries, and prices the call.
package jev

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Question types accepted by the System One API.
const (
	TypeNoul   = "noul"
	TypeChoice = "choice"
	TypeScore  = "score"
)

// Question is one typed question. Criteria is passed upstream verbatim:
// choice expects an object of option name to description (2 to 255 keys),
// score expects an array of 2 to 10 level descriptions, noul accepts an
// optional object with only "true" and "false" keys.
type Question struct {
	Type         string          `json:"type"`
	Instructions string          `json:"instructions"`
	Criteria     json.RawMessage `json:"criteria,omitempty"`
}

// Request is the upstream request body.
type Request struct {
	Model     string              `json:"model"`
	State     string              `json:"state"`
	Questions map[string]Question `json:"questions"`
}

// ProbabilityMap rejects null entries instead of silently decoding them as zero.
type ProbabilityMap map[string]float64

func (p *ProbabilityMap) UnmarshalJSON(data []byte) error {
	var raw map[string]*float64
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if raw == nil {
		*p = nil
		return nil
	}
	values := make(ProbabilityMap, len(raw))
	for key, value := range raw {
		if value == nil {
			return fmt.Errorf("probability %q must not be null", key)
		}
		values[key] = *value
	}
	*p = values
	return nil
}

// Answer is one upstream answer. Only the fields for the question's type
// are populated.
type Answer struct {
	Type          string             `json:"type"`
	Noul          *float64           `json:"noul,omitempty"`
	Choice        string             `json:"choice,omitempty"`
	Score         json.RawMessage    `json:"score,omitempty"`
	Legend        map[string]string  `json:"legend,omitempty"`
	Probabilities ProbabilityMap `json:"probabilities,omitempty"`
	Confidence    *float64           `json:"confidence,omitempty"`
}

// Usage is the upstream token accounting. Output tokens are free.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// Response is the upstream response body.
type Response struct {
	Model   string            `json:"model"`
	Answers map[string]Answer `json:"answers"`
	Usage   Usage             `json:"usage"`
}

// Value renders the primary answer as a plain string so bundle conditions
// can compare it: noul and score as fixed four-decimal numbers, choice as
// the option name. Missing or non-numeric data yields "".
func (a Answer) Value() string {
	switch a.Type {
	case TypeNoul:
		if a.Noul == nil {
			return ""
		}
		return strconv.FormatFloat(*a.Noul, 'f', 4, 64)
	case TypeChoice:
		return a.Choice
	case TypeScore:
		v, ok := a.scoreValue()
		if !ok {
			return ""
		}
		return strconv.FormatFloat(v, 'f', 4, 64)
	}
	return ""
}

// scoreValue parses the numeric score. The vendor returns a fractional
// position along the level index axis (for example 1.43), never a label.
// A JSON null is "missing": Unmarshal into a pointer leaves it nil, whereas
// Unmarshal into a plain float64 would silently keep zero.
func (a Answer) scoreValue() (float64, bool) {
	var v *float64
	if len(a.Score) == 0 || json.Unmarshal(a.Score, &v) != nil || v == nil {
		return 0, false
	}
	return *v, true
}

const (
	maxQuestions      = 32
	maxChoiceOptions  = 255
	minChoiceOptions  = 2
	minScoreLevels    = 2
	maxScoreLevels    = 10
	maxInstructionLen = 4096
	// MaxQuestionBytes bounds one question's instructions plus criteria, and
	// MaxQuestionsTotalBytes bounds all of them together. With these caps
	// StateBudget can never fall below MaxStateBytes-MaxQuestionBytes, and
	// state plus every question stays well inside the vendor's 64k-token
	// request ceiling.
	MaxQuestionBytes       = 16 << 10
	MaxQuestionsTotalBytes = 48 << 10
)

// questionNamePattern has no dots because the bundle resolver reads exactly
// one path component after "result", and starts with a letter so names
// never look like numbers in conditions.
var questionNamePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)

// ErrInvalidResponse marks an upstream reply that parsed but does not answer
// the questions asked. Callers treat it as a failed decision, never as data.
var ErrInvalidResponse = errors.New("jev: invalid response")

// ValidateResponse checks that every requested question has an answer of the
// right type with values in range: noul in 0 to 1, choice among the supplied
// options, score within 0 to the top level index, probabilities and
// confidence in 0 to 1. Unrequested answers are ignored.
func ValidateResponse(qs map[string]Question, r *Response) error {
	if r == nil || len(r.Answers) == 0 {
		return fmt.Errorf("%w: no answers", ErrInvalidResponse)
	}
	for name, q := range qs {
		a, ok := r.Answers[name]
		if !ok {
			return fmt.Errorf("%w: no answer for question %q", ErrInvalidResponse, name)
		}
		if a.Type != q.Type {
			return fmt.Errorf("%w: question %q asked %s, answer type is %q", ErrInvalidResponse, name, q.Type, a.Type)
		}
		for opt, p := range a.Probabilities {
			if p < 0 || p > 1 {
				return fmt.Errorf("%w: question %q probability for %q is %v, expected 0 to 1", ErrInvalidResponse, name, opt, p)
			}
		}
		if a.Confidence != nil && (*a.Confidence < 0 || *a.Confidence > 1) {
			return fmt.Errorf("%w: question %q confidence %v is outside 0 to 1", ErrInvalidResponse, name, *a.Confidence)
		}
		switch q.Type {
		case TypeNoul:
			if a.Noul == nil {
				return fmt.Errorf("%w: question %q has no noul value", ErrInvalidResponse, name)
			}
			if *a.Noul < 0 || *a.Noul > 1 {
				return fmt.Errorf("%w: question %q noul %v is outside 0 to 1", ErrInvalidResponse, name, *a.Noul)
			}
		case TypeChoice:
			if a.Choice == "" {
				return fmt.Errorf("%w: question %q has an empty choice", ErrInvalidResponse, name)
			}
			var options map[string]string
			_ = json.Unmarshal(q.Criteria, &options)
			if _, ok := options[a.Choice]; !ok {
				return fmt.Errorf("%w: question %q choice %q is not one of the supplied options", ErrInvalidResponse, name, a.Choice)
			}
			if err := requireDistribution(name, a, choiceOptions(q)); err != nil {
				return err
			}
		case TypeScore:
			v, ok := a.scoreValue()
			if !ok {
				return fmt.Errorf("%w: question %q score is missing or not numeric", ErrInvalidResponse, name)
			}
			levels := scoreLevelKeys(q)
			top := float64(len(levels) - 1)
			if v < 0 || v > top {
				return fmt.Errorf("%w: question %q score %v is outside 0 to %v", ErrInvalidResponse, name, v, top)
			}
			if err := requireDistribution(name, a, levels); err != nil {
				return err
			}
		}
	}
	return nil
}

// requireDistribution enforces that choice and score answers carry the
// probabilities and confidence the vendor documents for them, and that the
// probabilities form a distribution over exactly the expected keys: the
// option names for a choice, the level indices "0".."n-1" for a score. A
// bare choice with no distribution, or a distribution over other keys, is
// not a calibrated decision. The sum tolerance absorbs vendor rounding.
func requireDistribution(name string, a Answer, expected []string) error {
	if len(a.Probabilities) == 0 {
		return fmt.Errorf("%w: question %q answer has no probabilities", ErrInvalidResponse, name)
	}
	if a.Confidence == nil {
		return fmt.Errorf("%w: question %q answer has no confidence", ErrInvalidResponse, name)
	}
	if len(a.Probabilities) != len(expected) {
		return fmt.Errorf("%w: question %q probabilities cover %d keys, expected %d", ErrInvalidResponse, name, len(a.Probabilities), len(expected))
	}
	sum := 0.0
	for _, key := range expected {
		p, ok := a.Probabilities[key]
		if !ok {
			return fmt.Errorf("%w: question %q probabilities are missing key %q", ErrInvalidResponse, name, key)
		}
		sum += p
	}
	if sum < 0.98 || sum > 1.02 {
		return fmt.Errorf("%w: question %q probabilities sum to %.3f, expected 1", ErrInvalidResponse, name, sum)
	}
	return nil
}

// choiceOptions and scoreLevelKeys derive the expected probability keys
// from the question's criteria.
func choiceOptions(q Question) []string {
	var options map[string]string
	_ = json.Unmarshal(q.Criteria, &options)
	keys := make([]string, 0, len(options))
	for k := range options {
		keys = append(keys, k)
	}
	return keys
}

func scoreLevelKeys(q Question) []string {
	var levels []json.RawMessage
	_ = json.Unmarshal(q.Criteria, &levels)
	keys := make([]string, len(levels))
	for i := range levels {
		keys[i] = strconv.Itoa(i)
	}
	return keys
}

// ValidateQuestions enforces the upstream schema locally so a bad bundle or
// request fails before any tokens are spent.
func ValidateQuestions(qs map[string]Question) error {
	if len(qs) == 0 {
		return fmt.Errorf("questions: at least one question is required")
	}
	if len(qs) > maxQuestions {
		return fmt.Errorf("questions: at most %d questions per call", maxQuestions)
	}
	total := 0
	for name, q := range qs {
		if !questionNamePattern.MatchString(name) {
			return fmt.Errorf("question name %q: must match %s", name, questionNamePattern)
		}
		if strings.TrimSpace(q.Instructions) == "" {
			return fmt.Errorf("question %q: instructions are required", name)
		}
		if len(q.Instructions) > maxInstructionLen {
			return fmt.Errorf("question %q: instructions exceed %d bytes", name, maxInstructionLen)
		}
		if n := QuestionBytes(q); n > MaxQuestionBytes {
			return fmt.Errorf("question %q: instructions plus criteria are %d bytes; the limit is %d", name, n, MaxQuestionBytes)
		} else {
			total += n
		}
		switch q.Type {
		case TypeNoul:
			if len(q.Criteria) > 0 {
				var c map[string]string
				if err := json.Unmarshal(q.Criteria, &c); err != nil {
					return fmt.Errorf("question %q: noul criteria must be an object of true/false to text", name)
				}
				for k := range c {
					if k != "true" && k != "false" {
						return fmt.Errorf("question %q: noul criteria keys must be \"true\" or \"false\"", name)
					}
				}
			}
		case TypeChoice:
			var c map[string]string
			if len(q.Criteria) == 0 || json.Unmarshal(q.Criteria, &c) != nil {
				return fmt.Errorf("question %q: choice criteria must be an object of option to description", name)
			}
			if len(c) < minChoiceOptions || len(c) > maxChoiceOptions {
				return fmt.Errorf("question %q: choice needs %d to %d options, got %d", name, minChoiceOptions, maxChoiceOptions, len(c))
			}
		case TypeScore:
			var levels []string
			if len(q.Criteria) == 0 || json.Unmarshal(q.Criteria, &levels) != nil {
				return fmt.Errorf("question %q: score criteria must be an array of level descriptions", name)
			}
			if len(levels) < minScoreLevels || len(levels) > maxScoreLevels {
				return fmt.Errorf("question %q: score needs %d to %d levels, got %d", name, minScoreLevels, maxScoreLevels, len(levels))
			}
		default:
			return fmt.Errorf("question %q: type must be noul, choice, or score", name)
		}
	}
	if total > MaxQuestionsTotalBytes {
		return fmt.Errorf("questions: %d bytes across all questions; the limit is %d", total, MaxQuestionsTotalBytes)
	}
	return nil
}

// QuestionBytes is the size StateBudget and ValidateQuestions account for:
// instructions plus criteria as sent.
func QuestionBytes(q Question) int {
	return len(q.Instructions) + len(q.Criteria)
}
```

- [ ] **Step 2.4: Run the type tests**

Run: `go test ./pkg/jev/ -run 'TestValidateQuestions|TestAnswerValue' -v`
Expected: PASS.

- [ ] **Step 2.5: Write the failing state tests**

`pkg/jev/state_test.go`:

```go
package jev

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestScrubStateRemovesHomeInEveryForm(t *testing.T) {
	home := "/Users/someone"
	in := "plain " + home + "/repo/a.go\n" +
		"url file://" + home + "/x\n" +
		`json {"path":"` + home + `/y"}` + "\n" +
		`escaped {"p":"\/Users\/someone\/z"}` + "\n" +
		"bare user someone wrote it; someones is a different word\n" +
		"other /Users/other/z"
	out := ScrubState(in, home, "someone")
	if strings.Contains(out, home) || strings.Contains(out, `\/Users\/someone`) {
		t.Fatalf("home path survived: %q", out)
	}
	for _, want := range []string{"plain ~/repo/a.go", "url file://~/x", `"path":"~/y"`, `"p":"~\/z"`, "bare user ~user wrote it", "someones is a different word", "/Users/other/z"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in %q", want, out)
		}
	}
	if ScrubState("no home here", "", "") != "no home here" {
		t.Fatal("empty home and user must be a no-op")
	}
	if ScrubState("a b", "/", "a") != "~user b" {
		t.Fatal("root home must not be replaced, username still masked")
	}
}

func TestCapStateKeepsHeadAndTailUnderLimit(t *testing.T) {
	head := "OBJECTIVE: build the thing\n"
	middle := strings.Repeat("noise line\n", 400)
	tail := "COMPLETION SUMMARY: done ✓"
	in := head + middle + tail
	limit := 512
	out, truncated := CapState(in, limit)
	if !truncated {
		t.Fatal("expected truncation")
	}
	if len(out) > limit {
		t.Fatalf("capped state is %d bytes, limit %d", len(out), limit)
	}
	if !strings.HasPrefix(out, head) {
		t.Fatalf("head lost: %q", out[:40])
	}
	if !strings.HasSuffix(out, tail) {
		t.Fatalf("tail lost: %q", out[len(out)-40:])
	}
	if !strings.Contains(out, "[state truncated") {
		t.Fatalf("missing marker: %q", out)
	}
	if strings.Contains(out, "�") {
		t.Fatalf("cut inside a rune: %q", out)
	}
	same, tr := CapState("short", 100)
	if tr || same != "short" {
		t.Fatalf("under-limit state changed: %q %v", same, tr)
	}
	tiny, tr := CapState(strings.Repeat("x", 300), 64)
	if !tr || len(tiny) > 64 {
		t.Fatalf("tiny limit not honored: %d bytes", len(tiny))
	}
}

func TestCapStateNeverSplitsMultibyteRunes(t *testing.T) {
	// Two-, three-, and four-byte runes at every position: any byte-aligned
	// cut that ignores boundaries lands mid-rune somewhere in this matrix.
	inputs := map[string]string{
		"two-byte":   strings.Repeat("é", 2000),
		"three-byte": strings.Repeat("€", 2000),
		"four-byte":  strings.Repeat("😀", 2000),
		"mixed":      strings.Repeat("aé€😀", 800),
	}
	for name, in := range inputs {
		for _, limit := range []int{512, 513, 514, 515, 1001, 100, 97} {
			out, truncated := CapState(in, limit)
			if !truncated || len(out) > limit {
				t.Fatalf("%s limit %d: truncated=%v len=%d", name, limit, truncated, len(out))
			}
			if !utf8.ValidString(out) {
				t.Fatalf("%s limit %d: output is not valid UTF-8", name, limit)
			}
			if strings.Contains(out, "�") {
				t.Fatalf("%s limit %d: replacement rune present", name, limit)
			}
		}
	}
	// Pre-existing invalid bytes are kept, not "repaired" away.
	if got := cutRuneEnd("ok\xff"); got != "ok\xff" {
		t.Fatalf("cutRuneEnd removed a pre-existing invalid byte: %q", got)
	}
	if got := cutRuneEnd("ok\xf0\x9f"); got != "ok" {
		t.Fatalf("cutRuneEnd kept a partial four-byte rune: %q", got)
	}
	if got := cutRuneEnd("ok\xe2\x82"); got != "ok" {
		t.Fatalf("cutRuneEnd kept a partial three-byte rune: %q", got)
	}
}

func TestStateBudgetSubtractsLongestQuestion(t *testing.T) {
	small := map[string]Question{"q": {Type: TypeNoul, Instructions: "short"}}
	if got := StateBudget(small); got >= MaxStateBytes || got < MaxStateBytes-200 {
		t.Fatalf("budget for a small question = %d", got)
	}
	big := map[string]Question{
		"q":  {Type: TypeNoul, Instructions: "short"},
		"q2": {Type: TypeChoice, Instructions: strings.Repeat("i", 4000), Criteria: json.RawMessage(`{"a":"` + strings.Repeat("d", 10000) + `","b":"x"}`)},
	}
	if err := ValidateQuestions(big); err != nil {
		t.Fatalf("fixture must validate: %v", err)
	}
	got := StateBudget(big)
	want := MaxStateBytes - QuestionBytes(big["q2"])
	if got != want {
		t.Fatalf("budget = %d, want %d", got, want)
	}
	if got+QuestionBytes(big["q2"]) > MaxStateBytes {
		t.Fatalf("state plus longest question exceeds MaxStateBytes: %d", got+QuestionBytes(big["q2"]))
	}
	if got < MaxStateBytes-MaxQuestionBytes {
		t.Fatalf("validated questions must leave at least %d bytes, got %d", MaxStateBytes-MaxQuestionBytes, got)
	}
	if StateBudget(nil) != MaxStateBytes {
		t.Fatal("no questions should yield the full budget")
	}
	absurd := map[string]Question{"q": {Type: TypeNoul, Instructions: strings.Repeat("i", MaxStateBytes)}}
	if StateBudget(absurd) != 0 {
		t.Fatal("a question larger than the whole budget must leave zero state room")
	}
}
```

- [ ] **Step 2.6: Write `pkg/jev/state.go`**

```go
package jev

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

// MaxStateBytes caps state plus the longest question before anything leaves
// the machine. The upstream limit is 32k tokens for state plus the longest
// question; code tokenizes at roughly 2.5 bytes per token, so 64 KiB stays
// under it with margin. ValidateQuestions bounds each question to
// MaxQuestionBytes, so StateBudget is always at least
// MaxStateBytes-MaxQuestionBytes (48 KiB) and the combined size never
// exceeds MaxStateBytes.
const MaxStateBytes = 64 << 10

// LocalIdentity returns the home directory and account name to scrub. The
// username comes from os/user, falling back to the last path element of the
// home directory.
func LocalIdentity() (home, username string) {
	home, _ = os.UserHomeDir()
	if u, err := user.Current(); err == nil && u.Username != "" {
		username = u.Username
	} else if home != "" {
		username = filepath.Base(home)
	}
	return home, username
}

// ScrubState masks the home directory path (plain and JSON-escaped forms)
// with "~" and the whole-word account name with "~user". It runs on every
// state string before sending, in the endpoint and in bundle steps. It is a
// narrow guard against the most common identifier, not a full anonymizer.
func ScrubState(state, home, username string) string {
	home = strings.TrimRight(home, "/")
	if home != "" && home != "/" {
		state = strings.ReplaceAll(state, home, "~")
		escaped := strings.ReplaceAll(home, "/", `\/`)
		state = strings.ReplaceAll(state, escaped, "~")
	}
	username = strings.TrimSpace(username)
	if username != "" && username != "/" && username != "~" {
		re := regexp.MustCompile(`\b` + regexp.QuoteMeta(username) + `\b`)
		state = re.ReplaceAllString(state, "~user")
	}
	return state
}

// StateBudget is how much state fits alongside the questions: MaxStateBytes
// minus the largest single question's encoded size. The vendor counts state
// plus the longest question. Callers validate questions first, so the result
// is never below MaxStateBytes-MaxQuestionBytes; an oversized question that
// slipped past validation yields a zero budget, which Decide rejects.
func StateBudget(qs map[string]Question) int {
	longest := 0
	for _, q := range qs {
		if n := QuestionBytes(q); n > longest {
			longest = n
		}
	}
	if longest >= MaxStateBytes {
		return 0
	}
	return MaxStateBytes - longest
}

// CapState keeps the head and the tail of an oversized state with a marker
// between them, because the objective sits at the start of a typical state
// and the conclusion at the end. The result never exceeds maxBytes, marker
// included, and cuts land on UTF-8 boundaries. The second result reports
// whether anything was removed.
func CapState(state string, maxBytes int) (string, bool) {
	if maxBytes <= 0 || len(state) <= maxBytes {
		return state, false
	}
	marker := fmt.Sprintf("\n[state truncated: %d of %d bytes omitted]\n", 0, len(state))
	// Reserve the marker at its widest (the omitted count can only grow).
	room := maxBytes - len(marker) - 8
	if room < 2 {
		return cutRuneStart(state, maxBytes), true
	}
	headLen := room / 4
	tailLen := room - headLen
	head := cutRuneEnd(state[:headLen])
	// Pass the whole state so the boundary scan runs; slicing first would
	// hand cutRuneStart a string it returns unchanged.
	tail := cutRuneStart(state, tailLen)
	omitted := len(state) - len(head) - len(tail)
	out := head + fmt.Sprintf("\n[state truncated: %d of %d bytes omitted]\n", omitted, len(state)) + tail
	if len(out) > maxBytes {
		// Only possible when the omitted count grew a digit; trim the tail.
		tail = cutRuneStart(tail, len(tail)-(len(out)-maxBytes))
		out = head + fmt.Sprintf("\n[state truncated: %d of %d bytes omitted]\n", len(state)-len(head)-len(tail), len(state)) + tail
	}
	return out, true
}

// cutRuneEnd drops a trailing partial rune so s ends on a rune boundary.
// It walks back at most UTFMax bytes to the last rune-start byte and cuts
// there when the bytes from that start do not form a complete rune. A lone
// invalid byte that was already in the input counts as a complete error
// rune and is kept, so pre-existing garbage is never silently removed.
func cutRuneEnd(s string) string {
	i := len(s) - 1
	for i >= 0 && i >= len(s)-utf8.UTFMax && !utf8.RuneStart(s[i]) {
		i--
	}
	if i < 0 {
		return s
	}
	if !utf8.FullRuneInString(s[i:]) {
		return s[:i]
	}
	return s
}

// cutRuneStart returns at most n trailing bytes of s, starting on a rune
// boundary, so it may return fewer than n bytes.
func cutRuneStart(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if n >= len(s) {
		n = len(s)
	}
	cut := len(s) - n
	for cut < len(s) && !utf8.RuneStart(s[cut]) {
		cut++
	}
	return s[cut:]
}
```

- [ ] **Step 2.7: Run the state tests**

Run: `go test ./pkg/jev/ -run 'TestScrubState|TestCapState' -v`
Expected: PASS.

- [ ] **Step 2.8: Write the failing client tests**

`pkg/jev/client_test.go`:

```go
package jev

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"rcodegen/pkg/settings"
)

// noSleep skips retry backoff in tests.
func noSleep(context.Context, time.Duration) error { return nil }

func questions() map[string]Question {
	return map[string]Question{
		"done":    {Type: TypeNoul, Instructions: "Is it done?"},
		"verdict": {Type: TypeChoice, Instructions: "Pick", Criteria: json.RawMessage(`{"complete":"c","incomplete":"i"}`)},
		"quality": {Type: TypeScore, Instructions: "Rate", Criteria: json.RawMessage(`["low","mid","high"]`)},
	}
}

func fakeUpstream(t *testing.T, handler http.HandlerFunc) (*httptest.Server, settings.JevDefaults) {
	t.Helper()
	// A developer's real key must never reach the fake upstream or a test
	// failure message: ResolveAPIKey prefers the environment, so clear it.
	t.Setenv("TYPESAFE_API_KEY", "")
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv, settings.JevDefaults{BaseURL: srv.URL, Model: "jev-1.13.0", TimeoutSeconds: 2, MaxConcurrent: 2, APIKey: "secret-key"}
}

func okBody() string {
	return `{"model":"jev-1.13.0","answers":{` +
		`"done":{"type":"noul","noul":0.87},` +
		`"verdict":{"type":"choice","choice":"incomplete","probabilities":{"complete":0.2,"incomplete":0.8},"confidence":0.6},` +
		`"quality":{"type":"score","score":1.1,"legend":{"0":"low","1":"mid","2":"high"},"probabilities":{"0":0.1,"1":0.7,"2":0.2},"confidence":0.5}},` +
		`"usage":{"input_tokens":400,"output_tokens":0}}`
}

func TestNewClientRejectsMissingKeyAndBadOrigin(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "")
	if _, err := NewClient(settings.JevDefaults{BaseURL: "https://api.typesafe.ai", Model: "jev-1.13.0", TimeoutSeconds: 1}); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("missing key err = %v", err)
	}
	t.Setenv("TYPESAFE_API_KEY", "from-env")
	if _, err := NewClient(settings.JevDefaults{BaseURL: "https://api.typesafe.ai", Model: "jev-1.13.0", TimeoutSeconds: 1}); err != nil {
		t.Fatalf("env key must configure the client: %v", err)
	}
	t.Setenv("TYPESAFE_API_KEY", "")
	for _, bad := range []string{"http://api.typesafe.ai", "https://u:p@api.typesafe.ai", "https://api.typesafe.ai/v1", "ftp://x", ""} {
		if _, err := NewClient(settings.JevDefaults{BaseURL: bad, Model: "m", TimeoutSeconds: 1, APIKey: "k"}); err == nil {
			t.Fatalf("expected error for base url %q", bad)
		}
	}
	if _, err := NewClient(settings.JevDefaults{BaseURL: "http://127.0.0.1:1", Model: "m", TimeoutSeconds: 1, APIKey: "k"}); err != nil {
		t.Fatalf("loopback http must be allowed for tests: %v", err)
	}
}

func TestDecideSuccessSendsAuthAndParsesAnswers(t *testing.T) {
	var got Request
	var auth string
	srv, cfg := fakeUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		if r.URL.Path != "/v1/systemone" || r.Method != http.MethodPost {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(okBody()))
	})
	_ = srv
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Decide(context.Background(), "", "some state", questions())
	if err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer secret-key" {
		// Never print the header: if the environment leaked in, it holds a real key.
		t.Fatal("Authorization header did not carry the fixture key")
	}
	if got.Model != "jev-1.13.0" || got.State != "some state" || len(got.Questions) != 3 {
		t.Fatalf("request = %+v", got)
	}
	if resp.Answers["done"].Value() != "0.8700" || resp.Answers["verdict"].Value() != "incomplete" || resp.Answers["quality"].Value() != "1.1000" {
		t.Fatalf("answers = %+v", resp.Answers)
	}
	if resp.Usage.InputTokens != 400 {
		t.Fatalf("usage = %+v", resp.Usage)
	}
	if c.Model() != "jev-1.13.0" {
		t.Fatalf("model = %q", c.Model())
	}
}

func TestDecideModelOverride(t *testing.T) {
	var got Request
	_, cfg := fakeUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(okBody()))
	})
	c, _ := NewClient(cfg)
	if _, err := c.Decide(context.Background(), "jev-latest", "s", questions()); err != nil {
		t.Fatal(err)
	}
	if got.Model != "jev-latest" {
		t.Fatalf("model override not sent: %q", got.Model)
	}
}

func TestDecideRetriesRateLimitThenSucceeds(t *testing.T) {
	var calls int32
	_, cfg := fakeUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"slow down"}}`))
			return
		}
		_, _ = w.Write([]byte(okBody()))
	})
	c, _ := NewClient(cfg)
	c.sleep = noSleep
	if _, err := c.Decide(context.Background(), "", "s", questions()); err != nil {
		t.Fatalf("expected retry to succeed: %v", err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
}

func TestDecideBackoffStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, cfg := fakeUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	})
	c, _ := NewClient(cfg)
	entered := make(chan struct{})
	c.sleep = func(ctx context.Context, _ time.Duration) error {
		close(entered)
		return Sleep(ctx, time.Hour)
	}
	done := make(chan error, 1)
	go func() {
		_, err := c.Decide(ctx, "", "s", questions())
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("client never entered backoff")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("backoff ignored cancellation")
	}
}

func TestDecideUnauthorizedIsNotRetriedAndHidesKey(t *testing.T) {
	var calls int32
	_, cfg := fakeUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"bad key secret-key"}}`))
	})
	c, _ := NewClient(cfg)
	c.sleep = noSleep
	_, err := c.Decide(context.Background(), "", "s", questions())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 401 || apiErr.Retryable {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "secret-key") {
		t.Fatalf("key leaked into error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("401 must not retry, calls = %d", calls)
	}
}

func TestDecideValidationErrorsBeforeNetwork(t *testing.T) {
	var calls int32
	_, cfg := fakeUpstream(t, func(w http.ResponseWriter, r *http.Request) { atomic.AddInt32(&calls, 1) })
	c, _ := NewClient(cfg)
	if _, err := c.Decide(context.Background(), "", "", questions()); err == nil {
		t.Fatal("empty state must fail")
	}
	if _, err := c.Decide(context.Background(), "", strings.Repeat("x", StateBudget(questions())+1), questions()); !errors.Is(err, ErrStateTooLarge) {
		t.Fatalf("oversize err = %v", err)
	}
	if _, err := c.Decide(context.Background(), "", "s", map[string]Question{}); err == nil {
		t.Fatal("empty questions must fail")
	}
	if calls != 0 {
		t.Fatalf("network touched %d times", calls)
	}
}

func TestDecideRejectsUnusableAnswers(t *testing.T) {
	// Exactly one defect (the choice "maybe"); every other answer is valid,
	// so map iteration order cannot surface a different error first.
	_, cfg := fakeUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"model":"jev-1.13.0","answers":{` +
			`"done":{"type":"noul","noul":0.5},` +
			`"verdict":{"type":"choice","choice":"maybe","probabilities":{"complete":0.5,"incomplete":0.5},"confidence":0.1},` +
			`"quality":{"type":"score","score":1,"legend":{"0":"low","1":"mid","2":"high"},"probabilities":{"0":0,"1":1,"2":0},"confidence":1}},` +
			`"usage":{"input_tokens":1,"output_tokens":0}}`))
	})
	c, _ := NewClient(cfg)
	_, err := c.Decide(context.Background(), "", "s", questions())
	if !errors.Is(err, ErrInvalidResponse) || !strings.Contains(err.Error(), "maybe") {
		t.Fatalf("choice outside options must be rejected: %v", err)
	}
	_, cfg = fakeUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"model":"jev-1.13.0","answers":{},"usage":{"input_tokens":1,"output_tokens":0}}`))
	})
	c, _ = NewClient(cfg)
	if _, err := c.Decide(context.Background(), "", "s", questions()); !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("empty answers must be rejected: %v", err)
	}
}

func TestDecideUpstreamValidationMapsTo422NotRetryable(t *testing.T) {
	_, cfg := fakeUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"error":{"message":"criteria invalid"}}`))
	})
	c, _ := NewClient(cfg)
	_, err := c.Decide(context.Background(), "", "s", questions())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 422 || apiErr.Retryable || !strings.Contains(apiErr.Message, "criteria invalid") {
		t.Fatalf("err = %v", err)
	}
}

func TestCostUSD(t *testing.T) {
	if got := CostUSD(1_000_000); math.Abs(got-0.042) > 1e-12 {
		t.Fatalf("cost per million = %v", got)
	}
	if got := CostUSD(300); math.Abs(got-1.26e-5) > 1e-15 {
		t.Fatalf("cost for 300 tokens = %v", got)
	}
	if got := CostUSD(0); got != 0 {
		t.Fatalf("zero tokens cost = %v", got)
	}
}
```

- [ ] **Step 2.9: Write `pkg/jev/client.go`**

```go
package jev

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"rcodegen/pkg/settings"
)

const (
	// InputCostPerMillionUSD is TypeSafe's published Jev price. Output is free.
	InputCostPerMillionUSD = 0.042

	systemOnePath    = "/v1/systemone"
	maxResponseBytes = 1 << 20
	maxAttempts      = 3
)

var (
	ErrNotConfigured = errors.New("jev: no API key configured (set TYPESAFE_API_KEY or defaults.jev.api_key)")
	ErrStateTooLarge = fmt.Errorf("jev: state exceeds %d bytes", MaxStateBytes)

	retryBackoff = []time.Duration{200 * time.Millisecond, 800 * time.Millisecond}
)

// APIError is a non-2xx upstream reply. Retryable is true for 429, 529, and
// other 5xx; the message never contains the API key.
type APIError struct {
	Status    int
	Message   string
	Retryable bool
}

func (e *APIError) Error() string {
	return fmt.Sprintf("jev: upstream HTTP %d: %s", e.Status, e.Message)
}

// Sleeper waits for a backoff interval or until ctx is done, returning
// ctx.Err() in the latter case. Tests substitute a no-op.
type Sleeper func(ctx context.Context, d time.Duration) error

// Sleep is the default Sleeper: cancellation ends the wait immediately, so
// a client that disconnects during retry backoff never holds a decision
// slot for the remaining interval.
func Sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Client performs System One calls. It is safe for concurrent use.
type Client struct {
	origin  *url.URL
	apiKey  string
	model   string
	timeout time.Duration
	http    *http.Client
	sleep   Sleeper // replaced in tests
}

// NewClient validates the configuration and returns a client. The base URL
// must be an https origin with no path, query, or credentials; plain http is
// accepted only for loopback hosts so tests can use httptest.
func NewClient(cfg settings.JevDefaults) (*Client, error) {
	apiKey := cfg.ResolveAPIKey()
	if apiKey == "" {
		return nil, ErrNotConfigured
	}
	origin, err := validateOrigin(cfg.BaseURL)
	if err != nil {
		return nil, err
	}
	model := cfg.Model
	if model == "" {
		model = settings.DefaultJevModel
	}
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = time.Duration(settings.DefaultJevTimeoutSeconds) * time.Second
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &Client{
		origin:  origin,
		apiKey:  apiKey,
		model:   model,
		timeout: timeout,
		http: &http.Client{
			Transport: transport,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return errors.New("jev: upstream redirects are not allowed")
			},
		},
		sleep: Sleep,
	}, nil
}

func validateOrigin(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Hostname() == "" {
		return nil, errors.New("jev: base_url must be an https origin")
	}
	if u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("jev: base_url must be an origin without path, query, or credentials")
	}
	host := u.Hostname()
	loopback := strings.EqualFold(host, "localhost")
	if ip := net.ParseIP(host); ip != nil {
		loopback = ip.IsLoopback()
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !loopback {
			return nil, errors.New("jev: base_url must use https (http is allowed only for loopback)")
		}
	default:
		return nil, errors.New("jev: base_url must use https")
	}
	u.Path, u.RawPath = "", ""
	return u, nil
}

// Model returns the configured default model.
func (c *Client) Model() string { return c.model }

// Decide sends one System One request. An empty model uses the configured
// default. State must already be scrubbed; Decide enforces the size cap and
// question schema before touching the network.
func (c *Client) Decide(ctx context.Context, model, state string, questions map[string]Question) (*Response, error) {
	if strings.TrimSpace(state) == "" {
		return nil, errors.New("jev: state must not be empty")
	}
	if err := ValidateQuestions(questions); err != nil {
		return nil, fmt.Errorf("jev: %w", err)
	}
	if budget := StateBudget(questions); budget == 0 || len(state) > budget {
		return nil, ErrStateTooLarge
	}
	if model == "" {
		model = c.model
	}
	payload, err := json.Marshal(Request{Model: model, State: state, Questions: questions})
	if err != nil {
		return nil, fmt.Errorf("jev: encode request: %w", err)
	}

	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			if err := c.sleep(ctx, retryBackoff[attempt-1]); err != nil {
				return nil, err
			}
		}
		resp, err := c.post(ctx, payload)
		if err == nil {
			if verr := ValidateResponse(questions, resp); verr != nil {
				return nil, verr
			}
			return resp, nil
		}
		lastErr = err
		var apiErr *APIError
		if errors.Is(err, ErrInvalidResponse) || (errors.As(err, &apiErr) && !apiErr.Retryable) {
			return nil, err
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	return nil, lastErr
}

func (c *Client) post(ctx context.Context, payload []byte) (*Response, error) {
	reqCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	u := *c.origin
	u.Path = systemOnePath
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, u.String(), bytes.NewReader(payload))
	if err != nil {
		return nil, errors.New("jev: build request")
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	httpResp, err := c.http.Do(req)
	if err != nil {
		return nil, &APIError{Status: 0, Message: c.redact(err.Error()), Retryable: true}
	}
	defer httpResp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(httpResp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, &APIError{Status: httpResp.StatusCode, Message: "read response: " + c.redact(err.Error()), Retryable: true}
	}
	if len(body) > maxResponseBytes {
		return nil, &APIError{Status: httpResp.StatusCode, Message: "response exceeds 1 MiB", Retryable: false}
	}
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return nil, &APIError{
			Status:    httpResp.StatusCode,
			Message:   c.redact(upstreamMessage(body, httpResp.StatusCode)),
			Retryable: httpResp.StatusCode == 429 || httpResp.StatusCode >= 500,
		}
	}
	var out Response
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("%w: malformed JSON from upstream: %s", ErrInvalidResponse, c.redact(err.Error()))
	}
	if out.Answers == nil {
		return nil, fmt.Errorf("%w: upstream response has no answers", ErrInvalidResponse)
	}
	return &out, nil
}

// redact keeps the API key out of every error path.
func (c *Client) redact(s string) string {
	if c.apiKey == "" {
		return s
	}
	return strings.ReplaceAll(s, c.apiKey, "[redacted]")
}

func upstreamMessage(body []byte, status int) string {
	var env struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(body, &env) == nil && len(env.Error) > 0 {
		var text string
		if json.Unmarshal(env.Error, &text) == nil && text != "" {
			return text
		}
		var detail struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(env.Error, &detail) == nil && detail.Message != "" {
			return detail.Message
		}
	}
	var flat struct {
		Message string `json:"message"`
		Detail  string `json:"detail"`
	}
	if json.Unmarshal(body, &flat) == nil {
		if flat.Message != "" {
			return flat.Message
		}
		if flat.Detail != "" {
			return flat.Detail
		}
	}
	return http.StatusText(status)
}

// CostUSD prices a call from its input tokens at the published rate.
func CostUSD(inputTokens int) float64 {
	if inputTokens <= 0 {
		return 0
	}
	return float64(inputTokens) * InputCostPerMillionUSD / 1_000_000
}
```

Add client tests for null probability entries in both choice and score responses; each must fail with `ErrInvalidResponse` without retrying. Add a validation-error redaction test so vendor-provided invalid values cannot echo the API key.

- [ ] **Step 2.10: Run the whole package with the race detector**

Run: `go test ./pkg/jev/ -race -v`
Expected: all PASS. `go vet ./pkg/jev/` clean.

---

### Task 3: `POST /v1/decisions` on rserve

**Files:**
- Create: `pkg/server/openai/decisions.go`
- Modify: `pkg/server/openai/errorcodes.go:22-60` (declare and classify), `pkg/server/openai/types.go:244-262` (`HealthResponse`), `pkg/server/openai/handler.go:25-37` (struct), `:62-93` (constructor and route), `:168-181` (health)
- Test: `pkg/server/openai/decisions_test.go`

**Interfaces:**
- Consumes: `jev.NewClient`, `jev.Client.Decide`, `jev.ValidateQuestions`, `jev.ScrubState`, `jev.CapState`, `jev.MaxStateBytes`, `jev.CostUSD`, `jev.APIError`, `settings.JevDefaults`.
- Produces: `DecisionRequest`, `DecisionResponse`, `DecisionHealth`, `Handler.decisions *decisionGate`, `HealthResponse.Decisions *DecisionHealth`.

- [ ] **Step 3.1: Write the failing endpoint tests**

`pkg/server/openai/decisions_test.go`:

```go
package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"rcodegen/pkg/jev"
	"rcodegen/pkg/server"
	"rcodegen/pkg/settings"
)

const decisionOK = `{"model":"jev-1.13.0","answers":{"verdict":{"type":"choice","choice":"incomplete","probabilities":{"complete":0.2,"incomplete":0.8},"confidence":0.6},"gaps":{"type":"noul","noul":0.9}},"usage":{"input_tokens":500,"output_tokens":0}}`

func decisionBody(state string) string {
	return `{"state":` + strings.TrimSpace(mustJSON(state)) + `,"questions":{` +
		`"verdict":{"type":"choice","instructions":"pick","criteria":{"complete":"c","incomplete":"i"}},` +
		`"gaps":{"type":"noul","instructions":"gaps?"}}}`
}

func mustJSON(v interface{}) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func newDecisionHandler(t *testing.T, upstream http.HandlerFunc, maxConcurrent int) (*Handler, *httptest.Server) {
	t.Helper()
	t.Setenv("TYPESAFE_API_KEY", "") // the fixture key must win; never a developer's real key
	srv := httptest.NewServer(upstream)
	t.Cleanup(srv.Close)
	s := settings.GetDefaultSettings()
	s.Defaults.Jev = settings.JevDefaults{BaseURL: srv.URL, Model: "jev-1.13.0", TimeoutSeconds: 2, MaxConcurrent: maxConcurrent, APIKey: "unit-key"}
	h := NewHandler(s, nil, server.NewRunRegistry(1), nil, nil, nil)
	return h, srv
}

func postDecision(h *Handler, body string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/decisions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestDecisionsSuccessScrubsStateAndReportsCost(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	var seen string
	var auth string
	h, _ := newDecisionHandler(t, func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		var req struct {
			State string `json:"state"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		seen = req.State
		_, _ = w.Write([]byte(decisionOK))
	}, 4)

	rec := postDecision(h, decisionBody("report at "+home+"/repo/out.md"), map[string]string{"X-Correlation-ID": "job-7"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if auth != "Bearer unit-key" {
		t.Fatal("upstream Authorization header did not carry the fixture key") // never print it
	}
	if strings.Contains(seen, home) || !strings.Contains(seen, "~/repo/out.md") {
		t.Fatalf("state not scrubbed: %q", seen)
	}
	var resp DecisionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Object != "decision" || resp.Model != "jev-1.13.0" || resp.CorrelationID != "job-7" {
		t.Fatalf("resp = %+v", resp)
	}
	if resp.Answers["verdict"].Choice != "incomplete" || resp.Answers["gaps"].Noul == nil || *resp.Answers["gaps"].Noul != 0.9 {
		t.Fatalf("answers = %+v", resp.Answers)
	}
	if resp.Usage == nil || resp.Usage.PromptTokens != 500 || resp.CostUSD != jev.CostUSD(500) || resp.CostUSD <= 0 {
		t.Fatalf("usage/cost = %+v %v, want cost %v", resp.Usage, resp.CostUSD, jev.CostUSD(500))
	}
	if rec.Header().Get("X-Correlation-ID") != "job-7" {
		t.Fatal("correlation header not echoed")
	}
	if strings.Contains(rec.Body.String(), "unit-key") {
		t.Fatal("key leaked into response")
	}
}

func TestDecisionsUnconfiguredIs503NotRetryable(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "")
	h := NewHandler(settings.GetDefaultSettings(), nil, server.NewRunRegistry(1), nil, nil, nil)
	rec := postDecision(h, decisionBody("s"), nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var er ErrorResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &er)
	if er.Error.Code != string(codeDecisionsUnconfigured) || er.Error.Retryable {
		t.Fatalf("error = %+v", er.Error)
	}
}

func TestDecisionsNilSettingsIsUnconfigured(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "")
	h := NewHandler(nil, nil, server.NewRunRegistry(1), nil, nil, nil)
	if rec := postDecision(h, decisionBody("s"), nil); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d", rec.Code)
	}
}

func TestDecisionsRejectsBadRequests(t *testing.T) {
	h, _ := newDecisionHandler(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(decisionOK)) }, 4)
	cases := []struct {
		name string
		body string
		code ErrorCode
		http int
	}{
		{"invalid json", `{"state":`, codeInvalidJSON, http.StatusBadRequest},
		{"trailing garbage", decisionBody("s") + ` trailing`, codeInvalidJSON, http.StatusBadRequest},
		{"two documents", decisionBody("s") + decisionBody("t"), codeInvalidJSON, http.StatusBadRequest},
		{"oversized trailing whitespace", decisionBody("s") + strings.Repeat(" ", maxDecisionBodyBytes), codeStateTooLarge, http.StatusRequestEntityTooLarge},
		{"body over limit", `{"state":"` + strings.Repeat("x", 600*1024) + `","questions":{"q":{"type":"noul","instructions":"x"}}}`, codeStateTooLarge, http.StatusRequestEntityTooLarge},
		{"empty state", `{"state":"","questions":{"q":{"type":"noul","instructions":"x"}}}`, codeInvalidDecision, http.StatusBadRequest},
		{"no questions", `{"state":"s","questions":{}}`, codeInvalidDecision, http.StatusBadRequest},
		{"bad type", `{"state":"s","questions":{"q":{"type":"essay","instructions":"x"}}}`, codeInvalidDecision, http.StatusBadRequest},
		{"foreign model", `{"state":"s","model":"gpt-5","questions":{"q":{"type":"noul","instructions":"x"}}}`, codeInvalidModel, http.StatusBadRequest},
		{"too large", `{"state":"` + strings.Repeat("x", 100*1024) + `","questions":{"q":{"type":"noul","instructions":"x"}}}`, codeStateTooLarge, http.StatusRequestEntityTooLarge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := postDecision(h, tc.body, nil)
			if rec.Code != tc.http {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			var er ErrorResponse
			_ = json.Unmarshal(rec.Body.Bytes(), &er)
			if er.Error.Code != string(tc.code) {
				t.Fatalf("code = %q, want %q", er.Error.Code, tc.code)
			}
		})
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/decisions", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET status %d", rec.Code)
	}
}

func TestDecisionsUpstreamFailuresMapToRetryability(t *testing.T) {
	t.Run("rate limited", func(t *testing.T) {
		h, _ := newDecisionHandler(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(429) }, 4)
		h.decisions.sleep = func(context.Context, time.Duration) error { return nil }
		rec := postDecision(h, decisionBody("s"), nil)
		var er ErrorResponse
		_ = json.Unmarshal(rec.Body.Bytes(), &er)
		if rec.Code != http.StatusServiceUnavailable || er.Error.Code != string(codeDecisionUpstream) || !er.Error.Retryable {
			t.Fatalf("status %d error %+v", rec.Code, er.Error)
		}
	})
	t.Run("bad key", func(t *testing.T) {
		h, _ := newDecisionHandler(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"error":{"message":"unit-key rejected"}}`))
		}, 4)
		rec := postDecision(h, decisionBody("s"), nil)
		var er ErrorResponse
		_ = json.Unmarshal(rec.Body.Bytes(), &er)
		if rec.Code != http.StatusBadGateway || er.Error.Code != string(codeDecisionUpstreamAuth) || er.Error.Retryable {
			t.Fatalf("status %d error %+v", rec.Code, er.Error)
		}
		if strings.Contains(rec.Body.String(), "unit-key") {
			t.Fatal("key leaked")
		}
	})
	t.Run("upstream 422", func(t *testing.T) {
		h, _ := newDecisionHandler(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(422)
			_, _ = w.Write([]byte(`{"error":{"message":"criteria invalid"}}`))
		}, 4)
		rec := postDecision(h, decisionBody("s"), nil)
		var er ErrorResponse
		_ = json.Unmarshal(rec.Body.Bytes(), &er)
		if rec.Code != http.StatusBadRequest || er.Error.Code != string(codeInvalidDecision) || !strings.Contains(er.Error.Message, "criteria invalid") {
			t.Fatalf("status %d error %+v", rec.Code, er.Error)
		}
	})
	t.Run("unusable upstream answers are an upstream fault", func(t *testing.T) {
		h, _ := newDecisionHandler(t, func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"model":"jev-1.13.0","answers":{"verdict":{"type":"choice","choice":"maybe","probabilities":{"complete":0.5,"incomplete":0.5},"confidence":0.1},"gaps":{"type":"noul","noul":0.9}},"usage":{"input_tokens":5,"output_tokens":0}}`))
		}, 4)
		rec := postDecision(h, decisionBody("s"), nil)
		var er ErrorResponse
		_ = json.Unmarshal(rec.Body.Bytes(), &er)
		if rec.Code != http.StatusBadGateway || er.Error.Code != string(codeDecisionUpstreamInvalid) || !er.Error.Retryable {
			t.Fatalf("status %d error %+v", rec.Code, er.Error)
		}
		if !strings.Contains(er.Error.Message, "maybe") {
			t.Fatalf("message should name the bad choice: %q", er.Error.Message)
		}
	})
}

func TestDecisionsCapacityIsFastRetryable503(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	h, _ := newDecisionHandler(t, func(w http.ResponseWriter, r *http.Request) {
		<-release
		_, _ = w.Write([]byte(decisionOK))
	}, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		postDecision(h, decisionBody("first"), nil)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for h.decisions.inFlight() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	start := time.Now()
	rec := postDecision(h, decisionBody("second"), nil)
	if rec.Code != http.StatusServiceUnavailable || time.Since(start) > time.Second {
		t.Fatalf("status %d after %s: %s", rec.Code, time.Since(start), rec.Body.String())
	}
	var er ErrorResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &er)
	if er.Error.Code != string(codeDecisionCapacity) || !er.Error.Retryable {
		t.Fatalf("error = %+v", er.Error)
	}
	once.Do(func() { close(release) })
	<-done
}

func TestDecisionsRequireBearerWhenTokenSet(t *testing.T) {
	os.Setenv("RSERVE_TOKEN", "t0k")
	defer os.Unsetenv("RSERVE_TOKEN")
	h, _ := newDecisionHandler(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(decisionOK)) }, 2)
	if rec := postDecision(h, decisionBody("s"), nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token status %d", rec.Code)
	}
	if rec := postDecision(h, decisionBody("s"), map[string]string{"Authorization": "Bearer t0k"}); rec.Code != http.StatusOK {
		t.Fatalf("with token status %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHealthReportsDecisions(t *testing.T) {
	h, _ := newDecisionHandler(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(decisionOK)) }, 3)
	postDecision(h, decisionBody("s"), nil)
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var hr HealthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &hr); err != nil {
		t.Fatal(err)
	}
	d := hr.Decisions
	if d == nil || !d.Configured || d.Model != "jev-1.13.0" || d.Total != 1 || d.Failed != 0 || d.MaxConcurrent != 3 || d.CostUSD <= 0 {
		t.Fatalf("decisions health = %+v", d)
	}
}
```

- [ ] **Step 3.2: Run to confirm failure**

Run: `go test ./pkg/server/openai/ -run 'Decisions|HealthReportsDecisions' -v`
Expected: compile errors for `DecisionResponse`, `codeDecisionsUnconfigured`, `h.decisions`.

- [ ] **Step 3.3: Declare and classify the error codes**

In `errorcodes.go`, add to the not-retryable group of the `const` block:

```go
	codeInvalidDecision        ErrorCode = "invalid_decision"
	codeStateTooLarge          ErrorCode = "state_too_large"
	codeDecisionsUnconfigured  ErrorCode = "decisions_unconfigured"
	codeDecisionUpstreamAuth   ErrorCode = "decision_upstream_auth"
	codeDecisionUpstreamRejected ErrorCode = "decision_upstream_rejected"
```

And to the retryable group:

```go
	codeDecisionCapacity        ErrorCode = "decision_capacity"
	codeDecisionUpstream        ErrorCode = "decision_upstream"
	codeDecisionUpstreamInvalid ErrorCode = "decision_upstream_invalid"
```

In `errorRetryable`, add, with the comment style the file uses:

```go
	// Decisions API: a malformed or oversized decision, an unconfigured
	// vendor key, or a rejected vendor key will fail the same way every time.
	codeInvalidDecision:       false,
	codeStateTooLarge:         false,
	codeDecisionsUnconfigured: false,
	codeDecisionUpstreamAuth:  false,
	codeDecisionUpstreamRejected: false,
	// All decision slots busy, the vendor rate-limited or errored, or the
	// vendor returned a reply that failed validation: the caller's request
	// was fine and the same request can succeed shortly.
	codeDecisionCapacity:        true,
	codeDecisionUpstream:        true,
	codeDecisionUpstreamInvalid: true,
```

`errorcodes_test.go` fails on any code declared but unused or used but unclassified, so all eight must be both declared and referenced by `decisions.go`.

- [ ] **Step 3.4: Add the health type**

In `types.go`, inside `HealthResponse` after the async fields, add:

```go
	// Decisions describes the Jev decisions gateway. Nil when the build has
	// no decisions support; Configured false when no vendor key is set.
	Decisions *DecisionHealth `json:"decisions,omitempty"`
```

And after the struct:

```go
// DecisionHealth is the /health view of POST /v1/decisions.
type DecisionHealth struct {
	Configured    bool    `json:"configured"`
	Model         string  `json:"model,omitempty"`
	Total         int64   `json:"total"`
	Failed        int64   `json:"failed"`
	InFlight      int     `json:"in_flight"`
	MaxConcurrent int     `json:"max_concurrent"`
	CostUSD       float64 `json:"cost_usd"`
}
```

- [ ] **Step 3.5: Write `pkg/server/openai/decisions.go`**

```go
// decisions.go implements POST /v1/decisions: typed, calibrated answers from
// TypeSafe's Jev decision model, proxied so the vendor key stays on the
// server. Decisions have their own small concurrency budget and never touch
// the run registry, so a 100 ms classification is never queued behind a
// multi-minute CLI run. Every state string is scrubbed of the home directory
// before it leaves the machine.
package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"rcodegen/pkg/jev"
	"rcodegen/pkg/settings"
)

const maxDecisionBodyBytes = 512 << 10

// DecisionRequest is the POST /v1/decisions body. It mirrors the vendor
// request minus the key.
type DecisionRequest struct {
	State     string                  `json:"state"`
	Questions map[string]jev.Question `json:"questions"`
	Model     string                  `json:"model,omitempty"`
}

// DecisionResponse is the POST /v1/decisions body on success.
type DecisionResponse struct {
	Object        string                `json:"object"`
	Model         string                `json:"model"`
	Answers       map[string]jev.Answer `json:"answers"`
	Usage         *Usage                `json:"usage,omitempty"`
	CostUSD       float64               `json:"cost_usd"`
	StateBytes    int                   `json:"state_bytes"`
	LatencyMs     int64                 `json:"latency_ms"`
	CorrelationID string                `json:"correlation_id,omitempty"`
}

// decisionGate owns the client, the semaphore, and the counters.
type decisionGate struct {
	client *jev.Client // nil when unconfigured
	model  string
	sem    chan struct{}
	sleep  jev.Sleeper // forwarded to the client; tests substitute a no-op

	total  atomic.Int64
	failed atomic.Int64
	mu     sync.Mutex
	cost   float64
}

func newDecisionGate(s *settings.Settings) *decisionGate {
	g := &decisionGate{sleep: jev.Sleep}
	if s == nil {
		g.sem = make(chan struct{}, settings.DefaultJevMaxConcurrent)
		return g
	}
	cfg := s.Defaults.Jev
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = settings.DefaultJevMaxConcurrent
	}
	g.sem = make(chan struct{}, cfg.MaxConcurrent)
	g.model = cfg.Model
	if cfg.ResolveAPIKey() == "" {
		return g
	}
	client, err := jev.NewClient(cfg)
	if err != nil {
		// A bad base URL or key is a configuration problem; report it once
		// and serve decisions_unconfigured rather than failing startup.
		fmt.Fprintf(os.Stderr, "rserve: decisions disabled: %v\n", err)
		return g
	}
	g.client = client
	g.model = client.Model()
	return g
}

func (g *decisionGate) configured() bool { return g != nil && g.client != nil }

func (g *decisionGate) inFlight() int { return len(g.sem) }

func (g *decisionGate) health() *DecisionHealth {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	cost := g.cost
	g.mu.Unlock()
	return &DecisionHealth{
		Configured:    g.configured(),
		Model:         g.model,
		Total:         g.total.Load(),
		Failed:        g.failed.Load(),
		InFlight:      g.inFlight(),
		MaxConcurrent: cap(g.sem),
		CostUSD:       cost,
	}
}

func (g *decisionGate) record(cost float64, failed bool) {
	g.total.Add(1)
	if failed {
		g.failed.Add(1)
		return
	}
	g.mu.Lock()
	g.cost += cost
	g.mu.Unlock()
}

// handleDecisions serves POST /v1/decisions.
func (h *Handler) handleDecisions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, NewErrorResponse(
			"method not allowed", "invalid_request_error", codeMethodNotAllowed,
		))
		return
	}
	g := h.decisions
	if !g.configured() {
		writeJSON(w, http.StatusServiceUnavailable, NewErrorResponse(
			"decisions are not configured: set TYPESAFE_API_KEY or defaults.jev.api_key", "server_error", codeDecisionsUnconfigured,
		))
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxDecisionBodyBytes)
	var req DecisionRequest
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&req); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeJSON(w, http.StatusRequestEntityTooLarge, NewErrorResponse(
				fmt.Sprintf("request body exceeds %d bytes", maxDecisionBodyBytes), "invalid_request_error", codeStateTooLarge,
			))
			return
		}
		writeJSON(w, http.StatusBadRequest, NewErrorResponse(
			"invalid JSON body: "+err.Error(), "invalid_request_error", codeInvalidJSON,
		))
		return
	}
	// Exactly one JSON value: trailing garbage or a second document means the
	// caller is confused, and a vendor call would spend tokens on it.
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeJSON(w, http.StatusRequestEntityTooLarge, NewErrorResponse(
				fmt.Sprintf("request body exceeds %d bytes", maxDecisionBodyBytes), "invalid_request_error", codeStateTooLarge,
			))
			return
		}
		writeJSON(w, http.StatusBadRequest, NewErrorResponse(
			"request body must contain exactly one JSON object", "invalid_request_error", codeInvalidJSON,
		))
		return
	}
	if strings.TrimSpace(req.State) == "" {
		writeJSON(w, http.StatusBadRequest, NewErrorResponse(
			"state must not be empty", "invalid_request_error", codeInvalidDecision,
		))
		return
	}
	if err := jev.ValidateQuestions(req.Questions); err != nil {
		writeJSON(w, http.StatusBadRequest, NewErrorResponse(
			err.Error(), "invalid_request_error", codeInvalidDecision,
		))
		return
	}
	if req.Model != "" && req.Model != g.model && !strings.HasPrefix(req.Model, "jev-") {
		writeJSON(w, http.StatusBadRequest, NewErrorResponse(
			fmt.Sprintf("model %q is not a Jev model; use %q or a jev-* identifier", req.Model, g.model),
			"invalid_request_error", codeInvalidModel,
		))
		return
	}

	home, username := jev.LocalIdentity()
	state := jev.ScrubState(req.State, home, username)
	if budget := jev.StateBudget(req.Questions); len(state) > budget {
		writeJSON(w, http.StatusRequestEntityTooLarge, NewErrorResponse(
			fmt.Sprintf("state is %d bytes; the limit for these questions is %d", len(state), budget),
			"invalid_request_error", codeStateTooLarge,
		))
		return
	}

	// Admission: a full gate answers immediately rather than waiting behind
	// a slow upstream, so callers with retry policies can back off.
	select {
	case g.sem <- struct{}{}:
		defer func() { <-g.sem }()
	default:
		writeJSON(w, http.StatusServiceUnavailable, NewErrorResponse(
			fmt.Sprintf("all %d decision slots are busy", cap(g.sem)), "server_error", codeDecisionCapacity,
		))
		return
	}

	corrID := correlationID(r)
	start := time.Now()
	resp, err := g.decide(r.Context(), req.Model, state, req.Questions)
	latency := time.Since(start)
	if err != nil {
		g.record(0, true)
		h.writeDecisionError(w, err)
		return
	}
	cost := jev.CostUSD(resp.Usage.InputTokens)
	g.record(cost, false)
	writeJSON(w, http.StatusOK, DecisionResponse{
		Object:  "decision",
		Model:   resp.Model,
		Answers: resp.Answers,
		Usage: &Usage{
			PromptTokens:     resp.Usage.InputTokens,
			CompletionTokens: resp.Usage.OutputTokens,
			TotalTokens:      resp.Usage.InputTokens + resp.Usage.OutputTokens,
		},
		CostUSD:       cost,
		StateBytes:    len(state),
		LatencyMs:     latency.Milliseconds(),
		CorrelationID: corrID,
	})
}

// decide runs the client call with the gate's sleep function so tests can
// skip retry backoff.
func (g *decisionGate) decide(ctx context.Context, model, state string, qs map[string]jev.Question) (*jev.Response, error) {
	return g.client.DecideWithSleep(ctx, model, state, qs, g.sleep)
}

func (h *Handler) writeDecisionError(w http.ResponseWriter, err error) {
	var apiErr *jev.APIError
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		writeJSON(w, http.StatusServiceUnavailable, NewErrorResponse(
			"decision cancelled or timed out", "server_error", codeDecisionUpstream,
		))
	case errors.As(err, &apiErr) && (apiErr.Status == http.StatusUnauthorized || apiErr.Status == http.StatusForbidden):
		writeJSON(w, http.StatusBadGateway, NewErrorResponse(
			"the vendor rejected rserve's API key", "server_error", codeDecisionUpstreamAuth,
		))
	case errors.As(err, &apiErr) && apiErr.Status == http.StatusUnprocessableEntity:
		writeJSON(w, http.StatusBadRequest, NewErrorResponse(
			"the vendor rejected the request: "+apiErr.Message, "invalid_request_error", codeInvalidDecision,
		))
	case errors.Is(err, jev.ErrInvalidResponse):
		// The caller's request was fine; the vendor's reply was not usable.
		// That is an upstream fault, and the same request may succeed later.
		writeJSON(w, http.StatusBadGateway, NewErrorResponse(
			"the vendor returned an unusable reply: "+err.Error(), "server_error", codeDecisionUpstreamInvalid,
		))
	case errors.As(err, &apiErr) && !apiErr.Retryable:
		writeJSON(w, http.StatusBadGateway, NewErrorResponse(
			"vendor rejected the request: "+apiErr.Message, "server_error", codeDecisionUpstreamRejected,
		))
	case errors.As(err, &apiErr):
		writeJSON(w, http.StatusServiceUnavailable, NewErrorResponse(
			"vendor call failed: "+apiErr.Message, "server_error", codeDecisionUpstream,
		))
	default:
		writeJSON(w, http.StatusBadRequest, NewErrorResponse(
			err.Error(), "invalid_request_error", codeInvalidDecision,
		))
	}
}
```

This references `DecideWithSleep`, which does not exist yet. Add it to `pkg/jev/client.go` and make `Decide` delegate to it:

```go
// Decide sends one System One request with the default retry backoff.
func (c *Client) Decide(ctx context.Context, model, state string, questions map[string]Question) (*Response, error) {
	return c.DecideWithSleep(ctx, model, state, questions, c.sleep)
}

// DecideWithSleep is Decide with an explicit backoff Sleeper, so callers that
// run many decisions can share test hooks. A nil Sleeper uses the client's.
func (c *Client) DecideWithSleep(ctx context.Context, model, state string, questions map[string]Question, sleep Sleeper) (*Response, error) {
	if sleep == nil {
		sleep = c.sleep
	}
	// body of the former Decide, with c.sleep(ctx, ...) replaced by sleep(ctx, ...)
}
```

Move the former body of `Decide` into `DecideWithSleep` and change the one `c.sleep(ctx, retryBackoff[attempt-1])` call to `sleep(ctx, retryBackoff[attempt-1])`, keeping the error return. The `ValidateResponse` call stays inside the loop body as written. Re-run `go test ./pkg/jev/` afterward; the Task 2 tests still pass because `Decide` keeps its signature.

- [ ] **Step 3.6: Wire the handler**

In `handler.go` `Handler` struct, add after `runBundleFn`:

```go
	decisions      *decisionGate // POST /v1/decisions gateway
```

In `NewHandler`, add to the literal after `runBundleFn: defaultBundleRun(s),`:

```go
		decisions:      newDecisionGate(s),
```

Register the route after the `/v1/runs/` line:

```go
	h.mux.HandleFunc("/v1/decisions", h.handleDecisions)
```

In `handleHealth`, add to the `HealthResponse` literal:

```go
		Decisions:     h.decisions.health(),
```

Add HTTP tests for upstream 403 (auth, non-retryable), 404 (rejected, non-retryable), and invalid replies (502, retryable). Confirm that no response exposes the vendor key.

- [ ] **Step 3.7: Run the endpoint tests and the whole package**

Run: `go test ./pkg/server/openai/ -race -v -run 'Decisions|HealthReportsDecisions|ErrorCodes'`
Expected: PASS, including the error-code completeness test.

Run: `go test ./pkg/server/openai/ ./pkg/jev/ -race`
Expected: `ok` for both.

---

### Task 4: Bundle schema, validation, and labels

**Files:**
- Modify: `pkg/bundle/bundle.go:18-43`
- Create: `pkg/bundle/validate.go`
- Modify: `pkg/bundle/loader.go:34-67`
- Test: `pkg/bundle/validate_test.go`

**Interfaces:**
- Consumes: `jev.Question`, `jev.ValidateQuestions`.
- Produces: `bundle.DecideDef{State string; Questions map[string]jev.Question; Model string}`, `Step.Decide *DecideDef`, `(Step).ToolLabel() string`, `(*Bundle).Validate() error`.

- [ ] **Step 4.1: Write the failing tests**

`pkg/bundle/validate_test.go`:

```go
package bundle

import (
	"encoding/json"
	"strings"
	"testing"

	"rcodegen/pkg/jev"
)

func decideStep() Step {
	return Step{Name: "gate", Decide: &DecideDef{
		State: "${steps.build.stdout}",
		Questions: map[string]jev.Question{
			"verdict": {Type: "choice", Instructions: "pick", Criteria: json.RawMessage(`{"complete":"c","incomplete":"i"}`)},
		},
	}}
}

func TestValidateAcceptsDecideStep(t *testing.T) {
	b := &Bundle{Name: "x", Steps: []Step{{Name: "build", Tool: "claude", Task: "t"}, decideStep()}}
	if err := b.Validate(); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestValidateRejectsBadDecideSteps(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*Step)
		want string
	}{
		{"missing state", func(s *Step) { s.Decide.State = "" }, "state"},
		{"bad question", func(s *Step) { s.Decide.Questions["verdict"] = jev.Question{Type: "essay", Instructions: "x"} }, "type"},
		{"no questions", func(s *Step) { s.Decide.Questions = nil }, "at least one"},
		{"decide plus tool", func(s *Step) { s.Tool = "claude" }, "cannot combine"},
		{"decide plus vote", func(s *Step) { s.Vote = &VoteDef{Inputs: []string{"a"}, Strategy: "majority"} }, "cannot combine"},
		{"decide plus then", func(s *Step) { s.If = "true"; s.Then = &Step{Name: "t", Tool: "claude", Task: "x"} }, "then/else"},
		{"decide plus else", func(s *Step) { s.If = "true"; s.Else = &Step{Name: "e", Tool: "claude", Task: "x"} }, "then/else"},
		{"no name", func(s *Step) { s.Name = "" }, "name"},
		{"reserved name cost_usd", func(s *Step) {
			s.Decide.Questions["cost_usd"] = jev.Question{Type: "noul", Instructions: "x"}
		}, "reserved"},
		{"reserved name stdout", func(s *Step) {
			s.Decide.Questions["stdout"] = jev.Question{Type: "noul", Instructions: "x"}
		}, "reserved"},
		{"reserved name model", func(s *Step) {
			s.Decide.Questions["model"] = jev.Question{Type: "noul", Instructions: "x"}
		}, "reserved"},
		{"derived suffix confidence", func(s *Step) {
			s.Decide.Questions["verdict_confidence"] = jev.Question{Type: "noul", Instructions: "x"}
		}, "_confidence"},
		{"derived suffix probabilities", func(s *Step) {
			s.Decide.Questions["x_probabilities"] = jev.Question{Type: "noul", Instructions: "x"}
		}, "_probabilities"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := decideStep()
			tc.mut(&s)
			b := &Bundle{Name: "x", Steps: []Step{s}}
			err := b.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestValidateWalksNestedSteps(t *testing.T) {
	bad := decideStep()
	bad.Decide.State = ""
	b := &Bundle{Name: "x", Steps: []Step{
		{Name: "c", If: "true", Then: &bad, Else: &Step{Name: "e", Tool: "claude", Task: "t"}},
	}}
	if err := b.Validate(); err == nil {
		t.Fatal("then-branch decide step not validated")
	}
	good := decideStep()
	b = &Bundle{Name: "x", Steps: []Step{
		{Name: "c", If: "true", Then: &Step{Name: "e", Tool: "claude", Task: "t"}, Else: &good},
	}}
	if err := b.Validate(); err != nil {
		t.Fatalf("decide in else branch must be allowed: %v", err)
	}
}

func TestValidateRejectsDecideInsideParallel(t *testing.T) {
	good := decideStep()
	b := &Bundle{Name: "x", Steps: []Step{
		{Name: "p", Parallel: []Step{{Name: "a", Tool: "claude", Task: "t"}, good}},
	}}
	err := b.Validate()
	if err == nil || !strings.Contains(err.Error(), "parallel") {
		t.Fatalf("decide directly inside parallel must be rejected, err = %v", err)
	}
	nested := &Bundle{Name: "x", Steps: []Step{
		{Name: "p", Parallel: []Step{{Name: "inner", Parallel: []Step{good}}}},
	}}
	if err := nested.Validate(); err == nil || !strings.Contains(err.Error(), "parallel") {
		t.Fatalf("decide nested two levels inside parallel must be rejected, err = %v", err)
	}
	branch := &Bundle{Name: "x", Steps: []Step{
		{Name: "p", Parallel: []Step{{Name: "c", If: "true", Then: &good}}},
	}}
	if err := branch.Validate(); err == nil || !strings.Contains(err.Error(), "parallel") {
		t.Fatalf("decide in a branch inside parallel must be rejected, err = %v", err)
	}
}

func TestBuiltinBundlesValidate(t *testing.T) {
	names, err := List()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if _, err := Load(name); err != nil {
			t.Fatalf("builtin %s: %v", name, err)
		}
	}
}

func TestModelHint(t *testing.T) {
	if got := (Step{Tool: "claude", Model: "opus"}).ModelHint(); got != "opus" {
		t.Fatalf("tool model hint = %q", got)
	}
	s := decideStep()
	s.Decide.Model = "jev-latest"
	if got := s.ModelHint(); got != "jev-latest" {
		t.Fatalf("decide model hint = %q", got)
	}
	if got := decideStep().ModelHint(); got != "" {
		t.Fatalf("decide without override should hint empty, got %q", got)
	}
}

func TestToolLabel(t *testing.T) {
	if got := (Step{Tool: "claude"}).ToolLabel(); got != "claude" {
		t.Fatalf("tool label = %q", got)
	}
	if got := decideStep().ToolLabel(); got != "jev" {
		t.Fatalf("decide label = %q", got)
	}
	if got := (Step{Parallel: []Step{{Tool: "claude"}}}).ToolLabel(); got != "parallel" {
		t.Fatalf("parallel label = %q", got)
	}
	if got := (Step{Vote: &VoteDef{}}).ToolLabel(); got != "" {
		t.Fatalf("vote label = %q", got)
	}
}

func TestDecideJSONRoundTrip(t *testing.T) {
	src := `{"name":"gate","decide":{"state":"s","model":"jev-latest","questions":{"done":{"type":"noul","instructions":"done?"}}}}`
	var s Step
	if err := json.Unmarshal([]byte(src), &s); err != nil {
		t.Fatal(err)
	}
	if s.Decide == nil || s.Decide.Model != "jev-latest" || s.Decide.Questions["done"].Type != "noul" {
		t.Fatalf("decoded = %+v", s)
	}
	out, _ := json.Marshal(s)
	if !strings.Contains(string(out), `"decide":{`) {
		t.Fatalf("encoded = %s", out)
	}
}
```

- [ ] **Step 4.2: Run to confirm failure**

Run: `go test ./pkg/bundle/ -v`
Expected: compile errors for `DecideDef`, `Validate`, `ToolLabel`.

- [ ] **Step 4.3: Extend `bundle.go`**

Add the import and fields:

```go
package bundle

import "rcodegen/pkg/jev"
```

In `Step`, after the `Vote` field:

```go
	// Decision: typed questions answered by the Jev decision model. Result
	// keys (each question name, *_confidence, *_probabilities, stdout)
	// feed ${steps.name.result.key} and "if" conditions.
	Decide *DecideDef `json:"decide,omitempty"`
```

After `VoteDef`:

```go
// DecideDef configures a decide step. State is a template resolved like a
// task; Questions follow the TypeSafe schema; Model overrides defaults.jev.model.
type DecideDef struct {
	State     string                  `json:"state"`
	Questions map[string]jev.Question `json:"questions"`
	Model     string                  `json:"model,omitempty"`
}

// ToolLabel names what runs a step, for display, events, and cost grouping:
// the tool for tool steps, "jev" for decide steps, "parallel" for parallel
// groups, and "" for merge, vote, and pure conditional containers.
func (s Step) ToolLabel() string {
	switch {
	case s.Tool != "":
		return s.Tool
	case s.Decide != nil:
		return "jev"
	case len(s.Parallel) > 0:
		return "parallel"
	}
	return ""
}

// ModelHint is the model a step asked for, wherever it lives: Model for
// tool steps, Decide.Model for decide steps. Displays and started events
// use it so an explicit override is shown, not the configured default.
func (s Step) ModelHint() string {
	if s.Decide != nil {
		return s.Decide.Model
	}
	return s.Model
}
```

- [ ] **Step 4.4: Write `pkg/bundle/validate.go`**

```go
package bundle

import (
	"fmt"
	"strings"

	"rcodegen/pkg/jev"
)

// reservedResultKeys are envelope result keys the executor and orchestrator
// own. A question with one of these names would overwrite accounting or
// collide with what the resolver and displays read.
var reservedResultKeys = map[string]bool{
	"stdout": true, "stderr": true, "model": true, "decision": true,
	"cost_usd": true, "total_cost_usd": true,
	"input_tokens": true, "output_tokens": true,
	"cache_read_tokens": true, "cache_write_tokens": true,
	"output_length": true, "output_truncated": true, "state_truncated": true,
	"steps": true, "completed": true, "merged": true, "votes": true,
}

// Validate checks the parts of a bundle that would otherwise fail only at
// run time after tokens were spent. Today that is decide steps; other step
// kinds keep their historical leniency.
func (b *Bundle) Validate() error {
	for i := range b.Steps {
		if err := validateStep(&b.Steps[i], "", false); err != nil {
			return err
		}
	}
	return nil
}

func validateStep(s *Step, parent string, inParallel bool) error {
	label := s.Name
	if parent != "" {
		label = parent + "/" + s.Name
	}
	if s.Decide != nil {
		if strings.TrimSpace(s.Name) == "" {
			return fmt.Errorf("decide step: name is required")
		}
		if s.Tool != "" || len(s.Parallel) > 0 || s.Merge != nil || s.Vote != nil {
			return fmt.Errorf("step %s: cannot combine decide with tool, parallel, merge, or vote", label)
		}
		if s.Then != nil || s.Else != nil {
			// A step with then/else is a branch container: the orchestrator
			// runs the chosen branch and never the container's own decide.
			return fmt.Errorf("step %s: cannot combine decide with then/else; put the decide step inside a branch instead", label)
		}
		if inParallel {
			// The parallel executor reports a failed child as "partial" and
			// the orchestrator continues past partial, so a gate inside a
			// parallel group could not stop the bundle.
			return fmt.Errorf("step %s: decide steps cannot run inside a parallel group", label)
		}
		if strings.TrimSpace(s.Decide.State) == "" {
			return fmt.Errorf("step %s: decide.state is required", label)
		}
		if err := jev.ValidateQuestions(s.Decide.Questions); err != nil {
			return fmt.Errorf("step %s: decide.%v", label, err)
		}
		for name := range s.Decide.Questions {
			if reservedResultKeys[name] {
				return fmt.Errorf("step %s: question name %q is a reserved result key", label, name)
			}
			if strings.HasSuffix(name, "_confidence") || strings.HasSuffix(name, "_probabilities") {
				return fmt.Errorf("step %s: question name %q collides with derived keys; names may not end in _confidence or _probabilities", label, name)
			}
		}
	}
	for i := range s.Parallel {
		if err := validateStep(&s.Parallel[i], label, true); err != nil {
			return err
		}
	}
	if s.Then != nil {
		if err := validateStep(s.Then, label, inParallel); err != nil {
			return err
		}
	}
	if s.Else != nil {
		if err := validateStep(s.Else, label, inParallel); err != nil {
			return err
		}
	}
	return nil
}
```

- [ ] **Step 4.5: Call `Validate` from `Load`**

In `loader.go`, in the user-bundle branch after `b.SourcePath = userPath`, add:

```go
		if err := b.Validate(); err != nil {
			return nil, fmt.Errorf("invalid bundle %s: %w", name, err)
		}
```

In the builtin branch after `b.SourcePath = findBuiltinBundlePath(name)`, add:

```go
	if err := b.Validate(); err != nil {
		return nil, fmt.Errorf("invalid builtin bundle %s: %w", name, err)
	}
```

- [ ] **Step 4.6: Run the package tests**

Run: `go test ./pkg/bundle/ -v`
Expected: all PASS, including `TestBuiltinBundlesValidate` over the nine existing bundles. `pkg/orchestrator` and `pkg/executor` still compile because `validateStep`'s third argument is internal to this package.

---

### Task 5: The decide executor

**Files:**
- Create: `pkg/executor/decide.go`
- Modify: `pkg/executor/dispatcher.go:18-49`
- Test: `pkg/executor/decide_test.go`

**Interfaces:**
- Consumes: `bundle.DecideDef`, `orchestrator.Context.Resolve/Ctx`, `workspace.Workspace.WriteOutput`, `jev.*`, `settings.Settings`, `runner.SettingsAware`.
- Produces: `executor.DecideExecutor` with `SetSettings(*settings.Settings)` and `Execute(step, ctx, ws)`; `(*Dispatcher).SetSettings(*settings.Settings)`. Envelope result keys as listed in the Design: one string per question, `<q>_confidence`, `<q>_probabilities`, `stdout`, `model`, `cost_usd`, `input_tokens`, `output_tokens`, `state_truncated`. Failure codes: `JEV_UNCONFIGURED`, `JEV_CONFIG_ERROR`, `JEV_INVALID_RESPONSE`, `JEV_ERROR`, `CANCELLED`.

- [ ] **Step 5.1: Write the failing tests**

`pkg/executor/decide_test.go`:

```go
package executor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"rcodegen/pkg/bundle"
	"rcodegen/pkg/envelope"
	"rcodegen/pkg/jev"
	"rcodegen/pkg/orchestrator"
	"rcodegen/pkg/settings"
	"rcodegen/pkg/workspace"
)

const decideOK = `{"model":"jev-1.13.0","answers":{` +
	`"verdict":{"type":"choice","choice":"incomplete","probabilities":{"complete":0.3,"incomplete":0.7},"confidence":0.4},` +
	`"done":{"type":"noul","noul":0.25}},"usage":{"input_tokens":300,"output_tokens":0}}`

func decideFixture(t *testing.T, upstream http.HandlerFunc) (*DecideExecutor, *orchestrator.Context, *workspace.Workspace) {
	t.Helper()
	t.Setenv("TYPESAFE_API_KEY", "") // the fixture key must win; never a developer's real key
	srv := httptest.NewServer(upstream)
	t.Cleanup(srv.Close)
	s := settings.GetDefaultSettings()
	s.Defaults.Jev = settings.JevDefaults{BaseURL: srv.URL, Model: "jev-1.13.0", TimeoutSeconds: 2, MaxConcurrent: 2, APIKey: "unit-key"}
	e := &DecideExecutor{}
	e.SetSettings(s)

	ws, err := workspace.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := orchestrator.NewContext(context.Background(), map[string]string{"task": "build a CLI"})
	// A prior tool step whose stdout lives in the workspace output file.
	path, err := ws.WriteOutput("build", map[string]interface{}{"stdout": "COMPLETION SUMMARY: wrote " + t.TempDir() + "/src/main.go, tests not run"})
	if err != nil {
		t.Fatal(err)
	}
	ctx.SetResult("build", &envelope.Envelope{Status: envelope.StatusSuccess, OutputRef: path})
	return e, ctx, ws
}

func gateStep() *bundle.Step {
	return &bundle.Step{Name: "gate", Decide: &bundle.DecideDef{
		State: "OBJECTIVE:\n${inputs.task}\n\nREPORT:\n${steps.build.stdout}",
		Questions: map[string]jev.Question{
			"verdict": {Type: "choice", Instructions: "pick", Criteria: json.RawMessage(`{"complete":"c","incomplete":"i"}`)},
			"done":    {Type: "noul", Instructions: "done?"},
		},
	}}
}

func TestDecideExecutorResolvesScrubsAndWritesResults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	var seen jev.Request
	e, ctx, ws := decideFixture(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&seen)
		_, _ = w.Write([]byte(decideOK))
	})
	// Put the home path into the prior step's output so scrubbing is observable.
	path, _ := ws.WriteOutput("build", map[string]interface{}{"stdout": "wrote " + home + "/src/main.go"})
	ctx.SetResult("build", &envelope.Envelope{Status: envelope.StatusSuccess, OutputRef: path})

	env, err := e.Execute(gateStep(), ctx, ws)
	if err != nil {
		t.Fatal(err)
	}
	if env.Status != envelope.StatusSuccess {
		t.Fatalf("status = %s (%+v)", env.Status, env.Error)
	}
	if !strings.Contains(seen.State, "OBJECTIVE:\nbuild a CLI") || !strings.Contains(seen.State, "wrote ~/src/main.go") || strings.Contains(seen.State, home) {
		t.Fatalf("state sent = %q", seen.State)
	}
	if env.Result["verdict"] != "incomplete" || env.Result["done"] != "0.2500" {
		t.Fatalf("results = %+v", env.Result)
	}
	if _, present := env.Result["decision"]; present {
		t.Fatal("no synthesized decision key: conditions name the question")
	}
	if env.Result["verdict_confidence"] != "0.4000" {
		t.Fatalf("confidence = %v", env.Result["verdict_confidence"])
	}
	stdout, _ := env.Result["stdout"].(string)
	var shown struct {
		Answers map[string]string `json:"answers"`
	}
	if err := json.Unmarshal([]byte(stdout), &shown); err != nil || shown.Answers["verdict"] != "incomplete" || shown.Answers["done"] != "0.2500" {
		t.Fatalf("stdout = %q (err %v)", stdout, err)
	}
	if got := orchestrator.StepOutput(env); got != stdout {
		t.Fatalf("StepOutput = %q, want the stdout JSON so /v1/bundles responses show the decision", got)
	}
	// ${steps.gate.stdout} resolves through the output file, not Result, so
	// this proves the file landed and carries the same JSON.
	fileCtx := orchestrator.NewContext(context.Background(), nil)
	fileCtx.SetResult("gate", env)
	if got := fileCtx.Resolve("${steps.gate.stdout}"); got != stdout {
		t.Fatalf("${steps.gate.stdout} = %q, want the stdout JSON via the output file", got)
	}
	if c, ok := env.Result["cost_usd"].(float64); !ok || c != jev.CostUSD(300) || c <= 0 {
		t.Fatalf("cost = %v (%T), want %v", env.Result["cost_usd"], env.Result["cost_usd"], jev.CostUSD(300))
	}
	if in, ok := env.Result["input_tokens"].(int); !ok || in != 300 {
		t.Fatalf("input tokens = %v (%T)", env.Result["input_tokens"], env.Result["input_tokens"])
	}
	if env.Result["model"] != "jev-1.13.0" || env.Metrics == nil || env.Metrics.Tool != "jev" {
		t.Fatalf("model/tool = %v / %+v", env.Result["model"], env.Metrics)
	}
	if env.OutputRef == "" {
		t.Fatal("no output file written")
	}

	// The keys drive bundle conditions through the normal resolver.
	ctx.SetResult("gate", env)
	if !orchestrator.EvaluateCondition("${steps.gate.result.verdict} != 'complete'", ctx) {
		t.Fatal("choice condition should be true")
	}
	if orchestrator.EvaluateCondition("${steps.gate.result.done} >= 0.6", ctx) {
		t.Fatal("noul 0.25 must not pass a 0.6 threshold")
	}
	if !orchestrator.EvaluateCondition("${steps.gate.result.done} < 0.6", ctx) {
		t.Fatal("noul numeric compare failed")
	}
}

func TestDecideExecutorUnusableAnswersFail(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"missing question", `{"model":"jev-1.13.0","answers":{"verdict":{"type":"choice","choice":"complete","probabilities":{"complete":1,"incomplete":0},"confidence":1}},"usage":{"input_tokens":1,"output_tokens":0}}`, "done"},
		{"choice outside options", `{"model":"jev-1.13.0","answers":{"verdict":{"type":"choice","choice":"maybe","probabilities":{"complete":0.5,"incomplete":0.5},"confidence":0.1},"done":{"type":"noul","noul":0.5}},"usage":{"input_tokens":1,"output_tokens":0}}`, "maybe"},
		{"choice without distribution", `{"model":"jev-1.13.0","answers":{"verdict":{"type":"choice","choice":"complete"},"done":{"type":"noul","noul":0.5}},"usage":{"input_tokens":1,"output_tokens":0}}`, "probabilities"},
		{"empty answers", `{"model":"jev-1.13.0","answers":{},"usage":{"input_tokens":1,"output_tokens":0}}`, "no answers"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, ctx, ws := decideFixture(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(tc.body)) })
			env, err := e.Execute(gateStep(), ctx, ws)
			if err != nil {
				t.Fatal(err)
			}
			if env.Status != envelope.StatusFailure || env.Error == nil || env.Error.Code != "JEV_INVALID_RESPONSE" || !strings.Contains(env.Error.Message, tc.want) {
				t.Fatalf("envelope = %+v", env)
			}
		})
	}
}

func TestDecideExecutorUpstreamFailureIsFailureEnvelope(t *testing.T) {
	e, ctx, ws := decideFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(422)
		_, _ = w.Write([]byte(`{"error":{"message":"bad criteria unit-key"}}`))
	})
	env, err := e.Execute(gateStep(), ctx, ws)
	if err != nil {
		t.Fatalf("upstream failure must not abort with a Go error: %v", err)
	}
	if env.Status != envelope.StatusFailure || env.Error.Code != "JEV_ERROR" {
		t.Fatalf("envelope = %+v", env)
	}
	if strings.Contains(env.Error.Message, "unit-key") {
		t.Fatal("key leaked into envelope")
	}
}

func TestDecideExecutorFailsWhenOutputCannotBeWritten(t *testing.T) {
	e, ctx, ws := decideFixture(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(decideOK)) })
	ws.JobDir = filepath.Join(t.TempDir(), "missing") // outputs/ does not exist, so WriteOutput fails
	env, err := e.Execute(gateStep(), ctx, ws)
	if err != nil {
		t.Fatal(err)
	}
	if env.Status != envelope.StatusFailure || env.Error == nil || env.Error.Code != "OUTPUT_WRITE_FAILED" {
		t.Fatalf("envelope = %+v", env)
	}
}

func TestDecideExecutorUnconfigured(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "")
	e := &DecideExecutor{}
	e.SetSettings(settings.GetDefaultSettings()) // no key
	ws, _ := workspace.New(t.TempDir())
	ctx := orchestrator.NewContext(context.Background(), nil)
	env, err := e.Execute(gateStep(), ctx, ws)
	if err != nil || env.Status != envelope.StatusFailure || env.Error.Code != "JEV_UNCONFIGURED" {
		t.Fatalf("env = %+v err = %v", env, err)
	}
	nilSettings := &DecideExecutor{}
	env, err = nilSettings.Execute(gateStep(), ctx, ws)
	if err != nil || env.Error == nil || env.Error.Code != "JEV_UNCONFIGURED" {
		t.Fatalf("nil settings env = %+v err = %v", env, err)
	}
}

func TestDecideExecutorCancelledContextReturnsError(t *testing.T) {
	e, _, ws := decideFixture(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(decideOK)) })
	cctx, cancel := context.WithCancel(context.Background())
	cancel()
	ctx := orchestrator.NewContext(cctx, map[string]string{"task": "t"})
	path, _ := ws.WriteOutput("build", map[string]interface{}{"stdout": "x"})
	ctx.SetResult("build", &envelope.Envelope{Status: envelope.StatusSuccess, OutputRef: path})
	env, err := e.Execute(gateStep(), ctx, ws)
	if err == nil || env == nil || env.Error == nil || env.Error.Code != "CANCELLED" {
		t.Fatalf("env = %+v err = %v", env, err)
	}
}

func TestDecideExecutorCapsOversizedStateKeepingHeadAndTail(t *testing.T) {
	var seen jev.Request
	e, ctx, ws := decideFixture(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&seen)
		_, _ = w.Write([]byte(decideOK))
	})
	big := strings.Repeat("noise\n", jev.MaxStateBytes/6+10) + "COMPLETION SUMMARY: end"
	path, _ := ws.WriteOutput("build", map[string]interface{}{"stdout": big})
	ctx.SetResult("build", &envelope.Envelope{Status: envelope.StatusSuccess, OutputRef: path})
	step := gateStep()
	env, err := e.Execute(step, ctx, ws)
	if err != nil || env.Status != envelope.StatusSuccess {
		t.Fatalf("env = %+v err = %v", env, err)
	}
	budget := jev.StateBudget(step.Decide.Questions)
	if len(seen.State) > budget {
		t.Fatalf("state len %d exceeds budget %d", len(seen.State), budget)
	}
	if !strings.HasPrefix(seen.State, "OBJECTIVE:\nbuild a CLI") {
		t.Fatalf("head lost: %q", seen.State[:40])
	}
	if !strings.HasSuffix(seen.State, "COMPLETION SUMMARY: end") || !strings.Contains(seen.State, "[state truncated") {
		t.Fatalf("tail or marker lost: %q", seen.State[len(seen.State)-60:])
	}
	if env.Result["state_truncated"] != true {
		t.Fatalf("state_truncated = %v", env.Result["state_truncated"])
	}
}

func TestDispatcherRoutesDecideAndAcceptsSettings(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "")
	d := NewDispatcher(nil)
	d.SetSettings(settings.GetDefaultSettings())
	ws, _ := workspace.New(t.TempDir())
	ctx := orchestrator.NewContext(context.Background(), nil)
	env, err := d.Execute(gateStep(), ctx, ws)
	if err != nil || env == nil || env.Error == nil || env.Error.Code != "JEV_UNCONFIGURED" {
		t.Fatalf("dispatcher did not route to decide executor: env = %+v err = %v", env, err)
	}
}
```

- [ ] **Step 5.2: Run to confirm failure**

Run: `go test ./pkg/executor/ -run 'Decide|Dispatcher' -v`
Expected: compile errors for `DecideExecutor` and `SetSettings`.

- [ ] **Step 5.3: Write `pkg/executor/decide.go`**

```go
package executor

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"sync"
	"time"

	"rcodegen/pkg/bundle"
	"rcodegen/pkg/envelope"
	"rcodegen/pkg/jev"
	"rcodegen/pkg/orchestrator"
	"rcodegen/pkg/settings"
	"rcodegen/pkg/workspace"
)

// DecideExecutor runs a bundle decide step against the Jev decision model.
// It fails closed: an unconfigured client, an upstream error, or an
// unusable answer set yields a failure envelope, and the orchestrator stops
// the bundle as it does for any failed step. Load-time validation keeps
// decide steps out of parallel groups, where a failure would only be
// "partial" and the run would continue.
type DecideExecutor struct {
	mu        sync.Mutex
	settings  *settings.Settings
	client    *jev.Client
	clientErr error
	built     bool
}

// SetSettings receives the orchestrator's settings (runner.SettingsAware).
func (e *DecideExecutor) SetSettings(s *settings.Settings) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.settings = s
	e.client, e.clientErr, e.built = nil, nil, false
}

func (e *DecideExecutor) getClient() (*jev.Client, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.built {
		return e.client, e.clientErr
	}
	e.built = true
	if e.settings == nil {
		e.clientErr = jev.ErrNotConfigured
		return nil, e.clientErr
	}
	e.client, e.clientErr = jev.NewClient(e.settings.Defaults.Jev)
	return e.client, e.clientErr
}

func (e *DecideExecutor) Execute(step *bundle.Step, ctx *orchestrator.Context, ws *workspace.Workspace) (*envelope.Envelope, error) {
	builder := envelope.New().WithTool("jev")
	client, err := e.getClient()
	if err != nil {
		if errors.Is(err, jev.ErrNotConfigured) {
			return builder.Failure("JEV_UNCONFIGURED", err.Error()).Build(), nil
		}
		return builder.Failure("JEV_CONFIG_ERROR", err.Error()).Build(), nil
	}

	home, username := jev.LocalIdentity()
	state := jev.ScrubState(ctx.Resolve(step.Decide.State), home, username)
	state, truncated := jev.CapState(state, jev.StateBudget(step.Decide.Questions))

	start := time.Now()
	resp, err := client.Decide(ctx.Ctx(), step.Decide.Model, state, step.Decide.Questions)
	duration := time.Since(start)
	builder = builder.WithDuration(duration.Milliseconds())
	if err != nil {
		if runCtx := ctx.Ctx(); runCtx != nil && runCtx.Err() != nil {
			return builder.Failure("CANCELLED", "decision cancelled: "+err.Error()).Build(), runCtx.Err()
		}
		if errors.Is(err, jev.ErrInvalidResponse) {
			return builder.Failure("JEV_INVALID_RESPONSE", err.Error()).Build(), nil
		}
		return builder.Failure("JEV_ERROR", err.Error()).Build(), nil
	}

	// The client has already run jev.ValidateResponse, so every requested
	// question has a usable answer of the right type.
	names := make([]string, 0, len(step.Decide.Questions))
	for name := range step.Decide.Questions {
		names = append(names, name)
	}
	sort.Strings(names)
	values := make(map[string]string, len(names))
	for _, name := range names {
		values[name] = resp.Answers[name].Value()
	}
	// stdout is what StepOutput, /v1/bundles responses, and
	// ${steps.name.stdout} read. The answers sit under a fixed "answers"
	// key: a top-level object could otherwise carry "type":"result" if a
	// bundle named questions "type" and "result", and extractStreamingResult
	// would reduce it to one string.
	stdoutJSON, _ := json.Marshal(map[string]interface{}{"answers": values})

	sum := sha256.Sum256([]byte(state))
	outputPath, err := ws.WriteOutput(step.Name, map[string]interface{}{
		"stdout":          string(stdoutJSON),
		"model":           resp.Model,
		"answers":         resp.Answers,
		"usage":           resp.Usage,
		"state_bytes":     len(state),
		"state_sha256":    hex.EncodeToString(sum[:]),
		"state_truncated": truncated,
		"latency_ms":      duration.Milliseconds(),
	})
	if err != nil {
		// ${steps.name.stdout} resolves through the output file, so a step
		// whose file did not land is not a usable decision.
		return builder.Failure("OUTPUT_WRITE_FAILED", "write decision output: "+err.Error()).Build(), nil
	}

	builder = builder.Success().
		WithOutputRef(outputPath).
		WithResult("stdout", string(stdoutJSON)).
		WithResult("model", resp.Model).
		WithResult("cost_usd", jev.CostUSD(resp.Usage.InputTokens)).
		WithResult("input_tokens", resp.Usage.InputTokens).
		WithResult("output_tokens", resp.Usage.OutputTokens).
		WithResult("state_truncated", truncated)
	for _, name := range names {
		ans := resp.Answers[name]
		builder = builder.WithResult(name, values[name])
		if ans.Confidence != nil {
			builder = builder.WithResult(name+"_confidence", strconv.FormatFloat(*ans.Confidence, 'f', 4, 64))
		}
		if len(ans.Probabilities) > 0 {
			builder = builder.WithResult(name+"_probabilities", ans.Probabilities)
		}
	}
	return builder.Build(), nil
}
```

The import block for this file is `crypto/sha256`, `encoding/hex`, `encoding/json`, `errors`, `sort`, `strconv`, `sync`, `time`, plus `rcodegen/pkg/bundle`, `rcodegen/pkg/envelope`, `rcodegen/pkg/jev`, `rcodegen/pkg/orchestrator`, `rcodegen/pkg/settings`, `rcodegen/pkg/workspace`. `fmt` and `os` are not needed.

- [ ] **Step 5.4: Wire the dispatcher**

In `dispatcher.go`, add `"rcodegen/pkg/settings"` to the imports, add the field and case, and the settings hook:

```go
type Dispatcher struct {
	tool     *ToolExecutor
	parallel *ParallelExecutor
	merge    *MergeExecutor
	vote     *VoteExecutor
	decide   *DecideExecutor
}

func NewDispatcher(tools map[string]runner.Tool) *Dispatcher {
	d := &Dispatcher{
		tool:   &ToolExecutor{Tools: tools},
		merge:  &MergeExecutor{},
		vote:   &VoteExecutor{},
		decide: &DecideExecutor{},
	}
	d.parallel = &ParallelExecutor{Dispatcher: d}
	d.merge.ToolExecutor = d.tool
	return d
}

// SetSettings forwards orchestrator settings to executors that need them
// (runner.SettingsAware). The orchestrator calls it right after
// DispatcherFactory.
func (d *Dispatcher) SetSettings(s *settings.Settings) {
	d.decide.SetSettings(s)
}
```

In `Execute`, insert before `case step.Tool != "":`:

```go
	case step.Decide != nil:
		return d.decide.Execute(step, ctx, ws)
```

- [ ] **Step 5.5: Run the executor package**

Run: `go test ./pkg/executor/ -race -v`
Expected: all PASS, including the existing vote, merge, and cancel tests.

---

### Task 6: Orchestrator labels, model, and settings handoff

**Files:**
- Modify: `pkg/orchestrator/orchestrator.go:132-140` (event), `:210-241` (`New`), `:243-262` (`getStepModel`), `:335-338` (pre-loop models), `:402-410` (started event), `:455-468` (`StepStats`)
- Modify: `pkg/orchestrator/progress.go:91-104` (constructor), `:127-148` (`toolColor`)
- Modify: `pkg/orchestrator/live_display.go:72-84` (constructor)
- Test: `pkg/orchestrator/step_events_test.go`, `pkg/orchestrator/orchestrator_test.go`

**Interfaces:**
- Consumes: `bundle.Step.ToolLabel()`, `runner.SettingsAware`, `settings.Defaults.Jev.Model`.
- Produces: decide steps report `Tool: "jev"` in `StepEvent`, `StepStats`, and displays; `getStepModel("jev", "")` returns the configured Jev model.

- [ ] **Step 6.1: Write the failing tests**

Append to `pkg/orchestrator/step_events_test.go`:

```go
func TestRun_DecideStepIsLabeledJev(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	old := DispatcherFactory
	DispatcherFactory = func(tools map[string]runner.Tool) StepExecutor {
		return stubFuncExecutor(func(step *bundle.Step) (*envelope.Envelope, error) {
			return envelope.New().Success().
				WithResult("verdict", "complete").
				WithResult("cost_usd", 0.00001).
				WithResult("input_tokens", 12).
				WithResult("output_tokens", 0).
				WithResult("model", "jev-1.13.0").
				Build(), nil
		})
	}
	defer func() { DispatcherFactory = old }()

	s := settings.GetDefaultSettings()
	o := New(s)
	var events []StepEvent
	o.SetStepCallback(func(ev StepEvent) { events = append(events, ev) })
	b := &bundle.Bundle{Name: "gate-bundle", Steps: []bundle.Step{
		{Name: "gate", Decide: &bundle.DecideDef{State: "s", Questions: map[string]jev.Question{"q": {Type: "noul", Instructions: "x"}}}},
		{Name: "gate2", Decide: &bundle.DecideDef{State: "s", Model: "jev-latest", Questions: map[string]jev.Question{"q": {Type: "noul", Instructions: "x"}}}},
	}}
	if _, err := o.Run(b, map[string]string{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(events) != 4 {
		t.Fatalf("events = %d, want started+completed for two steps", len(events))
	}
	for _, ev := range events {
		if ev.Tool != "jev" {
			t.Fatalf("event %s tool = %q, want jev", ev.Type, ev.Tool)
		}
	}
	if events[0].Model != settings.DefaultJevModel {
		t.Fatalf("started model = %q, want configured default %q", events[0].Model, settings.DefaultJevModel)
	}
	if events[1].Model != "jev-1.13.0" || events[1].InputTokens != 12 {
		t.Fatalf("completed event = %+v", events[1])
	}
	if events[2].Model != "jev-latest" {
		t.Fatalf("started model for an explicit override = %q, want jev-latest", events[2].Model)
	}
}
```

Add `"rcodegen/pkg/jev"` to that file's imports.

Append to `pkg/orchestrator/orchestrator_test.go`:

```go
func TestGetStepModel_Jev(t *testing.T) {
	s := settings.GetDefaultSettings()
	s.Defaults.Jev.Model = "jev-1.13.0"
	o := New(s)
	if got := o.getStepModel("jev", ""); got != "jev-1.13.0" {
		t.Fatalf("default jev model = %q", got)
	}
	if got := o.getStepModel("jev", "jev-latest"); got != "jev-latest" {
		t.Fatalf("step override = %q", got)
	}
	if got := New(&settings.Settings{}).getStepModel("jev", ""); got != "" {
		t.Fatalf("empty settings should yield empty model, got %q", got)
	}
}

func TestNew_HandsSettingsToDispatcher(t *testing.T) {
	old := DispatcherFactory
	var received *settings.Settings
	DispatcherFactory = func(tools map[string]runner.Tool) StepExecutor {
		return &settingsRecorder{onSet: func(s *settings.Settings) { received = s }}
	}
	defer func() { DispatcherFactory = old }()
	s := settings.GetDefaultSettings()
	New(s)
	if received != s {
		t.Fatal("dispatcher did not receive settings")
	}
}

type settingsRecorder struct{ onSet func(*settings.Settings) }

func (r *settingsRecorder) SetSettings(s *settings.Settings) { r.onSet(s) }
func (r *settingsRecorder) Execute(*bundle.Step, *Context, *workspace.Workspace) (*envelope.Envelope, error) {
	return envelope.New().Success().Build(), nil
}
```

Add the imports that file needs: `"rcodegen/pkg/bundle"`, `"rcodegen/pkg/envelope"`, `"rcodegen/pkg/runner"`, `"rcodegen/pkg/workspace"`.

- [ ] **Step 6.2: Run to confirm failure**

Run: `go test ./pkg/orchestrator/ -run 'DecideStepIsLabeledJev|GetStepModel_Jev|HandsSettingsToDispatcher' -v`
Expected: FAIL. The label test reports `tool = ""`; the model test reports `""`; the handoff test reports no settings received.

- [ ] **Step 6.3: Hand settings to the dispatcher**

In `New`, directly after `dispatcher = DispatcherFactory(tools)` inside the `if DispatcherFactory != nil` block, add:

```go
		if aware, ok := dispatcher.(runner.SettingsAware); ok {
			aware.SetSettings(s)
		}
```

- [ ] **Step 6.4: Teach `getStepModel` about jev**

Replace the tail of `getStepModel` (from `// Use step's model if specified`) with:

```go
	// Use step's model if specified
	if stepModel != "" {
		return stepModel
	}
	// Decide steps take the configured Jev model; they are not in the tool map.
	if toolName == "jev" {
		if o.settings == nil {
			return ""
		}
		return o.settings.Defaults.Jev.Model
	}
	// Use the configured tool default.
	if tool, ok := o.tools[toolName]; ok {
		return tool.DefaultModelSetting()
	}
	return ""
```

- [ ] **Step 6.5: Use `ToolLabel` everywhere the loop reports a tool**

In `stepCompletedEvent`, change `Tool: step.Tool,` to `Tool: step.ToolLabel(),`.

In the pre-loop model pass (`:335-338`), change to:

```go
	for i, step := range b.Steps {
		display.SetStepModel(i, o.getStepModel(step.ToolLabel(), step.ModelHint()))
	}
```

In the started event and display block (`:402-410`), replace the three `execStep.Tool` uses and the two `execStep.Model` uses:

```go
		display.SetStepModel(i, o.getStepModel(execStep.ToolLabel(), execStep.ModelHint()))
		o.emitStep(StepEvent{
			Type:  StepEventStarted,
			Index: i,
			Name:  step.Name,
			Tool:  execStep.ToolLabel(),
			Model: o.getStepModel(execStep.ToolLabel(), execStep.ModelHint()),
		})
```

`ModelHint` returns `Decide.Model` for decide steps, so a step that asks for `jev-latest` is displayed as such from the start rather than as the configured default.

In the skipped event just below, change `Tool: execStep.Tool` to `Tool: execStep.ToolLabel()`.

In the `StepStats` literal (`:460-468`), change `Tool: execStep.Tool,` to `Tool: execStep.ToolLabel(),`. This is what makes the final report's `by_model` map group Jev cost under `jev` instead of an empty key.

Leave the `--opus-only` and `--flash` checks on `execStep.Tool` unchanged; they must only match real tools.

- [ ] **Step 6.6: Displays**

In `progress.go` `NewProgressDisplay`, replace the four lines that derive `tool` with:

```go
		tool := step.ToolLabel()
```

Do the same in `live_display.go` `NewLiveDisplay`. In `toolColor`, add before `case "parallel":`:

```go
	case "jev":
		return colorGreen
```

- [ ] **Step 6.7: Run the orchestrator package**

Run: `go test ./pkg/orchestrator/ -race -v`
Expected: all PASS.

---

### Task 7: Demonstration bundle, docs, and live smoke check

**Files:**
- Create: `pkg/bundle/builtin/gated-build.json`
- Modify: `README.md:261-275` (bundle table), `:303-313` (features), `:700-710` (endpoint table), `:860-880` (curl examples); `API.md:504-510`; `PRODUCT.md:52-70` (capabilities 5 and 7) and the settings paragraph

**Interfaces:**
- Consumes: everything above.

- [ ] **Step 7.1: Write the bundle**

`pkg/bundle/builtin/gated-build.json`:

```json
{
  "name": "gated-build",
  "description": "Claude builds, Jev triages the builder's own completion report, Claude finishes only if the triage says the objective is not met, Gemini tests. The gate is advisory triage, not verification",
  "inputs": [
    {"name": "output_dir", "required": false, "description": "Base directory for generated files (default from settings)"},
    {"name": "project_name", "required": true, "description": "Project subdirectory name"},
    {"name": "task", "required": true, "description": "What to build"}
  ],
  "steps": [
    {
      "name": "build",
      "tool": "claude",
      "task": "${inputs.task}\n\nRequirements:\n- Create well-structured, production-ready code\n- Include error handling and input validation\n- Add a README with usage instructions\n\nSave code to: ${inputs.output_dir}/${inputs.project_name}/src/\nSave README to: ${inputs.output_dir}/${inputs.project_name}/README.md\n\nEnd your reply with a section titled COMPLETION SUMMARY that lists: what was built, what you verified and how, and every known gap or unimplemented requirement. Write 'Known gaps: none' only after checking."
    },
    {
      "name": "gate",
      "decide": {
        "state": "OBJECTIVE:\n${inputs.task}\n\nBUILDER REPORT:\n${steps.build.stdout}",
        "questions": {
          "verdict": {
            "type": "choice",
            "instructions": "Compare the BUILDER REPORT against the OBJECTIVE. Choose complete only if every requirement in the objective is reported as built and verified. Choose verify_more if everything is reported as built but verification is missing or partial. Choose incomplete if any requirement is missing, deferred, or listed as a gap.",
            "criteria": {
              "complete": "Every requirement is reported as built and verified.",
              "verify_more": "Built, but verification is missing or partial.",
              "incomplete": "At least one requirement is missing or listed as a gap."
            }
          },
          "has_known_gaps": {
            "type": "noul",
            "instructions": "Does the BUILDER REPORT list any known gaps, TODOs, or unimplemented requirements?"
          }
        }
      }
    },
    {
      "name": "finish",
      "if": "${steps.gate.result.verdict} != 'complete'",
      "tool": "claude",
      "task": "A completion judge rated the previous build attempt as: ${steps.gate.result.verdict} (known gaps probability ${steps.gate.result.has_known_gaps}).\n\nObjective:\n${inputs.task}\n\nPrevious builder report:\n${steps.build.stdout}\n\n1. Read the code in ${inputs.output_dir}/${inputs.project_name}/src/\n2. Implement every missing requirement and run the verification that was skipped\n3. End with an updated COMPLETION SUMMARY in the same format"
    },
    {
      "name": "test",
      "tool": "gemini",
      "task": "Test the implementation.\n\n1. Navigate to ${inputs.output_dir}/${inputs.project_name}/\n2. Run the code against sample inputs\n3. Verify it works correctly\n4. Note any issues\n\nSave test results to: ${inputs.output_dir}/${inputs.project_name}/test-results.md"
    }
  ]
}
```

Verify it loads and validates: `go test ./pkg/bundle/ -run TestBuiltinBundlesValidate -v` (PASS) and `go run ./cmd/rcodegen list 2>/dev/null | grep gated-build` or the equivalent list command in `cmd/rcodegen/main.go:153`.

What this bundle does and does not prove: the gate judges the builder's own COMPLETION SUMMARY, which is the builder's account, not an independent check, and nothing judges again after `finish`. That is deliberate for v1: it demonstrates the mechanics and is honest about being triage. Adding a second gate after `finish` needs a way to reference a possibly-skipped step's result, which the resolver does not have today.

- [ ] **Step 7.2: README**

In the bundle table after the `summary` row, add:

```markdown
| `gated-build` | Claude builds, Jev judges completion, Claude finishes only if the judge says the objective is not met, Gemini tests |
```

Change "There are 9 built-in bundles" to "There are 10 built-in bundles".

In Bundle Features, add a bullet after **Conditional steps**:

```markdown
- **Decision gates** -- `decide` steps send scrubbed, capped state plus typed questions to TypeSafe's Jev decision model and expose the answers as `${steps.name.result.<question>}` for `if` conditions (decide steps fail closed and cannot sit inside `parallel`); see `gated-build`
```

Extend the step-types sentence: after "`vote` selects among named outputs (...)," insert "`decide` asks the Jev decision model typed `noul`, `choice`, or `score` questions about resolved state,".

In the HTTP endpoint table after the `/v1/bundles/{name}` POST row, add:

```markdown
| `/v1/decisions` | POST | Typed decisions from TypeSafe Jev: `state` + `questions` in, calibrated `answers` + `cost_usd` out. Requires `TYPESAFE_API_KEY` in rserve's environment |
```

After the bundle curl examples, add:

````markdown
```bash
# Ask Jev whether a report says the work is complete
curl -X POST http://127.0.0.1:14261/v1/decisions \
  -H "Content-Type: application/json" \
  -d '{"state":"OBJECTIVE: add retries\nREPORT: added retries; tests not run",
       "questions":{"verdict":{"type":"choice","instructions":"Is the objective met?",
       "criteria":{"complete":"built and verified","incomplete":"missing or unverified"}}}}'
```
````

Add `jev` to the settings example block wherever the README reproduces `settings.json.example`.

- [ ] **Step 7.3: API.md**

After the `/v1/bundles` section, add:

```markdown
### POST /v1/decisions

Typed, calibrated decisions from TypeSafe's Jev model, proxied so the vendor key stays on the server. Body: `state` (text; the budget is 64 KiB minus the largest question, after scrubbing), `questions` (map of name to `{type: noul|choice|score, instructions, criteria}`; names match `^[A-Za-z][A-Za-z0-9_-]{0,63}$`), optional `model` (default `jev-1.13.0`). Response: `answers` keyed by question with `noul` (0 to 1), `choice` (one of the supplied options), or `score` (a fractional level position from 0 to the top level index) plus `probabilities` and `confidence` where the type carries them, `usage`, `cost_usd`, `state_bytes`, `latency_ms`, and the echoed `correlation_id`. rserve validates every answer against the questions and refuses an upstream reply that is missing, mistyped, or out of range. Decisions use their own concurrency budget (`defaults.jev.max_concurrent`) and never occupy a run slot.

Error codes: `invalid_json` (400, also for trailing content after the JSON object), `invalid_decision` (400), `state_too_large` (413, for state over budget or a body over 512 KiB), `invalid_model` (400), `decisions_unconfigured` (503, not retryable), `decision_capacity` (503, retryable), `decision_upstream` (503, retryable), `decision_upstream_invalid` (502, retryable: the vendor replied but the answers failed validation), `decision_upstream_auth` (502, not retryable, for 401/403), `decision_upstream_rejected` (502, not retryable). `/health` reports a `decisions` object with `configured`, `model`, `total`, `failed`, `in_flight`, `max_concurrent`, and `cost_usd`.

Every state string has the server's home directory (plain and JSON-escaped) replaced by `~` and the account name by `~user` before it is sent; other identifiers in the state are the caller's responsibility. Jev classifies; it does not authorize. Its documented weaknesses include literal reading, no arithmetic, and susceptibility to instructions embedded in the state, so keep destructive steps behind deterministic checks.
```

- [ ] **Step 7.4: PRODUCT.md**

In capability 5, after the sentence listing step types, add: "A `decide` step asks TypeSafe's Jev decision model typed questions about resolved state and exposes the answers to `if` conditions, which is how `gated-build` re-runs the builder only when a judge says the objective is unmet."

In capability 7, after the file-upload sentence, add: "`POST /v1/decisions` proxies Jev decisions so any client gets typed, calibrated answers without holding the vendor key; decisions have their own concurrency budget and appear in `/health`."

In the "Layered configuration priority" bullet, add: "`TYPESAFE_API_KEY` supplies the Jev key and overrides `defaults.jev.api_key`; `RCODEGEN_MODEL` never fans out into the Jev model."

Update the bundle count and the "Current State" version line at the end, after the VERSION bump in Task 8.

- [ ] **Step 7.5: Live smoke check (only when the key is present)**

```bash
cd /Users/cliff/Desktop/_code/codegen_suite/rcodegen && make rserve
printenv TYPESAFE_API_KEY | wc -c
```

If the count is nonzero, in a second terminal: `./bin/rserve -port 14260` (loopback default), then:

```bash
curl -s -X POST http://127.0.0.1:14261/v1/decisions -H 'Content-Type: application/json' \
  -d '{"state":"OBJECTIVE: add retries\nREPORT: added retries with backoff; unit tests pass","questions":{"verdict":{"type":"choice","instructions":"Is the objective met?","criteria":{"complete":"built and verified","incomplete":"missing or unverified"}},"done":{"type":"noul","instructions":"Is the work complete?"}}}' | /opt/homebrew/bin/python3.13 -m json.tool
curl -s http://127.0.0.1:14261/health | /opt/homebrew/bin/python3.13 -m json.tool | grep -A8 '"decisions"'
```

Expected: `"choice": "complete"`, a `done` probability above 0.5, `cost_usd` around 0.00001, and `decisions.total` of 1 in health. Record the observed values in the CHANGELOG entry. If the key is absent, note in the CHANGELOG that the live check was skipped.

---

### Task 8: Build, version, changelog, commit

**Files:**
- Modify: `VERSION`, `CHANGELOG.md`, `PRODUCT.md` (version line, bundle count)

- [ ] **Step 8.1: Test the tree before versioning**

```bash
cd /Users/cliff/Desktop/_code/codegen_suite/rcodegen
go mod vendor
go vet ./...
go test -race ./...
git diff --check
```

Expected: `ok` for every package; `git diff --check` prints nothing.

- [ ] **Step 8.2: Bump VERSION (read it only now)**

```bash
cat VERSION
```

Increment the revision. If the revision is already 15, bump the minor version and reset the revision to 0. Write the new value back with a trailing newline.

- [ ] **Step 8.3: Build with the new version embedded**

`make` builds binaries with the `VERSION` embedded by `appversion.go`, so the build must follow the bump, not precede it.

```bash
make
./bin/rserve -v
make test
```

Expected: all eight binaries in `bin/`, `rserve -v` prints the new version, `make test` passes.

- [ ] **Step 8.4: CHANGELOG entry**

Prepend under `# Changelog`, using the new version and today's date:

```markdown
## <new version> — 2026-09-22
- **Added TypeSafe Jev decisions to rserve and bundles.** `POST /v1/decisions` proxies the Jev decision model (`state` + typed `noul`/`choice`/`score` questions in, calibrated `answers` + `cost_usd` out) with the vendor key resolved from `TYPESAFE_API_KEY` at client construction and never written into settings, `RSERVE_TOKEN` auth, home-directory and account-name scrubbing, a 64 KiB state budget that subtracts the largest question, answer validation against the questions (type, option membership, ranges, and a probability distribution over exactly the expected keys), retries on 429/529 with cancellation-aware backoff, a separate `defaults.jev.max_concurrent` budget that never occupies a run slot, eight classified error codes, and a `decisions` block in `/health`
- **Bundles gain a `decide` step.** State is resolved, scrubbed, and head-plus-tail capped; answers land in `${steps.name.result.<question>}` (plus `*_confidence`, `*_probabilities`, and a JSON `stdout` so `/v1/bundles` responses show the decision) so `if` conditions can branch. Decide steps fail closed, are rejected inside `parallel` at load time (a parallel child failure is only `partial`), may not use reserved result keys as question names, and are labeled `jev` in displays, step events, and `by_model` cost grouping. New builtin `gated-build` re-runs the builder only when the triage gate rates the objective unmet
- New `pkg/jev` client (no new dependencies); `defaults.jev` settings block with `RCODEGEN_JEV_MODEL`/`RCODEGEN_JEV_BASE_URL` overrides; `RCODEGEN_MODEL` does not fan out to Jev. No caching or replay: TypeSafe does not document Jev as deterministic. The judge's reliability on real reports is unmeasured; `gated-build` is triage, not verification. Docs: README, API.md, PRODUCT.md, settings.json.example
- Verified with `go vet ./...`, `make`, and the full `make test` race suite. Live smoke check against api.typesafe.ai: <observed verdict, noul, cost, or "skipped, no key">
- Agent: Codex:GPT-6
```

Update `PRODUCT.md`'s "Current State" version line to the new version and the built-in bundle count from nine to ten.

- [ ] **Step 8.5: Commit and push**

```bash
git add -A
git commit -m "Enable typed decision gates without distributing vendor credentials

Share a bounded Jev client between rserve and bundles.

Confidence: high
Scope-risk: moderate
Tested: Unit, fake-upstream integration, race tests, static analysis
Agent: Codex:GPT-6"
git push
```

Expected: push succeeds to `origin/master`. Follow the workspace Lore commit protocol and push the current branch to its configured upstream.

---

## Self-review notes

- Every Design requirement maps to a task: endpoint (3), settings and key resolution (1), client with scrubbing, budget, cap, retries, response validation, cost (2), bundle schema, validation, reserved names, parallel exclusion (4), executor and result keys (5), labels and cost grouping (6), demo bundle and docs (7), version then build then commit (8).
- Names are consistent across tasks: `jev.Question`, `jev.Answer.Value`, `jev.ValidateQuestions`, `jev.ValidateResponse`, `jev.ErrInvalidResponse`, `jev.LocalIdentity`, `jev.ScrubState(state, home, username)`, `jev.CapState`, `jev.StateBudget`, `jev.QuestionBytes`, `jev.MaxStateBytes`, `jev.MaxQuestionBytes`, `jev.MaxQuestionsTotalBytes`, `jev.NewClient`, `Client.Decide`, `Client.DecideWithSleep`, `jev.Sleeper`, `jev.Sleep`, `jev.CostUSD`, `Step.ModelHint`, `jev.APIError`, `jev.ErrNotConfigured`, `settings.JevDefaults.ResolveAPIKey`, `bundle.DecideDef`, `Step.ToolLabel`, `Bundle.Validate`, `executor.DecideExecutor`, `Dispatcher.SetSettings`, `decisionGate`, `DecisionRequest`, `DecisionResponse`, `DecisionHealth`.
- Review Focus items 1 to 11 are pinned in Tasks 2, 2 and 5, 5, 2, 3, 4, 4, 1, 2 and 3 and 5, 2, and 4 respectively.
- Review history, pass one: a gate inside `parallel` would not stop the bundle, answers were not validated, the cap overshot its limit and dropped the head, the env key could be persisted by `LoadOrSetup`, question names could clobber result keys, the privacy claim overstated the scrubber, a test contradicted the `decision` fallback, decide output was invisible to `StepOutput`, and the build preceded the version bump. All addressed; the `decision` convenience key was removed rather than repaired.
- Review history, pass three: `cutRuneEnd` kept partial three- and four-byte runes because a lone continuation byte counts as a full error rune; two tests were flaky or wrong (exact float equality on cost, a fixture with two defects under random map order); a failed `WriteOutput` still produced a success envelope while `${steps.name.stdout}` reads the file; started events showed the configured model instead of a step's `Decide.Model`; distribution checks tested presence but not correspondence; the HTTP decoder accepted trailing content and reported a body-limit hit as invalid JSON; retry backoff ignored cancellation; docs said six codes. All addressed: `cutRuneEnd` walks to the last rune start and checks `FullRuneInString` from there, with two-, three-, four-byte, and mixed tests; costs compare against `jev.CostUSD`; fixtures carry one defect; `OUTPUT_WRITE_FAILED` fails the step and `${steps.gate.stdout}` is asserted; `Step.ModelHint` feeds displays and started events; probabilities must cover exactly the option names or level indices and sum to about 1; the decoder requires EOF after one value and maps `MaxBytesError` to 413; `jev.Sleep` selects on the context; docs list all eight codes.
- Review history, pass two: fixtures could send and print a developer's real key; a JSON `null` score parsed as zero; a validated-but-unusable vendor reply was reported as a caller error; the tail cut skipped its rune-boundary scan; `StateBudget` could exceed its combined bound because questions were unbounded; `decide` plus `then` was accepted but never executed; questions named `type` and `result` could make the streaming parser collapse the stdout JSON. All addressed: fixtures clear the env and assertions never print headers; score parses through a pointer and choice/score answers require a distribution; `ErrInvalidResponse` maps to a retryable 502 `decision_upstream_invalid`; `cutRuneStart` receives the whole state; questions are capped at 16 KiB each and 48 KiB total, and the budget floor was removed; `then`/`else` on a decide step is rejected; stdout JSON nests under a fixed `answers` key.
- Follow-ups deliberately left out: gRPC `Decide` RPC, named presets, OTel spans and Kafka events for decisions, adding a gate to `build-review-audit`, measuring the judge and determinism before any caching, fixing the pre-existing env-key persistence for Ollama and LM Studio, and stopping bundles on tool failures inside `parallel`.

- Review history, pass four (Codex:GPT-6): null probabilities are rejected by a custom map decoder; permanent upstream failures retain non-retryability; the missing-question fixture contains one defect; cancellation is tested after entering backoff; body limits are classified at both decoder calls; stale decision-key wording is removed. Tests are run without pipelines that can hide their exit status.
