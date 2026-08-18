# rserve × Windmill Integration Features — Async Callbacks, Error Control, Observability

**Date:** August 18, 2026
**Status:** Proposed
**Origin:** Run-history analysis of 100 Windmill jobs in the `aows` workspace (aows_desk demo program). All flow-duration risk concentrates in rserve-backed steps (25–34s today, minutes-to-hours for real bundles) held on single synchronous HTTP connections; error responses carry no retryability signal; chat completions report no cost; queueing is invisible.

**Prerequisite:** the clone_work_dirs audit fixes (target 4.2.12) land first; every phase below rebases on top.

**Shape:** four server phases, each independently shippable with its own VERSION/CHANGELOG/release per AGENTS.md, followed by one Windmill-side adoption phase in `aows_suite/aows_desk`. Contracts below are pinned — implementers extend but do not redesign them.

---

## Phase 1 — Error control: retryability classification + correlation echo (target 4.2.13)

**Problem:** Windmill's native retry policy can't distinguish "retry me" (provider limit, CLI crash, timeout) from "doomed" (invalid_model, unsafe_symlink). Chat completions accept `X-Correlation-ID` but don't echo it or make runs findable by it (bundles-only today).

**Contract:**
- Error envelope gains one field: `{"error": {"message", "type", "code", "retryable": bool}}`.
- Classification table (single source of truth, one Go map + helper, unit-tested exhaustively):
  - `retryable: true` — provider/rate limits, CLI process crash or unexpected exit, CLI/network timeouts, transient clone failures (fs errors that are not policy rejections), slot-acquire interrupted by shutdown.
  - `retryable: false` — `unauthorized`, `empty_task`, `invalid_model`, `invalid_work_dir`, `unsafe_symlink`, `unsupported_git_worktree`, malformed request bodies, any 4xx validation class.
- Chat completions echo correlation: response header `X-Correlation-ID` + body field `"correlation_id"` (omitempty), mirroring the bundles behavior; the run registry entry records it (it already does for bundles — unify).

**Files:** `pkg/server/openai/types.go` (envelope + response field), the error-constructor site (`NewErrorResponse` and callers), `pkg/server/openai/handler.go` (echo header/body), registry correlation plumbing if chat path diverges from bundles.

**Tests:** exhaustive table test asserting every existing error code has an explicit classification (build fails on unclassified codes — add a completeness assertion, not a default); handler tests for echo on success, error, and streaming final chunk.

**Windmill acceptance check (live):** a flow step with Windmill `retry` configured retries a simulated provider-limit error and does NOT retry `invalid_model`.

---

## Phase 2 — Observability: cost/usage normalization + queue visibility (target 4.2.14)

**Problem:** codex reports `usage: null`; chat completions have no `cost_usd` at all (bundles have `total_cost_usd`). Queued requests (all slots busy) look identical to slow runs.

**Contract:**
- Chat completion response gains `"cost_usd"` (number, omitempty) and usage gains provenance: when the CLI reports tokens/cost, populate normally and set `"usage_source": "cli"`; when it cannot (codex), set `"usage_source": "unreported"` and omit fabricated zeros. Never invent numbers.
- Streaming: `queued` SSE event emitted when a request waits for a slot — `{"type": "queued", "position": N}` once on entry, and `{"type": "started"}` when the slot is acquired (only if a wait actually occurred). Non-streaming callers get response header `X-Queue-Wait-Ms` (omitted when zero).
- `/health` gains `"queued": N` alongside `active_runs`.

**Files:** per-tool cost/usage extraction lives with each tool adapter (`pkg/tools/*`) behind one interface method; queue events at the registry Acquire site; health handler.

**Tests:** per-tool usage extraction fixtures (claude reports, codex unreported); queue-position test with a saturated fake registry; health shape test.

**Windmill acceptance check (live):** digest flow logs show cost per run for claude models and explicit `unreported` for codex; a deliberately saturated rserve shows `queued` in step logs.

---

## Phase 3 — Async callback mode (target 4.3.0 — minor bump, new architecture)

**Problem:** synchronous-only chat completions couple three timeouts (HTTP read < module < instance), die with the connection (client disconnect cancels the run), and cannot survive a Windmill worker restart. This is the single biggest reliability gap for Desk-scale runs.

