# rcodegen API Reference

rcodegen exposes APIs at three levels: a gRPC streaming service, an OpenAI-compatible HTTP REST API, and CLI binaries.

---

## Server: rserve

Start the server:

```bash
rserve                                    # defaults: gRPC 14260, HTTP 14261, localhost, 3 concurrent
rserve -port 9000 -max-concurrent 5       # custom port (HTTP is port+1)
rserve -bind 127.0.0.1                    # explicit loopback bind
```

| Flag | Description | Default |
|------|-------------|---------|
| `-port` | gRPC listen port (HTTP is port+1) | `14260` |
| `-bind` | Listen address; non-loopback values require `RSERVE_ALLOW_INSECURE_REMOTE=1` | `127.0.0.1` |
| `-max-concurrent` | Max simultaneous runs | `3` |
| `-session-ttl` | Session inactivity TTL in minutes (`0` disables expiry) | `30` |
| `-v` | Show version and exit | |

Concurrency is controlled by a shared semaphore and requests queue until a slot is available. `RSERVE_TOKEN` optionally protects both listeners: all HTTP endpoints except `/health`, and gRPC calls from non-loopback peers. Neither listener has TLS, so the token travels in the clear. Keep rserve on loopback and expose only the required API through authenticated TLS transport. Non-loopback binds are refused unless `RSERVE_ALLOW_INSECURE_REMOTE=1` explicitly acknowledges the risk.

At startup — after binding its port, before serving — rserve removes any `rserve-clone-*` scratch directories left in `os.TempDir()` by a previous process, and logs the number removed. In-process cleanup of these directories cannot run when the process is killed, and since retained run state is in-memory only, nothing on disk at startup can still be in use.

---

## gRPC API

**Service:** `rserve.RServe`
**Proto:** `proto/rserve.proto`
**Default port:** `14260`
**Transport:** Plain TCP (no TLS). gRPC reflection is enabled.

**Interceptors:** recovery, OpenTelemetry tracing (unary), metrics, logging, and — when `RSERVE_TOKEN` is set — bearer authentication (unary and stream).

**Authentication:** none by default. If `RSERVE_TOKEN` is set, calls from **non-loopback** peers must send the `authorization` metadata key as `Bearer <token>`; missing or wrong credentials get `Unauthenticated` (tokens are compared in constant time). Peers on `127.0.0.0/8`, `::1`, or a unix socket are exempt — they are already on the machine and can invoke the CLIs directly — so enabling the token does not disturb local clients. A peer whose address cannot be determined is treated as remote.

Reflection (`grpc.reflection.v1`/`v1alpha`) and health (`grpc.health.v1.Health`) stay open to all peers regardless of the token, matching the open HTTP `/health`; neither can run, inspect, or cancel work.

### RunTask (server-streaming)

Run a single AI tool against one or more working directories.

```protobuf
rpc RunTask(RunTaskRequest) returns (stream RunEvent);
```

**Request:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `tool` | string | yes | `"claude"`, `"codex"`, `"gemini"`, `"opencode"`, or `"kilocode"` |
| `task` | string | yes | Task text or shortcut name (`audit`, `test`, `fix`, `refactor`, `quick`, `grade`, `study`) |
| `model` | string | no | Model override (e.g., `"opus"`, `"gpt-5.5"`) |
| `max_budget` | string | no | USD budget string (e.g., `"10.00"`) |
| `work_dirs` | []string | yes | Target directories |
| `variables` | map[string]string | no | Template variables for task prompts |
| `session_id` | string | no | Resume a prior session |

**Response stream:** Sequence of `RunEvent` messages.

### RunBundle (server-streaming)

Run a named multi-tool bundle workflow.

```protobuf
rpc RunBundle(RunBundleRequest) returns (stream RunEvent);
```

**Request:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `bundle` | string | yes | Bundle name (e.g., `"build-review-audit"`) |
| `inputs` | map[string]string | no | Key/value inputs for the bundle |
| `opus_only` | bool | no | Force Claude Opus for all steps |
| `flash_only` | bool | no | Force Gemini Flash for all steps |

### ListTasks (unary)

```protobuf
rpc ListTasks(ListTasksRequest) returns (ListTasksResponse);
```

Returns all available task shortcuts (`tasks[]`) and bundle definitions (`bundles[]`).

### GetStatus (unary)

```protobuf
rpc GetStatus(GetStatusRequest) returns (GetStatusResponse);
```

**Response:**

| Field | Type | Description |
|-------|------|-------------|
| `version` | string | Server version |
| `active_runs` | int32 | Currently running tasks |
| `max_concurrent` | int32 | Concurrency limit |
| `runs` | []ActiveRun | Active run details (run_id, tool, task, started_at_ms) |

`runs` lists what currently **holds a run slot**, on either protocol. An async run that is still queued has been given a `run_id` but has not acquired a slot, so it does not appear here: the proto has no state to express it in. `GET /v1/runs` owns the full async lifecycle — queued, running, and terminal — and is the endpoint to poll for it.

### CancelRun (unary)

```protobuf
rpc CancelRun(CancelRunRequest) returns (CancelRunResponse);
```

| Field | Type | Description |
|-------|------|-------------|
| `run_id` | string | Run ID to cancel |

Returns `cancelled` (bool) and `message` (string).

**Async run IDs are cancellable here from the moment they are issued**, including while the run is still queued and has no registry entry. The async store is asked first and answers for every ID it owns; anything else is an ordinary synchronous run and is cancelled through the registry as before. Cancelling an async run through gRPC is equivalent to `DELETE /v1/runs/{run_id}`: the CLI subprocess is killed, the scratch clone removed, the slot freed, and a `run_cancelled` **failure** callback delivered — never a success. A run that has already reached a terminal state returns `cancelled: false` with a message saying so, rather than claiming to have killed work that had already ended.

### Streaming Event Types (RunEvent)

Every event carries `run_id` and `timestamp_ms`, plus one of:

