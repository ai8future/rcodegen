# rserve + Windmill — Architecture Notes & Working Plan

Living document. Captures the architecture discussion and decisions for using
Windmill as the execution/control plane on top of rcodegen/rserve, so any
session can resume from here. Last updated: 2026-08-18 (rcodegen v4.2.10).

---

## 1. The big picture (three layers)

```
┌──────────────────────────────────────────────────────────────┐
│ OBSERVABILITY / EVALUATION            (future phase)         │
│ Langfuse v4 via OpenTelemetry                                │
│ cost & token analytics, LLM-judge + code evaluators,         │
│ monitors/alerts, full-text trace search                      │
└──────────────────────────▲───────────────────────────────────┘
                           │ OTLP (chassis OTel already wired in rserve
                           │       via OTEL_EXPORTER_OTLP_ENDPOINT)
┌──────────────────────────┴───────────────────────────────────┐
│ CONTROL PLANE / EXECUTION PLANE                              │
│ Windmill CE (self-hosted)                                    │
│ triggers (forms, webhooks, schedules), flow branches,        │
│ approval gates, retry policies, run history in HA Postgres,  │
│ operator UI, AI-agent steps                                  │
└──────────────────────────▲───────────────────────────────────┘
                           │ HTTPS gateway: POST /v1/bundles/{name}
                           │ (TLS + bearer token + X-Correlation-ID)
┌──────────────────────────┴───────────────────────────────────┐
│ MODEL EXECUTION                                              │
│ rserve → rcodegen bundles → claude / codex / gemini /        │
│ opencode / kilocode CLIs (subscription billing)              │
│ parallel steps, voting, merging, if/then/else, grading       │
└──────────────────────────────────────────────────────────────┘
```

**Division of responsibility** (the rule that keeps this clean):

- **Windmill owns**: when to run, input collection, branching on results,
  human approval, retries, schedules, cancellation, run state, publishing
  artifacts, notifications. Deterministic ops decisions.
- **rcodegen/rserve owns**: everything model-related — which tools run, in
  what order, parallel fan-out, voting, session reuse, cost capture. AI
  decisions live *inside bundles*.
- **Langfuse owns (later)**: telemetry, evals, cost analytics, alerting
  thresholds. Never the authoritative source of workflow or business state.
- **hotfolderd is retired** for this path. It existed to turn a synced
  filesystem into a job queue; Windmill *is* a job queue with a UI. Ported
  lessons: provider-limit hard-stop classification, content-hash idempotency,
  settle/debounce semantics (only needed if a folder-drop UX ever returns).

Why not LangChain / LangGraph / Temporal: our models are driven through
**subscription CLIs**, not per-token API SDKs. rcodegen bundles already are the
agent runtime; Windmill already provides durability, gates, scheduling, and a
UI. Adopting a Python agent framework would mean re-plumbing CLI execution and
paying API-token prices for what subscriptions cover. Revisit Temporal only if
runs someday span days, cross many services, and need signal-heavy workflows.

---

## 2. The live environment (facts)

### Windmill
- **Community Edition v1.790.1**, Helm chart 4.0.241, single-node k3s VM.
- URL: `http://windmill.10.0.4.224.nip.io` (plain http, LAN only).
  Fallback: `curl -H 'Host: windmill.10.0.4.224.nip.io' http://10.0.4.224/...`
- Workspace: **AOWS** (`aows`), owner cliff@cliffshaw.com (super-admin).
  Password in macOS Keychain on the Mac Studio: service `windmill-z2-admin`.
- Durable state: external z2 HA Postgres (HAProxy write endpoint
  `10.0.4.222:5433`, db `windmill`); DB URL lives in k8s secret
  `windmill/windmill-database-url`.
- VM: Proxmox VMID **103** (`windmill-k3s`) on `root@z2prox`.
  ⚠️ **Autostart is NOT set** — after a z2prox reboot Windmill stays down
  until `qm start 103`. Action item below.
- Health: 5-min LaunchAgent `com.ai8.windmill-z2-health` on the Mac Studio,
  logs to `~/Library/Logs/windmill-z2-health.jsonl` (logs only, no paging).
- CE gotchas: worker tags via `WORKER_TAGS` env work; worker-group UI,
  concurrency-key limits, Kafka/NATS/SQS triggers, audit logs are EE.
- UI note: no "Flows" sidebar item — everything is under **Home**
  (+ Script / + Flow / + App). Flow editor step picker contains the special
  steps: **AI Agent, Approval/Suspend, Branches, For-loops**.

### rserve (Mac Studio)
- Ports: gRPC **14260**, HTTP **14261**. Keep rserve bound to
  **`127.0.0.1`**. Both native listeners are plaintext; gRPC is unauthenticated
  unless `RSERVE_TOKEN` is set, and even then loopback peers are exempt.
- Remote access: expose only HTTP 14261 through an authenticated TLS reverse
  proxy or equivalent encrypted tunnel. Do not expose native gRPC 14260 to the
  LAN. Restrict the gateway to the Windmill VM at the host firewall as defense
  in depth.