**Contract:**
- Request fields (chat completions first; bundles in a follow-up once proven): `"callback_url"` (string, https or http-on-LAN), optional `"callback_headers"` (map, e.g. bearer for non-Windmill receivers). Mutually exclusive with `"stream": true` → 400.
- With `callback_url`: server validates the request fully (Phase-1 classification applies), enqueues, responds **202** `{"run_id", "status": "queued", "correlation_id"}` immediately, and releases the connection. The run executes exactly as today.
- On completion (success OR failure), rserve POSTs the full completion JSON (same shape as the synchronous response, plus `"run_id"` and `"status": "success"|"failure"`) to `callback_url`: 10s timeout, 3 attempts with exponential backoff, then give up and log — the result remains available for polling regardless.
- Poll/result endpoints: `GET /v1/runs/{run_id}` → status (queued/running/success/failure + timings); `GET /v1/runs/{run_id}/result` → the retained completion JSON (404 after eviction); `GET /v1/runs?correlation_id=` → run_id lookup (Phase 1 plumbing).
- Result retention: in-memory, bounded (default: 100 results or 1 hour, LRU eviction, size-capped per result at the existing 64KB output discipline). **Explicitly non-durable** — fleet doctrine keeps durable state in Postgres, never in rserve; document this loudly.
- Cancellation: `DELETE /v1/runs/{run_id}` (or reuse gRPC CancelRun semantics over HTTP) replaces disconnect-cancels for async runs.

**Windmill pairing (the payoff):** a flow step submits with `callback_url` = its own Windmill **resume URL** (proven mechanism — approval demo), then suspends. rserve resumes the flow with the completion as the resume payload. No held connections, no timeout coupling, worker restarts survive, and the suspend timeout (e.g. 2h) becomes the only knob.

**Files:** `pkg/server/openai/handler.go` (fork sync/async path after validation), new `pkg/server/openai/asyncruns.go` (retention store + callback dispatcher + endpoints), registry additions for status-by-id, router registration.

**Tests:** 202 contract; callback delivered on success and on failure; callback receiver down → retries then poll still works; retention eviction; stream+callback → 400; cancellation via DELETE; race test on concurrent submits.

**Windmill acceptance check (live):** `demo_query_rserve` converted to submit-and-suspend (Phase 5) completes end to end with the connection released during the agent run (verify: no long-held HTTP connection in rserve logs; flow suspended state observed mid-run).

---

## Phase 4 — Artifacts from cloned work dirs (target 4.3.1)

**Problem:** agents can now write freely in their clone sandbox, but everything they write is destroyed at cleanup — the only output channel is message text.

**Contract:**
- Request: `"return_artifacts": true` — requires `"clone_work_dirs": true` (400 `invalid_request` otherwise).
- Before cleanup, collect files created or modified under the clone (diff against a post-clone manifest of paths+mtimes+sizes captured before the CLI starts): text files only, bundles' existing caps reused verbatim (512KB/file, 2MB/response, 100 files), binary and oversize files listed by name in `"artifacts_skipped"` with reasons.
- Response: `"artifacts": [{"path" (clone-relative), "content", "bytes"}]` — same shape as bundles' artifacts for consumer symmetry. Callback mode includes artifacts in the callback payload subject to the same caps.

**Files:** `pkg/server/openai/workdirclone.go` (manifest + collection before cleanup), types, handler wiring; reuse the bundle artifact collector if extractable.

**Tests:** created/modified/untouched file matrix; caps enforcement + skip reporting; binary detection; interaction with cleanup ordering (artifacts collected even when the run failed).

**Windmill acceptance check (live):** digest agent writes `report.md` in its sandbox; the flow receives it as an artifact and emails it.

---

## Phase 5 — Windmill-side adoption (aows_desk repo, after Phase 3)

1. Convert `demo_query_rserve.ask_agent` to submit-with-callback + suspend/resume; delete the 1800s read timeout and 1900s module timeout (suspend timeout 2h becomes the guard). Evidence file regenerated.
2. Add Windmill `retry` (constant, 2 attempts) to the rserve step, relying on Phase-1 `retryable` — verify live that a permanent error does not retry.
3. Surface `cost_usd` + `usage_source` in step results; add to the digest email footer ("this report cost $0.00x").
4. Update `EMAIL_DESIGN.md`/`windmill/AGENTS.md` pointers and the fleet pattern note: async callback is the standard rserve integration for any run that may exceed ~60s.

---

## Execution + governance

- One executor subagent per phase (opus, xhigh), same pipeline as the demo program: implement → self-verify → controller review → release per repo AGENTS.md; live Windmill acceptance checks are part of each phase's definition of done, evidenced in the aows_desk verification style.
- Phases 1+2 may be built by one agent in sequence (small, adjacent); Phase 3 is its own agent and review cycle; Phase 4 after 3 ships; Phase 5 in aows_desk.
- Each server phase ends with an rserve restart (controller-coordinated, active_runs==0, `RSERVE_ALLOW_INSECURE_REMOTE=1` in the launch line) — motivating the standing ops item: create the rserve LaunchAgent so restarts stop being hand-run.
- Out of scope: durable run storage (Postgres stays the record), OpenAI function-calling on rserve (separate roadmap item), bundle-path callback (follow-up after chat proves it), Langfuse export (later phase per RSERVE_WITH_WINDMILL.md).