| Event | Fields | Description |
|-------|--------|-------------|
| `InitEvent` | `session_id`, `tool`, `model` | Run started |
| `TextEvent` | `content` | Assistant text fragment |
| `ToolUseEvent` | `tool_name`, `summary` | Tool invocation by the AI |
| `StepProgressEvent` | `step_name`, `status`, `tool`, `model`, `cost_usd`, `duration_ms`, `tokens` | Bundle step completed |
| `ResultEvent` | `exit_code`, `output`, `usage` (TokenUsage), `total_cost_usd`, `grade`, `session_id` | Run finished |
| `ErrorEvent` | `message`, `code` | Error occurred |

### Health Check

The standard `grpc.health.v1.Health/Check` service is registered and always returns `SERVING`.

### Example with grpcurl

```bash
# Discover services
grpcurl -plaintext 127.0.0.1:14260 list

# Run a task
grpcurl -plaintext -d '{"tool":"claude","task":"audit","work_dirs":["/path/to/project"]}' \
  127.0.0.1:14260 rserve.RServe/RunTask

# Check status
grpcurl -plaintext 127.0.0.1:14260 rserve.RServe/GetStatus

# List available tasks and bundles
grpcurl -plaintext 127.0.0.1:14260 rserve.RServe/ListTasks

# From another host when RSERVE_TOKEN is set (loopback needs no token)
grpcurl -plaintext -H "authorization: Bearer $RSERVE_TOKEN" \
  192.168.1.10:14260 rserve.RServe/GetStatus
```

---

## OpenAI-Compatible HTTP API

**Default port:** `14261` (gRPC port + 1)
**Authentication:** none by default. If `RSERVE_TOKEN` is set, send `Authorization: Bearer <token>` on every endpoint except `/health`.

Local OpenAI SDK clients can connect by pointing the base URL to `http://127.0.0.1:14261`. Remote clients must use an authenticated TLS gateway or encrypted tunnel.

### POST /v1/chat/completions

Chat completion endpoint. Supports both streaming (SSE) and non-streaming modes.

**Request body** (JSON, max 10 MB):

```json
{
  "model": "claude:opus",
  "messages": [
    {"role": "system", "content": "optional system prompt"},
    {"role": "user", "content": "audit this codebase for security issues"}
  ],
  "stream": false,
  "work_dirs": ["/path/to/project"],
  "clone_work_dirs": false,
  "return_artifacts": false,
  "session_id": "optional-session-id",
  "callback_url": "https://windmill.example.com/api/w/aows/jobs_u/resume/...",
  "callback_headers": {"Authorization": "Bearer receiver-token"}
}
```