- Auth: set `RSERVE_TOKEN` → requires `Authorization: Bearer <token>` on all
  HTTP endpoints except `/health`, and the same credential in the gRPC
  `authorization` metadata key for non-loopback peers (reflection and
  `grpc.health.v1` stay open). Keep it enabled behind the TLS gateway. The
  token bounds who may drive the server; it is not TLS, so it does not make
  exposing native gRPC to the LAN a good idea.
- Optional containment: `RSERVE_WORK_ROOT` restricts every `work_dir` to one
  parent directory. It must be absolute; use `/Users/cliff/rcodegen-runs`.
- OpenTelemetry: chassis OTel initializes when
  `OTEL_EXPORTER_OTLP_ENDPOINT` is set — the Langfuse hook-up point later.

---

## 3. The API contract

Windmill talks to rserve through the authenticated HTTPS gateway. The gateway
forwards to rserve's loopback HTTP listener. Full details in README; summary:

| Endpoint | Purpose |
|---|---|
| `GET /v1/bundles` | List bundles + declared inputs |
| `GET /v1/bundles/{name}` | Full step DAG (parallel, vote/merge, if/then/else) — render/introspect |
| `POST /v1/bundles/{name}` | Run a bundle |
| `POST /v1/chat/completions` | Single-tool runs, multi-turn via `session_id` (OpenAI-compatible) |
| `GET /health` | Open (no auth) — monitoring |

`POST /v1/bundles/{name}` essentials:

- **Request**: `{"inputs": {...}, "work_dir": "/abs/path", "stream": bool,
  "options": {"opus_only": bool, "flash_only": bool}}`
- **`X-Correlation-ID` header**: pass the Windmill job UUID. Echoed in
  response body + header, attached to the run registry (visible in
  `GetStatus`), and later the OTel/Langfuse trace attribute. This is the
  cross-system trace identity.
- **Response**: `status`, `steps[]` (per-step status/cost/tokens/duration +
  output, 64KB cap), **`output`** (last successful step's output — the
  verdict/answer channel Windmill branches on), `artifacts[]` (text files
  created/modified under `work_dir`, returned **inline** — 512KB/file,
  2MB/response, 100 files max), `total_cost_usd`, `usage`, `job_id`.
- **Streaming**: `"stream": true` → SSE `step_started` / `step_completed` /
  `step_skipped`, then `bundle_completed` with the full response. For live
  per-step progress in Windmill run logs on long bundles.
- **Cancellation**: client disconnect or gRPC `CancelRun` stops the run
  between steps and kills the in-flight step's CLI process (direct child
  only).
- **Concurrency**: at capacity, requests **queue** for a slot
  (`-max-concurrent`, default 3); 503 only if the caller cancels while
  waiting. This queueing is deliberate — rserve is the quota governor for
  subscription usage. Windmill flow steps should set a generous timeout
  (e.g. `curl -m 3600`).
