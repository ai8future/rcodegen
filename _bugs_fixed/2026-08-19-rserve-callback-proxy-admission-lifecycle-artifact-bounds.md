# rserve 4.3.3 — five defects in the async callback, lifecycle, cancellation, and artifact paths

**Date:** 2026-08-19
**Found by:** second-pass audit of the 4.3.0–4.3.2 changes
**Fixed in:** 4.3.3

Five defects, all in the async callback machinery added across 4.3.0–4.3.2 and the
clone-artifact collector added in 4.3.1. Two were release blockers; the other three
were correctness or availability defects in the same surfaces.

---

## 1. Plaintext callback delivery was routed through ambient HTTP proxies

**What was wrong.** `newCallbackClient` cloned `http.DefaultTransport`, which carries
`Proxy: http.ProxyFromEnvironment`, and replaced only `DialContext` for the plaintext
client. When `HTTP_PROXY` named a proxy, Go asked the dialer to connect to *the proxy*,
not to the callback host. So `dialPlaintextCallback` — the control that 4.3.2 documented
as the real enforcement of the private-address policy — resolved and approved the proxy,
while the absolute callback URL, the completion, the artifacts, and the caller's own
`callback_headers` were handed to that proxy to resolve and forward as it liked. For a
Windmill resume URL the URL is itself the credential.

**How it showed.** With `HTTP_PROXY` set to a live recording proxy, the proxy received the
full payload and the `Authorization` header the caller supplied. With a dead proxy
(`HTTP_PROXY=http://127.0.0.1:1`) the private-host callback test performed zero deliveries
and failed with `proxyconnect tcp: callback host did not resolve` — the dialer refusing the
*proxy's* address because the test's resolver stub did not answer for it, which is the
routing error in plain sight.

**Fix.** `transport.Proxy = nil` whenever the private-address dialer is installed
(`pkg/server/openai/asyncruns.go`, `newCallbackClient`). Plaintext delivery is direct or it
does not happen; there is deliberately no fallback through the default transport, so a
failed direct attempt stays failed and pollable. `https` keeps ordinary proxy behaviour —
its guarantee is the certificate, which a proxy cannot forge.

**Tests.** An adversarial pair: a *live* recording proxy proves the payload never reaches it
while a control transport built the old way proves the proxy is real and would have captured
URL, body, and header; and the DNS-rebinding refusal is re-asserted with the proxy variables
set. Plus a structural check that the plaintext transport has no proxy and the https one does.

---

## 2. Live async admission was unbounded

**What was wrong.** `submit` always retained a run and `submitAsyncRun` always answered `202`
and started a goroutine. The 100-result / 1-hour retention bounds apply only *after*
terminalization, and queued/running runs are deliberately never evicted, so there was no
admission bound at all. Accepted async work outlives the connection that submitted it, so
unlike a synchronous request it could not be bounded by the caller hanging up: a caller could
accumulate arbitrarily many goroutines and retained plans, each holding a task near the 10MB
body limit plus callback URL, headers, and work-directory strings.

This is capacity hardening rather than a live exposure — rserve is a bearer-authed,
single-tenant service on a LAN — but the bound belonged in the code regardless.

**Fix.** Two limits owned by `asyncRuns` and enforced under its mutex *before* a run ID
exists: live-run count (default `max(8, 4 × max_concurrent)`, `RSERVE_ASYNC_MAX_LIVE`) and
estimated retained request bytes (default 64MiB, `RSERVE_ASYNC_MAX_BYTES`). Both are needed —
a count alone permits a few maximum-size requests, a byte budget alone permits unbounded tiny
ones. Refusal is `503` + `Retry-After: 1` + `async_capacity` + `retryable: true`, with no
`run_id` and no goroutine. Reservations are released exactly once, when the execution
goroutine lets go of the plan. Limits are parsed once at startup and a non-positive or
unparseable value **stops the server** rather than being read as "unset". `/health` reports
`async_live`, `async_max_live`, `async_bytes`, `async_max_bytes`.

---

## 3. Terminal state and callback ownership were not atomic

**What was wrong.** The worker recorded its outcome through `finish` and then separately
entered `deliverOnce`; shutdown snapshotted live runs and independently entered the same
`deliverOnce` with a `server_shutdown` payload. `sync.Once` guaranteed one POST but not that
the POST matched what `finish` had stored. A run finishing after shutdown snapshotted it but
before the competing callback won could **store success and deliver `server_shutdown`** — a
false retryable failure that makes an external orchestrator redo work that had in fact
completed.