`callback_url` switches the request into [async callback mode](#async-callback-mode): the server answers `202` with a `run_id` and delivers the completion later. Everything below applies to both modes unless it says otherwise.

**Ephemeral work directories:** set `"clone_work_dirs": true` and each `work_dirs` entry is copied into a private scratch root (`$TMPDIR/rserve-clone-{run_id}-*`, mode 0700) before the CLI starts; the tool runs against the copy and the scratch root is removed when the run ends -- on success, on failure, and on client disconnect. Use it when concurrent runs share source trees, so agent state such as `.omc/` never lands in (or collides inside) the original directory. On macOS the copy is an APFS copy-on-write clone (`cp -Rc`), which is near-instant and consumes no extra space until written to; if the filesystem rejects that, rserve falls back to a real recursive copy. Dotfiles and dot-directories are included. The field defaults to `false` (the tool runs directly in the caller's directories), and is a no-op when `work_dirs` is absent. Responses report `"cloned_work_dirs": {n}` when cloning happened -- on the completion object for non-streaming requests, and on the final chunk for streaming ones. Bundle `work_dir` semantics are unaffected.

**What a source must look like.** Every `work_dirs` entry is checked before the request is queued for a run slot, so an unusable directory is rejected immediately rather than after waiting behind other work. A source that does not exist or is not a directory returns `400 invalid_work_dir`. Two further rules exist because a copy cannot isolate everything:

| Rule | Rejected with | Why |
|------|---------------|-----|
| No absolute symlinks anywhere in the tree, and no relative symlink whose target resolves outside the source root | `400 unsafe_symlink` | The copy preserves symlinks as symlinks, so an escaping link still points at the original tree and a write through it lands outside the scratch root |
| No regular file named `.git` anywhere in the tree, at any depth | `400 unsupported_git_worktree` | A `.git` file is a gitdir pointer -- a linked worktree at the root, a submodule checkout further down. The copy keeps using the original repository, so work inside the "isolated" clone mutates the caller's repository |

Both messages name the offending path relative to the source root. Relative symlinks that stay inside the source are fine and keep working inside the clone, where they resolve to the clone's own copies. A `.git` **directory** is self-contained and clones normally at any depth, so a vendored repository is fine; only the pointer file is refused. For a linked worktree, point `work_dirs` at the main worktree; for a tree with submodule checkouts, there is no copy-based isolation -- let git create the working copy instead. A symlinked source root is itself fine -- it is resolved before anything else is checked. Sources are re-checked once the run slot is held; a source that disappears in between fails the run with `500 clone_failed`.

**Artifacts: getting files back out of a clone.** A cloned run is a sandbox, and cleanup destroys everything in it — so an agent asked to "write the report to `report.md`" writes it into a directory nobody will ever read. Set `"return_artifacts": true` alongside `"clone_work_dirs": true` and the text files the run created or modified inside its clone come back inline on the response:

```json
{
  "artifacts": [
    {"path": "out/report.md", "content": "# Digest\n...", "bytes": 4182}
  ],
  "artifacts_skipped": [
    {"path": "out/chart.png", "reason": "binary"}
  ]
}
```

`return_artifacts` without `clone_work_dirs` (and at least one `work_dirs` entry) is `400 artifacts_require_clone`, not an empty list: an uncloned run writes into the caller's own tree, where the files already are, and answering `"artifacts": []` would read as "the agent wrote nothing".

- **What counts as written.** A manifest of every visible file's path, size, and mtime is taken after the clone completes and before the CLI starts. Anything created, or whose size or mtime moved, is an artifact. Files the source tree already held are not, and neither are deletions. Hidden entries are out of scope at any depth — a clone's `.git` index and a tool's own dot-directory state churn on every run and are not what a caller asked for.
- **Paths** are relative to the clone of the `work_dirs` entry that holds them. With more than one `work_dirs` entry they are prefixed with that clone's directory name (`alpha/notes.md`), since a bare relative path would be ambiguous across them.
- **Text only,** decided from the first 8KB: a NUL byte or invalid UTF-8 means binary. A rune straddling the 8KB boundary is not held against the file.
- **Caps** are the bundle artifact caps, reused verbatim: 512KB per file, 2MB of content per response, 100 artifacts. A file over the per-file cap is **skipped, not truncated** — an artifact that arrives is always the whole file. `artifacts_skipped` names everything found but not returned, with a reason: `binary`, `oversize`, `response_cap` (the 2MB budget was spent), `too_many_files`, `collection_error`, `scan_limit` (the clone holds more entries than one walk visits, so a created-or-modified diff over it cannot be trusted), or `inspection_cap` (below). The skip report is itself capped at 100 entries; further skips are logged only.
- **Inspection is bounded separately from the response**: at most **1,000 candidate files opened** and **16MiB read** per run, counting text probes and returned content alike. The response caps bound the answer, not the work of producing it — a binary file consumes neither the artifact count nor the content budget, so a clone holding many binaries, or many hard links to one binary, would otherwise be read in full once per name while collection holds the run slot. Each candidate is charged its 8KB text probe first and a file the probe calls binary is never read further. A file reached after the budget is spent is reported as `inspection_cap` rather than dropped silently, and paths are inspected in sorted order so which files those are is deterministic. The run itself still succeeds.
- **A failed run still reports its artifacts.** Half-written output is usually the most diagnostic thing a failed or cancelled run produced. The one exception is a failure where no clone was ever made — a rejected `work_dirs` entry, or a clone that could not be created — since there is then nothing to diff.
- **Collection runs strictly before cleanup** and can never fail a run: a clone or a file that cannot be read becomes a `collection_error` entry and a log line, not an error response. Each clone directory is pinned by an open descriptor taken before the CLI starts, so a run that replaces its own clone directory with a symlink cannot redirect collection elsewhere. Candidates are opened non-blocking and re-checked as regular files after opening, so a candidate that has become a FIFO or a device by the time it is read is refused instead of waiting for a writer that the run itself decides whether to provide.

**Model format:** `{tool}` or `{tool}:{model}` -- e.g., `claude`, `claude:opus`, `codex:gpt-5.6-sol`, `gemini`, `gemini:gemini-3-flash-preview`. Claude and Codex also accept a supported `-{effort}` suffix, such as `claude:opus-max`, `codex:gpt-5.6-luna-max`, or bare `codex-ultra` for the configured default model. OpenCode and KiloCode accept dynamic `provider/model` identifiers.

**Request headers:**

| Header | Description |
|--------|-------------|
| `X-Show-Tool-Use: true` | Include tool-use summaries in streaming text output |
| `X-Correlation-ID` | An external run identifier (e.g. a Windmill job UUID). Sanitized to `[A-Za-z0-9._-]`, capped at 128 characters, echoed as the `X-Correlation-ID` response header and the `"correlation_id"` body field, and attached to the run registry entry so `GetStatus` shows which external job owns each slot |

**Non-streaming response:**

```json
{
  "id": "chatcmpl-a1b2c3d4e5f6",
  "object": "chat.completion",
  "created": 1711800000,
  "model": "claude:opus",
  "choices": [{
    "index": 0,
    "message": {"role": "assistant", "content": "..."},
    "finish_reason": "stop"
  }],
  "usage": {
    "prompt_tokens": 1200,
    "completion_tokens": 3500,
    "total_tokens": 4700
  },
  "usage_source": "cli",
  "cost_usd": 0.0432,
  "session_id": "abc123",
  "cloned_work_dirs": 1,
  "correlation_id": "windmill-job-42",
  "artifacts": [{"path": "out/report.md", "content": "# Digest\n...", "bytes": 4182}],
  "artifacts_skipped": [{"path": "out/chart.png", "reason": "binary"}]
}
```

`correlation_id` is present only when the request carried `X-Correlation-ID`. `artifacts` and `artifacts_skipped` are present only when the request asked for them with `return_artifacts`, and each is omitted when empty.

**Usage and cost provenance:** `usage_source` says where the numbers came from and is always present on a completed response.

| `usage_source` | Meaning | Tools |
|----------------|---------|-------|
| `cli` | The tool's CLI reported usage; `usage` is populated, and `cost_usd` too when the CLI reports a cost | Claude (tokens + cost), Gemini (tokens only -- `cost_usd` is omitted, not zero) |
| `unreported` | The CLI publishes no usage at all. `usage` and `cost_usd` are **omitted entirely** | Codex (its JSON carries `usage: null`), OpenCode, KiloCode |

rserve never fabricates these numbers. An omitted `cost_usd` means "not measured", never "free", so a caller summing costs across runs must treat `unreported` as unknown rather than as zero. Extraction lives with each tool adapter (`runner.UsageReporter`), so a CLI that starts reporting usage is a change in one adapter.

**Streaming response** (`"stream": true`):

Server-Sent Events format. Each event is `data: {json chunk}\n\n`. Final message is `data: [DONE]\n\n`.

Response headers: `Content-Type: text/event-stream`, `Cache-Control: no-cache`, `Connection: keep-alive`, `X-Accel-Buffering: no`.

`session_id`, `cloned_work_dirs`, `correlation_id`, `usage`, `usage_source`, `cost_usd`, `artifacts`, and `artifacts_skipped` ride the final chunk (the one carrying `finish_reason`), not the content chunks. Artifacts have no earlier chunk to ride: the run's files exist only once it has finished.

**Queue visibility:** when every run slot is busy a request waits for one, which from outside looks exactly like a slow run. Streaming requests that wait receive two extra frames before any completion chunk, and only when a wait actually happened:

```
data: {"type": "queued", "position": 1}

data: {"type": "started"}
```

`position` counts from 1 and is this request's place in line at the moment it started waiting. A request that gets a slot immediately sees neither frame, so unqueued streams are unchanged. Non-streaming requests cannot be told mid-flight, so they get the total afterwards as the `X-Queue-Wait-Ms` response header, omitted when the wait was zero (including waits shorter than a millisecond). If the run fails after the queue frames have gone out, the error envelope arrives as a `data:` frame followed by `[DONE]` -- the HTTP status line is long gone by then.

**Error responses:**

```json
{"error": {"message": "unknown tool: foo", "type": "invalid_request_error", "code": "unknown_tool", "retryable": false}}
```

`retryable` is on every error response from every endpoint, and is always present -- `false` is a verdict, not a missing field. It answers one question: does sending the same request again stand a chance? Automatic retry policies (Windmill's per-step `retry`, for one) should branch on it rather than on the HTTP status, which cannot distinguish a transient `500` from a permanent one.

| `retryable` | Codes | Why |
|-------------|-------|-----|
| `false` | `method_not_allowed`, `unauthorized`, `invalid_json`, `unknown_tool`, `empty_task`, `invalid_model`, `invalid_effort`, `invalid_work_dir`, `unsafe_symlink`, `unsupported_git_worktree`, `unknown_bundle`, `missing_input`, `invalid_upload`, `missing_file`, `invalid_id`, `not_found`, `no_file_store`, `invalid_callback_url`, `invalid_callback_headers`, `callback_stream_conflict`, `artifacts_require_clone`, `run_cancelled` | The request is malformed, names something that does not exist, or is refused on policy grounds. It will be refused identically every time until the caller changes it |
| `true` | `concurrency_limit`, `async_capacity`, `clone_failed`, `work_dir_failed`, `bundle_failed`, `bundle_list_failed`, `save_failed`, `server_shutdown` | Transient: an interrupted slot wait, an async submission refused because the server already holds its configured limit of live async work, a filesystem failure, a CLI/provider failure (crash, unexpected exit, timeout, rate limit), or a server restart that caught the run in flight. The same request can succeed later |

| HTTP Status | Meaning |
|-------------|---------|
| `202` | Async submission accepted; the completion will be POSTed to `callback_url` |
| `400` | Bad request, unknown tool, invalid fixed model, unsupported model/effort combination, an unusable `callback_url`/`callback_headers`, `callback_url` together with `stream`, `return_artifacts` without a work-directory clone (`artifacts_require_clone`), or a `work_dirs` entry that is missing, not a directory, holds an escaping symlink (`unsafe_symlink`), or holds a git pointer file (`unsupported_git_worktree`) |
| `404` | Unknown run ID, or one whose result has been evicted from retention |
| `405` | Method not allowed |
| `500` | Work-directory clone failed, including a source that changed after validation |
| `503` | Request cancelled or disconnected while queued for a run slot, or an async submission refused by admission (`async_capacity`, or `server_shutdown` once shutdown has begun). Both carry `Retry-After: 1` and no `run_id` |

### Async callback mode

A synchronous completion holds one HTTP connection for the whole run, couples three timeouts (client read < caller's module timeout < instance timeout), and dies with the connection: a client disconnect cancels the run. For runs measured in minutes that is the largest source of avoidable failure. In callback mode the connection is released immediately and the run's lifecycle belongs to the server.

Send `callback_url` on a chat completion and rserve:

1. validates the request **in full** — model, effort, `work_dirs` policies, the callback URL itself — so a bad request still fails on this connection with the same `400` it always did;
2. admits it against the async limits below, or refuses it with a retryable `503` and no `run_id`;
3. answers `202` with the run's identity and releases the connection;
4. runs it exactly as the synchronous path would (same queue accounting, same `clone_work_dirs` behaviour, same completion shape);
5. POSTs the completion to `callback_url` when the run ends, success or failure;
6. retains the result for polling, whether or not the callback was delivered.

```json
{"run_id": "a1b2c3d4e5f60718", "status": "queued", "correlation_id": "windmill-job-42"}
```

`callback_url` cannot be combined with `"stream": true` (`400 callback_stream_conflict`) — a callback delivers the completion once, a stream delivers it incrementally.

**Admission.** Accepted async work outlives the connection that submitted it, so unlike a synchronous request it cannot be bounded by the caller hanging up. Two limits bound it instead, both checked under one lock **before** a `run_id` exists:

| Limit | Default | Override |
|-------|---------|----------|
| Live async runs — submitted and not yet finished | `max(8, 4 × max_concurrent)` | `RSERVE_ASYNC_MAX_LIVE` |
| Estimated retained request payload across those runs | 64MiB | `RSERVE_ASYNC_MAX_BYTES` |

Both are needed: a count alone lets a few requests near the 10MB body limit hold far more memory than intended, and a byte budget alone lets an unbounded number of tiny requests hold an unbounded number of goroutines. The byte figure is a conservative estimate of the strings a submission keeps alive — task text, callback URL and headers, model/session names, work-directory paths — plus a fixed per-run allowance, not exact heap accounting.

A submission past either limit is refused with `503`, `Retry-After: 1`, error code `async_capacity`, and `retryable: true`. **No `run_id` is issued and no goroutine is started**: there is nothing to poll, nothing to cancel, and no callback coming. A reservation is held until the run's execution goroutine lets go of the request, which is slightly longer than its terminal status — the memory is what is being bounded, not the lifecycle — and `/health` reports both the current usage and the configured ceilings. Once shutdown has begun, submissions are refused the same way with `server_shutdown`.

An override that is not a positive integer **stops the server at startup** rather than being ignored: both variables exist to bound memory, and silently treating `0` as "unset" would leave a server running without the bound its operator believed they had set. The effective limits are logged in the startup record.

Result retention is a separate budget (100 results or 1 hour) and is unchanged.

**Callback URL rules.** `https` is accepted for any host. Plain `http` is accepted only where the network itself is the boundary: a loopback or RFC1918 address (`127.0.0.0/8`, `::1`, `10/8`, `172.16/12`, `192.168/16`, IPv6 unique-local), the reserved `localhost` name, **or a hostname that resolves to one of those addresses** — which is what makes ingress-style URLs like `http://windmill.10.0.4.224.nip.io/api/w/.../resume/...` usable. Anything else is `400 invalid_callback_url`.

A hostname is checked twice, and the second check is the one that counts:

1. **At submit**, the name is resolved under a 2-second budget. If it fails to resolve, resolves to nothing, or answers with *any* address outside the accepted ranges, the submission is rejected with `400 invalid_callback_url` on the connection that sent it. This is fast feedback for a mistyped or public callback URL, nothing more.
2. **At delivery**, the name is resolved again inside the connection dialer, every address it answers with must still be acceptable, and rserve connects to a vetted address directly instead of re-resolving the name a third time. A host that passed validation and then answers with a public address — the DNS-rebinding shape, and an async run holds that window open for the length of the run — has its connection refused before any bytes leave the process. The delivery is logged as undelivered and the result stays pollable, exactly as an unreachable receiver would be.

Link-local addresses (`169.254.0.0/16`, `fe80::/10`) are **not** accepted, even though they are unroutable: `169.254.169.254` is the cloud instance-metadata endpoint, which is precisely what an attacker-chosen callback URL would aim at. Neither are the unspecified (`0.0.0.0`, `::`) or multicast ranges. An address literal in the URL is judged directly and never resolved, and `localhost`/`*.localhost` are accepted at submit without a lookup — but both still pass through the delivery dialer's check.

**Plaintext callbacks are delivered directly and never through a proxy.** `HTTP_PROXY`, `http_proxy`, and `NO_PROXY` have no effect on `http` callback delivery. They cannot: when an ambient proxy is selected, the transport asks the dialer to connect to the proxy rather than to the callback host, so the address check above would vet the proxy while the absolute callback URL, the completion, and the caller's own `callback_headers` went to it — for it to resolve and forward as it saw fit. That is the same public-address and rebinding exposure the dialer exists to close. There is no fallback: a direct plaintext delivery that fails stays failed and the result stays pollable. **`https` callbacks keep ordinary proxy behaviour**, because their guarantee is the certificate, which a proxy cannot forge. A deployment that must route callbacks through a proxy should use an `https` callback URL.

**Redirects are never followed**, on `http` or `https`. A receiver that answers a callback with a `3xx` gets that attempt counted as a failure and retried; the payload, the artifacts, and the caller's own `callback_headers` are not handed to the redirect target. A resume URL does not redirect, so nothing legitimate is lost.

`callback_headers` are applied verbatim to the POST (a bearer token for a receiver that needs one); names must be valid HTTP field names and values must not contain line breaks, else `400 invalid_callback_headers`. **Header values are never logged, and neither is the callback URL's path or query** — a Windmill resume URL is a secret in path form, so logs name only its scheme and host.

**Callback payload.** The synchronous completion object plus `run_id` and `status`:

```json
{
  "id": "chatcmpl-a1b2c3d4e5f60718",
  "object": "chat.completion",
  "created": 1711800000,
  "model": "claude:opus",
  "choices": [{"index": 0, "message": {"role": "assistant", "content": "..."}, "finish_reason": "stop"}],
  "usage": {"prompt_tokens": 1200, "completion_tokens": 3500, "total_tokens": 4700},
  "usage_source": "cli",
  "cost_usd": 0.0432,
  "correlation_id": "windmill-job-42",
  "run_id": "a1b2c3d4e5f60718",
  "status": "success"
}
```

A failure carries the same error envelope a synchronous caller would have received, `retryable` included:

```json
{
  "id": "chatcmpl-a1b2c3d4e5f60718",
  "object": "chat.completion",
  "created": 1711800000,
  "model": "claude:opus",
  "choices": [],
  "run_id": "a1b2c3d4e5f60718",
  "status": "failure",
  "error": {"message": "server shut down before the run finished; ...", "type": "server_error", "code": "server_shutdown", "retryable": true}
}
```

`status` reports whether the run produced a completion, not whether the model was happy: a CLI that exits nonzero still yields `success` with whatever it wrote, exactly as the synchronous path returns `200` for the same run. `failure` means no completion exists — `clone_failed`, `run_cancelled`, or `server_shutdown`. A `run_cancelled` failure still carries `artifacts`: what the run wrote before the kill is the point of cancelling it and looking.

**Delivery.** POST with `Content-Type: application/json`, 10s per attempt, 3 attempts with backoff (2s, then 8s), then rserve gives up and logs a warning. Any non-2xx counts as a failed attempt. Delivery happens **after** the run slot is released, so a slow receiver never holds capacity. An undelivered callback costs the run nothing: the result stays available at `GET /v1/runs/{run_id}/result` for as long as retention holds it.

**One ending per run.** A run's status, its retained result, and the callback its receiver gets are chosen together by a single atomic transition, and only whichever caller wins that transition delivers. A run that completes at the same moment the server begins shutting down therefore either reports its own outcome or reports `server_shutdown` — never stores one and delivers the other. Exactly one callback is sent per run, whatever raced.

**Retention is in-memory and non-durable.** Results live in the rserve process, bounded to **100 results or 1 hour**, whichever binds first, with least-recently-used eviction; a run that is still queued or running is never evicted. Message content is capped at 64KB — the same discipline as bundle step output — and an oversize completion is truncated with `"output_truncated": true` rather than dropped. **A restart loses every pending run and every retained result.** Callers whose run was in flight get one best-effort `server_shutdown` failure callback if their receiver is up, and nothing at all if it is not. Durable run state belongs in the caller's own store (Postgres, in this fleet) or in the caller's timeout — for a Windmill flow, the suspend timeout is the guard.

**Artifacts diverge between the callback and the retained copy, on purpose.** A callback is delivered once and then forgotten, so it carries artifacts in full, up to the same 2MB response budget the synchronous path uses. Retention has to hold results in this process's memory alongside 99 others, so it keeps the same 64KB discipline as message content: when a completion's artifact contents exceed 64KB in total, the **retained** copy drops them and lists every one in `artifacts_skipped` with reason `evicted_from_retention`, while the POST the receiver got carries the bytes.

The practical consequence: **take the artifacts off your callback.** `GET /v1/runs/{run_id}/result` is the fallback for learning a run's outcome, not a second copy of its payload — an artifact big enough to matter is exactly the one polling will not return.

### GET /v1/runs/{run_id}

Lifecycle status of an async run. Timestamps are Unix seconds and appear as the run reaches each stage.

```json
{
  "run_id": "a1b2c3d4e5f60718",
  "status": "running",
  "correlation_id": "windmill-job-42",
  "created_at": 1711800000,
  "started_at": 1711800004,
  "queue_wait_ms": 4120
}
```

`status` is `queued`, `running`, `success`, or `failure`. `404 not_found` means the ID is unknown or its result has been evicted — from here those are the same condition.

### GET /v1/runs/{run_id}/result

The run's retained result: the same terminal outcome the callback carried — identical `status`, and identical `error.code` when it failed — chosen by one atomic transition, so status, retained result, and delivered callback can never describe different endings.

It is **not** guaranteed to be byte-for-byte the callback body. Artifact content diverges above the 64KB retention budget by design (see below): the callback carries the bytes, the retained copy keeps the names with reason `evicted_from_retention`. `404 not_found` when the run is unknown, evicted, or has not finished yet (the message says which, and names the current status).

### GET /v1/runs

Run summaries, newest first. `?correlation_id=` filters to the runs one external job owns — the value is sanitized the same way `X-Correlation-ID` is. Without the parameter every known run is listed.

```json
{"object": "list", "data": [{"run_id": "a1b2c3d4e5f60718", "status": "success", "correlation_id": "windmill-job-42", "created_at": 1711800000, "started_at": 1711800004, "finished_at": 1711800039}]}
```

### DELETE /v1/runs/{run_id}

Cancels a queued or running async run: the CLI subprocess is killed, the scratch clone removed, the run slot freed, and a `run_cancelled` failure callback delivered. This is what replaces "client disconnect cancels the run" once the connection is gone. Returns `204` for any known run — including one that already finished, so a caller cancelling twice sees the same answer — and `404` once the run has been evicted.

The same run is equally cancellable through gRPC `CancelRun`, with the same outcome. Whichever arrives first owns the run's ending: the two cannot both win, and exactly one callback is sent. The error message names which API ended the run.

### Windmill pairing

The payoff: a flow step submits with its own resume URL as the callback, then suspends. rserve resumes the flow with the completion as the resume payload. No held connection, no timeout coupling, and a worker restart no longer kills the run.

```python
# Step 1 — submit, then suspend. Returns immediately.
import requests, wmill

resume_urls = wmill.get_resume_urls()          # approval/resume endpoints for this step
r = requests.post(
    "http://127.0.0.1:14261/v1/chat/completions",
    headers={"X-Correlation-ID": wmill.get_job_id()},
    json={
        "model": "claude:opus",
        "messages": [{"role": "user", "content": task}],
        "work_dirs": ["/srv/repo"],
        "clone_work_dirs": True,
        "return_artifacts": True,               # the files the agent writes come back
        "callback_url": resume_urls["resume"],
    },
    timeout=30,
)
r.raise_for_status()                            # 400s are still 400s, right here
run_id = r.json()["run_id"]                     # keep it: poll or cancel with this
```

Set the step's suspend timeout to the longest the run may take (e.g. 2h); it becomes the only timeout knob in the flow. The resumed payload is the callback body above, so the next step reads `status`, `choices[0].message.content`, `cost_usd`, `artifacts`, and — on failure — `error.retryable`, which is exactly what a Windmill `retry` policy should branch on. Read `artifacts` out of the resume payload and into the flow's own state (a variable, an email, a database row); polling will not give them back once they are over the retention budget. If the flow times out or the callback never lands, `GET /v1/runs/{run_id}` and `GET /v1/runs/{run_id}/result` are the fallback, subject to the retention bounds above.

### GET /v1/models

List available tools (only those whose CLI binary is found on PATH), configured defaults, fixed model namespaces, and model-specific effort suffixes. Dynamic OpenCode/KiloCode namespaces set `"dynamic": true` and list their configured default while continuing to accept arbitrary `provider/model` identifiers.

```json
{
  "object": "list",
  "data": [
    {"id": "claude", "object": "model", "created": 1711800000, "owned_by": "rcodegen", "efforts": ["low", "medium", "high", "xhigh", "max"]},
    {"id": "claude:sonnet", "object": "model", "created": 1711800000, "owned_by": "rcodegen", "default": true, "efforts": ["low", "medium", "high", "xhigh", "max"]},
    {"id": "codex", "object": "model", "created": 1711800000, "owned_by": "rcodegen", "efforts": ["low", "medium", "high", "xhigh", "max", "ultra"]},
    {"id": "codex:gpt-5.6-sol", "object": "model", "created": 1711800000, "owned_by": "rcodegen", "default": true, "efforts": ["low", "medium", "high", "xhigh", "max", "ultra"]},
    {"id": "opencode", "object": "model", "created": 1711800000, "owned_by": "rcodegen", "dynamic": true}
  ]
}
```

### GET/POST /v1/bundles and GET /v1/bundles/{name}

- `GET /v1/bundles` lists bundle names and required inputs.
- `GET /v1/bundles/{name}` returns the complete step DAG.
- `POST /v1/bundles/{name}` runs a bundle with optional `inputs`, absolute `work_dir`, `options`, and SSE `stream`. Responses include per-step results, final output, usage, correlation ID, and bounded inline text artifacts.
- `X-Correlation-ID` is sanitized, echoed, and attached to the run registry entry — the same handling chat completions get. `RSERVE_WORK_ROOT` can confine `work_dir` values and must be absolute.

### GET /health

```json
{
  "status": "ok",
  "version": "<current server version>",
  "active_runs": 1,
  "queued": 2,
  "max_concurrent": 3,
  "async_live": 5,
  "async_max_live": 12,
  "async_bytes": 41984,
  "async_max_bytes": 67108864
}
```

`queued` is the number of requests waiting for a run slot — the difference between a server that is busy and one that is saturated. It counts waiters from every entry point (HTTP chat completions, HTTP bundles, and gRPC).

`async_live` and `async_bytes` are what async admission currently holds, against the `async_max_live` and `async_max_bytes` ceilings. They are the two numbers that explain an `async_capacity` refusal, and the way to tell a genuinely full server from one whose limits were configured too low for its workload.

### POST /v1/files

Upload a file (multipart form). Max 50 MB. Files are stored beneath `rserve-files` in the operating system's temporary directory (`os.TempDir()`) with 24-hour expiry.

**Form fields:**

| Field | Required | Description |
|-------|----------|-------------|
| `file` | yes | File data |
| `purpose` | no | Purpose string (default: `"user_data"`) |

**Response:**

```json
{
  "id": "file-a1b2c3",
  "object": "file",
  "bytes": 1024,
  "created_at": 1711800000,
  "filename": "data.csv",
  "purpose": "user_data",
  "path": "<os-temp>/rserve-files/file-a1b2c3-data.csv"
}
```

### GET /v1/files

List all uploaded files. Returns `{"object": "list", "data": [...]}`.

### GET /v1/files/{id}

Retrieve metadata for a specific file by ID.

### DELETE /v1/files/{id}

Delete a file.

```json
{"id": "file-a1b2c3", "object": "file", "deleted": true}
```

### Multi-Turn Sessions

Both gRPC and HTTP APIs accept `session_id`. When the selected tool reports a native session identifier, the final response includes an opaque client-facing ID; pass it back with the same tool to resume. Automatic session discovery currently works for Claude and Gemini. Sessions expire after 30 minutes of inactivity by default, are stored in memory, and are not persisted across restarts.

```bash
# First request -- get session_id from response
curl http://127.0.0.1:14261/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"claude","messages":[{"role":"user","content":"read main.go"}]}'

# Continue the conversation
curl http://127.0.0.1:14261/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"claude","messages":[{"role":"user","content":"now add tests"}],"session_id":"abc123"}'
```

Unknown, expired, or tool-mismatched IDs are ignored and start a fresh run. Adjust expiry with `rserve -session-ttl`.

### OpenAI SDK Example (Python)

```python
from openai import OpenAI

client = OpenAI(base_url="http://127.0.0.1:14261/v1", api_key="unused")

response = client.chat.completions.create(
    model="claude:opus",
    messages=[{"role": "user", "content": "audit this project"}],
    extra_body={"work_dirs": ["/path/to/project"]}
)
print(response.choices[0].message.content)
```

---

## CLI Binaries

All CLIs load settings from `~/.rcodegen/settings.json`.

### Common Flags (all single-tool wrappers)

| Flag | Description |
|------|-------------|
| `-c, --code <path>` | Project path relative to `code_dir` in settings (comma-separated) |
| `-d, --dir <path>` | Absolute working directory (comma-separated) |
| `-m, --model <name>` | Model override |
| `-o, --output <dir>` | Custom output directory (replaces `_rcodegen`) |
| `-j, --json` | Output newline-delimited JSON |
| `-J, --stats-json` | Output run statistics as JSON |
| `-l, --lock` | Queue behind other running instances (file lock) |
| `-D, --delete-old` | Delete previous reports after run |
| `-R, --require-review` | Skip if previous report has no review |
| `-r, --recursive` | Recurse subdirectories for git repos |
| `--levels <n>` | Depth of recursive scan (default: 1, max: 10) |
| `--list <dirs>` | Comma-separated subdirectory names |
| `-A, --dir-all <dirs>` | Run all git repos in given directories |
| `-n, --dry-run` | Show command without executing |
| `-f, --force` | Bypass VERSION state check |
| `-t, --tasks` | List available task shortcuts |
| `-V, --verbose` | Debug logging |
| `-v, --version` | Show version |
| `-x key=value` | Template variable substitution (repeatable) |
| `--status-only` | Show credit status and exit |

### Task Shortcuts

| Shortcut | Description |
|----------|-------------|
| `audit` | Security and quality audit with 100-point grade |
| `test` | Propose comprehensive unit tests with 100-point grade |
| `fix` | Find and fix bugs and code smells with 100-point grade |
| `refactor` | Refactoring suggestions with 100-point grade |
| `quick` | Combined 4-section report with 100-point grade |
| `grade` | Developer grading with weighted categories |
| `generate` | Template task with variable substitution |
| `study` | Deep code analysis (read-only, no edits) |
| `suite` | Runs all 5 standard report types sequentially |

Custom tasks can be defined in `~/.rcodegen/settings.json`.

### rclaude

Wraps the `claude` CLI. Supports streaming JSON output via `claude --output-format stream-json`.

| Flag | Description | Default |
|------|-------------|---------|
| `-b, --budget <usd>` | Max budget in USD per run | `10.00` (max: `1000.00`) |
| `-e, --effort <lvl>` | Reasoning effort: `low`, `medium`, `high`, `xhigh`, `max` | `xhigh` |
| `-s, --status` | Track credit usage before/after task | |
| `-S, --no-status` | Disable credit usage tracking | |

**Valid models:** `fable`, `sonnet`, `opus`, `haiku`

```bash
rclaude -c myproject audit
rclaude -c myproject -m opus -b 20.00 test
rclaude -c proj1,proj2 suite
```

### rcodex

Wraps the `codex` CLI via `codex exec`.

| Flag | Description | Default |
|------|-------------|---------|
| `-e, --effort <lvl>` | Reasoning effort through `ultra`, validated per model | `xhigh` |
| `-s, --status` | Track credit usage before/after task | |
| `-S, --no-status` | Disable credit usage tracking | |

**Valid models:** `gpt-5.6-sol` (default), `gpt-5.6-terra`, `gpt-5.6-luna`, `gpt-5.5`, `gpt-5.4`, `gpt-5.3-codex`, `gpt-5.2-codex`, `gpt-4.1-codex`, `gpt-4o-codex`. Sol/Terra accept efforts through `ultra`, Luna through `max`, and older models through `xhigh`.

```bash
rcodex -c myproject audit
rcodex -c myproject -e high -m gpt-5.3-codex test
```

### rgemini

Wraps the `gemini` CLI.

| Flag | Description |
|------|-------------|
| `--flash` | Use gemini-3-flash-preview model |
| `-i, --image <files>` | Comma-separated input images for direct image generation |
| `-s, --status` | Track usage before/after task |
| `-S, --no-status` | Disable usage tracking |

**Valid models:** `gemini-3.1-pro-preview`, `gemini-3.1-flash-image-preview` (alias: `banana`), `gemini-3-flash-preview`, `gemini-2.5-pro`, `gemini-2.5-flash`, `gemini-2.5-flash-lite`

```bash
rgemini -c myproject audit
rgemini --flash -c myproject quick
```

### rcodegen (orchestrator)

Multi-tool bundle orchestrator.

```bash
rcodegen <bundle-name> [options] [inputs...]
rcodegen list
```

| Flag | Description |
|------|-------------|
| `-c <path>` | Codebase path |
| `--opus-only` | Force Claude Opus for all steps |
| `--flash` | Force Gemini Flash for all steps |
| `--static` | Disable animated live display |
| `-j` | JSON output |

Inputs are `key=value` pairs or positional text (becomes the `task` input).

```bash
rcodegen build-review-audit -c myproject "Add user authentication"
rcodegen ensemble -c myproject "Refactor the database layer" --opus-only
rcodegen list
```

### rbatch

Batch job runner with subcommands.

**`rbatch run <manifest.json>`**

| Flag | Description |
|------|-------------|
| `--concurrency <n>` | Override manifest concurrency |
| `--threshold <n>` | Budget threshold percentage |
| `--on-budget <action>` | `stop`, `wait`, or `ask` |
| `--max-wait <duration>` | Max wait when on-budget=wait |
| `--server <host:port>` | Delegate to remote rserve via gRPC |
| `--dry-run` | Show plan without running |
| `-v` | Verbose |

**`rbatch spool <directory>`** -- Process all manifests from a spool directory.

**`rbatch resume [state.json]`** -- Resume from checkpoint.

**`rbatch status [batch-name]`** -- Show batch status from `~/.rcodegen/batches/`.

**Manifest format:**

```json
{
  "name": "nightly-audits",
  "concurrency": 2,
  "budget": {
    "threshold_pct": 20,
    "on_budget": "stop",
    "check_interval": "3m",
    "max_wait": "1h"
  },
  "jobs": [
    {
      "name": "audit-api",
      "task": "audit",
      "tool": "claude",
      "dir": "/path/to/api",
      "model": "sonnet",
      "max_budget": "5.00"
    },
    {
      "name": "audit-web",
      "task": "audit",
      "tool": "gemini",
      "dir": "/path/to/web",
      "session": "web-group"
    }
  ]
}
```

Jobs sharing a `session` identifier are executed sequentially with session IDs carried forward.

---

## Configuration

### Settings File

`~/.rcodegen/settings.json`:

```json
{
  "code_dir": "~/code",
  "output_dir": "",
  "default_build_dir": "",
  "defaults": {
    "codex": { "model": "gpt-5.6-sol", "effort": "xhigh" },
    "claude": { "model": "sonnet", "budget": "10.00" },
    "gemini": { "model": "gemini-3.1-pro-preview" }
  },
  "tasks": {
    "my-custom-task": {
      "prompt": "Analyze this code for X. Save report as {report_file} in {report_dir}."
    }
  }
}
```

### Environment Variables

| Variable | Description |
|----------|-------------|
| `RCODEGEN_CODE_DIR` | Override `code_dir` |
| `RCODEGEN_OUTPUT_DIR` | Override `output_dir` |
| `RCODEGEN_MODEL` | Override model for all tools |
| `RCODEGEN_BUDGET` | Override Claude budget |
| `RCODEGEN_EFFORT` | Override Claude/Codex effort (Codex support is model-specific) |
| `RSERVE_TOKEN` | Require bearer authentication on native HTTP except `/health`, and on gRPC from non-loopback peers (reflection and health stay open) |
| `RSERVE_WORK_ROOT` | Absolute root that confines HTTP bundle `work_dir` values |
| `RSERVE_ALLOW_INSECURE_REMOTE` | Set to `1` to permit an explicitly unsafe non-loopback native bind |
| `RSERVE_ASYNC_MAX_LIVE` | Max simultaneous live async runs (default `max(8, 4 × max_concurrent)`). Must be a positive integer or the server refuses to start |
| `RSERVE_ASYNC_MAX_BYTES` | Max estimated retained request payload across live async runs (default 64MiB). Must be a positive integer or the server refuses to start |
| `RCODEGEN_LOG_LEVEL` | Log level (`warn`, `debug`, etc.) |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | OpenTelemetry collector endpoint (enables tracing/metrics) |
| `KAFKAKIT_BOOTSTRAP_SERVERS` | Kafka brokers for the optional kafkakit/lifecycle integration |
| `KAFKAKIT_TENANT_ID` | Kafka tenant ID (default: `ai8`) |
| `XYOPS_BASE_URL` | xyops monitoring API base URL |
| `XYOPS_API_KEY` | xyops monitoring API key |

### Configuration Priority

1. Hardcoded defaults (lowest)
2. `~/.rcodegen/settings.json`
3. Environment variables (`RCODEGEN_*`)
4. CLI flags (highest)

### Task Template Variables

| Variable | Expands To |
|----------|------------|
| `{report_file}` | Auto-generated report filename |
| `{report_dir}` | Configured report directory (default: `_rcodegen`) |
| `{codebase}` | Codebase name from `-c` |
| `{timestamp}` | Current timestamp |
| `{variable}` | Custom value from `-x variable=value` |

---

## External Services Consumed

| Service | Transport | Configuration |
|---------|-----------|---------------|
| Claude Code CLI (`claude`) | Subprocess | Must be installed and authenticated |
| OpenAI Codex CLI (`codex`) | Subprocess | Must be installed and authenticated |
| Google Gemini CLI (`gemini`) | Subprocess | Must be installed and authenticated |
| OpenTelemetry Collector | OTLP gRPC | `OTEL_EXPORTER_OTLP_ENDPOINT` |
| Kafka / Redpanda | TCP | `KAFKAKIT_BOOTSTRAP_SERVERS` |
| xyops Monitoring | HTTP | `XYOPS_BASE_URL` + `XYOPS_API_KEY` |
| iTerm2 Python API | Local | Optional, for credit tracking scripts |