- **Status mapping**: missing input → 400, unknown bundle → 404,
  bundle-logic failure → 200 + `"status":"failure"` (a *result*, not an
  infra error — branch on it, don't retry it).

Canonical step call from a Windmill Bash step:

```bash
curl -sS -m 3600 \
  -H "Authorization: Bearer $RSERVE_TOKEN" \
  -H "X-Correlation-ID: $WM_JOB_ID" \
  -H "Content-Type: application/json" \
  -d "{\"inputs\":{\"topic\":\"$TOPIC\"},\"work_dir\":\"/Users/cliff/rcodegen-runs/$WM_JOB_ID\"}" \
  "https://<rserve-gateway>/v1/bundles/research-report"
```

---

## 4. Integration patterns (three, in order of maturity)

1. **Bash/Python step → rserve bundle** *(primary; use now)*. The bundle name
   is the whole contract. Windmill supplies form/branches/approval around it.
   Keeps model usage on CLI subscriptions.
2. **Windmill AI-agent step with rcodegen as a tool**. Attach a tiny
   workspace script `run_rcodegen(bundle, inputs)` as a tool; the agent step
   (any provider) decides *when* to fire bundles. Native "AI-decided control
   flow" — but the agent step itself bills **API tokens** via a provider
   resource.
3. **AI-agent step pointed at rserve as a custom OpenAI endpoint** *(phase 2)*.
   Routes agent-step reasoning through subscription CLIs. Works today for
   plain LLM calls; the agent step's tool-calling loop needs rserve to
   implement OpenAI function-calling — a future rserve enhancement.

Economics rule of thumb: patterns 1–2's bundle work rides subscriptions;
anything through a Windmill provider resource rides API tokens.

---

## 5. The pilot flow: `research_report` (to build in AOWS)

```
Input form (topic, model, effort)
  → [Bash step] POST /v1/bundles/research-report   (correlation = job UUID)
  → [small step] parse .output for grade / JSON verdict
  → [Branch]
       pass    → publish artifacts + notify → Result
       revise  → one more bundle call (revision loop, bounded)
       weak    → [Approval step] human reviews the artifact inline, decides
```

- Trigger it three ways from day one (they're the same flow in Windmill):
  UI form, webhook, nightly schedule over a topic list.
- **Publish step**: Windmill commits the inline artifacts to a dedicated
  private `reports` git repo (decision: control plane owns publishing;
  the approval gate reviews the actual document, not a pointer).
- Prereq: a generalized `research-report` bundle (parameterized topic +
  structured verdict emission) — the `article` bundle is the template, minus
  its hardcoded subject.

## 6. Decisions log (settled)

| Decision | Choice |
|---|---|
| Execution plane | Windmill CE (already running); no LangChain, no Temporal for now |
| hotfolderd | Retired for this path; lessons ported |
| Bundle invocation | HTTPS gateway → loopback HTTP `POST /v1/bundles/{name}` (built API; gateway required) |
| Artifact transport | **Inline in response**; Windmill publishes (option B) |
| Trace identity | `X-Correlation-ID` = Windmill job UUID, end to end |
| Concurrency-full behavior | Queue (rserve = quota governor), not fast-fail |
| Verdict/failure contract | **Deferred until the pilot flow teaches us** — keep minimal |
| Worker placement | Now: VM workers call the authenticated TLS gateway; native rserve stays loopback-only. Later: dedicated k3s agent-worker image (CLIs + rcodegen + persistent home for CLI auth). Mac Studio = bootstrap, not destination |
| Artifact home | Private `reports` git repo (recommended, not yet created) |
| Dropbox | Not a requirement; folder-drop UX abandoned |

## 7. Open questions / deferred design

- **Verdict contract**: standardize `{verdict: pass|revise|escalate, score,
  reasons[]}` emitted by every bundle's final step, lifted to a top-level
  response field → one generic Windmill branch pattern for all bundles.
  Design after the pilot runs.
- **Failure semantics on the wire**: provider-limit → distinct error code the
  flow routes to a no-retry/notify branch (port hotfolderd's
  `ProviderLimitReason` into rserve); transient vs logic failures mapped so
  Windmill's retry policy only touches the former.
- **Retry ownership** (from external review discussion): Windmill retries the
  outer job; bundles own intra-run retries; side-effecting steps need
  idempotency keys (`topic:stage:content-hash`); never both layers blindly
  retrying the same side effect.
- **Resume/retry-from-step** (dbt-parity item #5): persist step envelopes
  under a run ID, `POST /v1/bundles/{name}/resume`. Build when a real
  multi-dollar bundle fails at its last step, not before.
- **Langfuse phase**: rserve OTel emitter → Langfuse (trace per bundle run,
  span per step, scores from grades); evaluate on Langfuse Cloud with a
  throwaway project before committing to self-host (ClickHouse+Postgres+Redis).
  Langfuse replaces `pkg/tracking` (iTerm2 scraping), `.grades.json`
  analytics, and dashboard metrics — it never replaces Windmill or rserve.
- **rserve function-calling** (enables integration pattern 3).
- **Windmill assets**: register published reports as workspace assets for
  lineage (check CE vs EE gates first).

## 8. Ops checklist (before the pilot is "real")

- [ ] `ssh root@z2prox 'qm set 103 --onboot 1'` — Windmill must survive host reboots
- [ ] rserve as a LaunchAgent on the Mac Studio: keep `-bind 127.0.0.1`, set
      `RSERVE_TOKEN`, and set `RSERVE_WORK_ROOT=/Users/cliff/rcodegen-runs`
- [ ] Put HTTP 14261 behind an authenticated TLS gateway restricted to the
      Windmill VM; verify native gRPC 14260 is not reachable from the LAN
- [ ] Store the rserve token as a Windmill **variable/secret** in AOWS
- [ ] Create the private `reports` repo; give the flow push credentials (Windmill secret)
- [ ] Health LaunchAgent: add alerting (currently logs only)
- [ ] Author the `research-report` bundle (git-tracked in this repo, not `~/.rcodegen/bundles`)
- [ ] Build the `research_report` flow in AOWS; test form → run → approve → publish end to end

## 9. Ideas parking lot

- Windmill **App** as operator portal: bundle picker fed by `GET /v1/bundles`,
  DAG rendering from the detail endpoint, live step status from SSE.
- Nightly schedule over a topic list file/table → fan-out one flow run per topic.
- Flow-as-chat on the research flow for interactive report refinement.
- Langfuse **monitors** as gate inputs: quality-score threshold webhook →
  flow branch (evaluation brain feeding decision gates).
- `wowerpoint`/`pdfgen_svc` step after approval: report → shareable deck/PDF.
- rbatch integration: bulk report campaigns via Windmill fan-out vs rbatch —
  decide which owns N-topic batches.

---

*Provenance: consolidated from the 2026-08-17/18 architecture sessions
(hotfolderd study → Langfuse/execution-plane survey → Windmill selection →
bundle HTTP API build v4.2.3–4.2.6 + external review fixes).*
