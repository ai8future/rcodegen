# Bundle HTTP API review fixes (4.2.3–4.2.5 → fixed in 4.2.6)

External review of the new bundle HTTP API surfaced six issues; all verified
against source and fixed.

## 1. Per-step / top-level output empty in production (High)

The step collector read `Envelope.Result["stdout"]`, but production executors
persist stdout to the `OutputRef` JSON file (`pkg/executor/tool.go`) — the
envelope never carries a `stdout` result key. Tests used synthetic envelopes
containing the key, masking the mismatch, so the 4.2.4 `steps[].output` /
top-level `output` feature returned empty strings on real runs. It also missed
the stream-JSON unwrap step that `${steps.X.stdout}` resolution applies.

**Fix:** new exported `orchestrator.StepOutput(env)` mirrors context resolution
exactly: `Result["stdout"]` fallback first (synthetic/test path), then the
OutputRef file with executor-convention keys (`stdout`, `merged`, `decision`),
unwrapped via `extractStreamingResult`. Collector now uses it.

## 2. Artifact scan followed symlinks / could open FIFOs (High)

`snapshotWorkDir` accepted any non-directory entry; `readFileCapped` used plain
`os.Open`. A symlink written into work_dir could exfiltrate files from outside
it; opening a FIFO would block the response and pin a run slot.

**Fix:** scan now skips anything where `!d.Type().IsRegular()` (lstat-based, so
symlinks/FIFOs/sockets/devices are excluded at discovery). Regression test
creates a symlink to an outside file plus a FIFO and asserts neither is
collected. Residual TOCTOU window (regular file swapped for a symlink between
scan and read) accepted for the current threat model (inputs are our own tool
outputs on an authenticated endpoint).

## 3. HTTP bundle cancellation did not work (High)

The run context from `registry.Acquire` was discarded and the orchestrator
built its own `context.Background()`-rooted signal context; tool subprocesses
ran via `cmd.Run()` with no kill path. Client disconnects and `CancelRun`
therefore never stopped costly CLI processes (inherited gRPC limitation).

**Fix:** new `Orchestrator.RunWithContext(ctx, …)` (Run delegates with
Background); the step loop stops on parent cancellation, and
`executor.runWithContext` kills the in-flight step's process on ctx.Done
(returns envelope `CANCELLED`). HTTP passes the registry run context; gRPC
`RunBundle` passes `stream.Context()`. Limitation: only the direct child is
killed — grandchildren may survive.

## 4. "Concurrency full → 503" was inaccurate (Medium)

`RunRegistry.Acquire` blocks on a semaphore until a slot frees or the caller's
context cancels — it never returns an immediate at-capacity error. Queueing is
the desired behavior (rserve as quota governor); the documentation was wrong.

**Fix:** README now states requests queue, with 503 only if the client
cancels/disconnects while waiting.

## 5. work_dir unbounded: any path, unbounded scan metadata (Medium)

Any absolute path (including `/`) was accepted and recursively scanned twice;
content was capped but entry counts and path metadata were not.

**Fix:** scan capped at 10,000 tracked files (`filepath.SkipAll`), artifacts
capped at 100 entries, and optional `RSERVE_WORK_ROOT` env restricts every
work_dir to a configured parent (400 otherwise). Default remains unrestricted
for backward compatibility.

## 6. Conditional/error step events lacked metrics (Medium)

`stepCompletedEvent` (used for conditional steps and dispatcher errors) only
set status/duration despite per-step results advertising cost/tokens/model.

**Fix:** it now extracts `cost_usd`, `model`, `input_tokens`, `output_tokens`
from the envelope when present.

## Lesson

The output-plumbing bug (finding 1) came from testing the handler exclusively
against fakes that emitted hand-built envelopes — the fake encoded my
assumption instead of the executor's actual contract. When faking a producer,
derive the fake's payload shape from the producer's code (or add one
integration test through the real path).

Agent: Claude:Opus 4.8