**Fix.** One store-owned primitive, `tryTerminalize(run, status, payload) bool`, which under
one hold of the mutex accepts a transition only from `queued`/`running`, sets terminal status
and times, derives the retained copy from the same payload, applies retention, and returns
`true` only to the winner. Only the winner delivers, and only the payload it won with.
Shutdown attempts terminalization per run and delivers only for the ones it won, so a run
that had already finished is left alone. `sync.Once` stays as defensive protection but is no
longer the state machine.

Cancellation cause is now explicit (`none | caller_http | caller_grpc | server_shutdown`), set
under the same mutex *before* the context is cancelled, rather than inferred from unrelated
state.

---

## 4. gRPC CancelRun acknowledged cancellations the async run then contradicted

**What was wrong.** The HTTP and gRPC servers share `RunRegistry`. Once an async run acquired
a slot it was registered under its published ID, so gRPC `CancelRun` found it and cancelled
*the acquisition's* child context. The async worker checks its own `run.ctx`, which was
untouched, so it classified the killed CLI as an ordinary completion and delivered
**`status: "success"`** — after gRPC had already told its caller `Cancelled: true`. Before slot
acquisition the run was absent from the registry entirely, so a queued async run was reported
as not found.

**Reproduced before fixing** (`pkg/server/asynccancel_test.go`): gRPC returned `Cancelled: true`
and the run published `"success"` with no error code.

**Fix.** The async store is the cancellation authority for its own IDs. `pkg/server` defines an
`AsyncCanceller` interface (the gRPC server cannot import the openai package, which imports it);
`cmd/rserve/main.go` injects the HTTP handler. `CancelRun` asks the async owner first and falls
back to the registry for ordinary synchronous runs. A queued async run is now cancellable; a
terminal one returns `cancelled: false` with "already finished" rather than claiming to have
killed work that had ended. HTTP `DELETE` and gRPC race to one outcome and one callback.

The gRPC `GetStatus` wording was corrected rather than the proto extended: it lists runs holding
a slot, and `/v1/runs` owns the full async lifecycle.

---

## 5. Artifact response caps did not bound artifact inspection work

**What was wrong.** The collector read every eligible changed file *in full* and only then
called `looksTextual`. A binary file consumes neither the artifact count nor the content
budget, so binaries were unbounded: with the 200,000-entry scan limit and the 512KB per-file
read limit, a run could create 200,000 hard links to one 512KB binary and cause roughly 100GB
of reading against 512KB of stored data — while holding the run slot.

**Measured, not assumed.** With read counters added to the unmodified collector: 300 hard links
to a 64KB binary produced **300 candidate reads and 19,660,800 bytes read** — every name read in
full. This contradicts the Phase-4 builder's report that probing stops at ~200 files; the audit
finding was correct. After the fix, the same tree with a shrunken test budget produces **25
candidates and 204,800 bytes** (the 8KB probe per candidate, nothing more).

**Fix.** An inspection budget independent of the response caps: 1,000 candidates and 16MiB read
per run, charged *before* each read. Each candidate is opened once, its text probe read and
charged first, and a binary is rejected without reading the remainder; a text file reserves its
advertised size against both the inspection and response budgets before the rest is read. A new
`inspection_cap` skip reason names files reached after the budget is spent, so nothing
disappears silently, and sorted path order keeps which files those are deterministic. The run
still succeeds.

The open also became `O_NONBLOCK` with a post-open `fstat` regular-file re-check: a candidate
that was a regular file at scan time and is a FIFO by the time it is opened would otherwise
block collection until a writer appeared — and the run that created the FIFO is the one deciding
whether one ever does.

---

## Files

- `pkg/server/openai/asyncruns.go` — proxy, admission, terminalization, causes, shutdown ordering
- `pkg/server/openai/cloneartifacts.go` — inspection budgets, probe-first reads, non-blocking open
- `pkg/server/openai/errorcodes.go` — `async_capacity` (retryable)
- `pkg/server/openai/handler.go` — `HandlerOption`/`WithAsyncLimits`, `/health` admission fields
- `pkg/server/openai/types.go` — `/health` fields
- `pkg/server/server.go` — `AsyncCanceller`, `CancelRun` routing
- `cmd/rserve/main.go` — env parsing that fails startup, canceller injection, effective limits logged
