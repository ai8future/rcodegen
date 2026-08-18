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

Concurrency is controlled by a shared semaphore and requests queue until a slot is available. `RSERVE_TOKEN` optionally protects HTTP endpoints except `/health`; the plaintext gRPC listener has no authentication and native HTTP has no TLS. Keep rserve on loopback and expose only the required API through authenticated TLS transport. Non-loopback binds are refused unless `RSERVE_ALLOW_INSECURE_REMOTE=1` explicitly acknowledges the risk.

---

## gRPC API

**Service:** `rserve.RServe`
**Proto:** `proto/rserve.proto`
**Default port:** `14260`
**Transport:** Plain TCP (no TLS). gRPC reflection is enabled.

**Interceptors:** recovery, OpenTelemetry tracing (unary), metrics, logging.

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

### CancelRun (unary)

```protobuf
rpc CancelRun(CancelRunRequest) returns (CancelRunResponse);
```

| Field | Type | Description |
|-------|------|-------------|
| `run_id` | string | Run ID to cancel |

Returns `cancelled` (bool) and `message` (string).

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
  "correlation_id": "windmill-job-42"
}
```

`correlation_id` is present only when the request carried `X-Correlation-ID`.

**Usage and cost provenance:** `usage_source` says where the numbers came from and is always present on a completed response.

| `usage_source` | Meaning | Tools |
|----------------|---------|-------|
| `cli` | The tool's CLI reported usage; `usage` is populated, and `cost_usd` too when the CLI reports a cost | Claude (tokens + cost), Gemini (tokens only -- `cost_usd` is omitted, not zero) |
| `unreported` | The CLI publishes no usage at all. `usage` and `cost_usd` are **omitted entirely** | Codex (its JSON carries `usage: null`), OpenCode, KiloCode |

rserve never fabricates these numbers. An omitted `cost_usd` means "not measured", never "free", so a caller summing costs across runs must treat `unreported` as unknown rather than as zero. Extraction lives with each tool adapter (`runner.UsageReporter`), so a CLI that starts reporting usage is a change in one adapter.

**Streaming response** (`"stream": true`):

Server-Sent Events format. Each event is `data: {json chunk}\n\n`. Final message is `data: [DONE]\n\n`.

Response headers: `Content-Type: text/event-stream`, `Cache-Control: no-cache`, `Connection: keep-alive`, `X-Accel-Buffering: no`.

`session_id`, `cloned_work_dirs`, `correlation_id`, `usage`, `usage_source`, and `cost_usd` ride the final chunk (the one carrying `finish_reason`), not the content chunks.

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
| `false` | `method_not_allowed`, `unauthorized`, `invalid_json`, `unknown_tool`, `empty_task`, `invalid_model`, `invalid_effort`, `invalid_work_dir`, `unsafe_symlink`, `unsupported_git_worktree`, `unknown_bundle`, `missing_input`, `invalid_upload`, `missing_file`, `invalid_id`, `not_found`, `no_file_store`, `invalid_callback_url`, `invalid_callback_headers`, `callback_stream_conflict`, `run_cancelled` | The request is malformed, names something that does not exist, or is refused on policy grounds. It will be refused identically every time until the caller changes it |
| `true` | `concurrency_limit`, `clone_failed`, `work_dir_failed`, `bundle_failed`, `bundle_list_failed`, `save_failed`, `server_shutdown` | Transient: an interrupted slot wait, a filesystem failure, a CLI/provider failure (crash, unexpected exit, timeout, rate limit), or a server restart that caught the run in flight. The same request can succeed later |

| HTTP Status | Meaning |
|-------------|---------|
| `202` | Async submission accepted; the completion will be POSTed to `callback_url` |
| `400` | Bad request, unknown tool, invalid fixed model, unsupported model/effort combination, an unusable `callback_url`/`callback_headers`, `callback_url` together with `stream`, or a `work_dirs` entry that is missing, not a directory, holds an escaping symlink (`unsafe_symlink`), or holds a git pointer file (`unsupported_git_worktree`) |
| `404` | Unknown run ID, or one whose result has been evicted from retention |
| `405` | Method not allowed |
| `500` | Work-directory clone failed, including a source that changed after validation |
| `503` | Request cancelled or disconnected while queued for a run slot |

### Async callback mode

A synchronous completion holds one HTTP connection for the whole run, couples three timeouts (client read < caller's module timeout < instance timeout), and dies with the connection: a client disconnect cancels the run. For runs measured in minutes that is the largest source of avoidable failure. In callback mode the connection is released immediately and the run's lifecycle belongs to the server.

Send `callback_url` on a chat completion and rserve:

1. validates the request **in full** — model, effort, `work_dirs` policies, the callback URL itself — so a bad request still fails on this connection with the same `400` it always did;
2. answers `202` with the run's identity and releases the connection;
3. runs it exactly as the synchronous path would (same queue accounting, same `clone_work_dirs` behaviour, same completion shape);
4. POSTs the completion to `callback_url` when the run ends, success or failure;
5. retains the result for polling, whether or not the callback was delivered.

```json
{"run_id": "a1b2c3d4e5f60718", "status": "queued", "correlation_id": "windmill-job-42"}
```

`callback_url` cannot be combined with `"stream": true` (`400 callback_stream_conflict`) — a callback delivers the completion once, a stream delivers it incrementally.

**Callback URL rules.** `https` is accepted for any host. Plain `http` is accepted only when the host is a loopback or RFC1918 address (`127.0.0.0/8`, `::1`, `localhost`, `10/8`, `172.16/12`, `192.168/16`, IPv6 unique-local), where the network itself is the boundary. Anything else is `400 invalid_callback_url`. `callback_headers` are applied verbatim to the POST (a bearer token for a receiver that needs one); names must be valid HTTP field names and values must not contain line breaks, else `400 invalid_callback_headers`. **Header values are never logged, and neither is the callback URL's path or query** — a Windmill resume URL is a secret in path form, so logs name only its scheme and host.

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

`status` reports whether the run produced a completion, not whether the model was happy: a CLI that exits nonzero still yields `success` with whatever it wrote, exactly as the synchronous path returns `200` for the same run. `failure` means no completion exists — `clone_failed`, `run_cancelled`, or `server_shutdown`.

**Delivery.** POST with `Content-Type: application/json`, 10s per attempt, 3 attempts with backoff (2s, then 8s), then rserve gives up and logs a warning. Any non-2xx counts as a failed attempt. Delivery happens **after** the run slot is released, so a slow receiver never holds capacity. An undelivered callback costs the run nothing: the result stays available at `GET /v1/runs/{run_id}/result` for as long as retention holds it.

**Retention is in-memory and non-durable.** Results live in the rserve process, bounded to **100 results or 1 hour**, whichever binds first, with least-recently-used eviction; a run that is still queued or running is never evicted. Message content is capped at 64KB — the same discipline as bundle step output — and an oversize completion is truncated with `"output_truncated": true` rather than dropped. **A restart loses every pending run and every retained result.** Callers whose run was in flight get one best-effort `server_shutdown` failure callback if their receiver is up, and nothing at all if it is not. Durable run state belongs in the caller's own store (Postgres, in this fleet) or in the caller's timeout — for a Windmill flow, the suspend timeout is the guard.

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

The retained callback payload, byte for byte what the callback receiver was sent. `404 not_found` when the run is unknown, evicted, or has not finished yet (the message says which, and names the current status).

### GET /v1/runs

Run summaries, newest first. `?correlation_id=` filters to the runs one external job owns — the value is sanitized the same way `X-Correlation-ID` is. Without the parameter every known run is listed.

```json
{"object": "list", "data": [{"run_id": "a1b2c3d4e5f60718", "status": "success", "correlation_id": "windmill-job-42", "created_at": 1711800000, "started_at": 1711800004, "finished_at": 1711800039}]}
```

### DELETE /v1/runs/{run_id}

Cancels a queued or running async run: the CLI subprocess is killed, the scratch clone removed, the run slot freed, and a `run_cancelled` failure callback delivered. This is what replaces "client disconnect cancels the run" once the connection is gone. Returns `204` for any known run — including one that already finished, so a caller cancelling twice sees the same answer — and `404` once the run has been evicted.

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
        "callback_url": resume_urls["resume"],
    },
    timeout=30,
)
r.raise_for_status()                            # 400s are still 400s, right here
run_id = r.json()["run_id"]                     # keep it: poll or cancel with this
```

Set the step's suspend timeout to the longest the run may take (e.g. 2h); it becomes the only timeout knob in the flow. The resumed payload is the callback body above, so the next step reads `status`, `choices[0].message.content`, `cost_usd`, and — on failure — `error.retryable`, which is exactly what a Windmill `retry` policy should branch on. If the flow times out or the callback never lands, `GET /v1/runs/{run_id}` and `GET /v1/runs/{run_id}/result` are the fallback, subject to the retention bounds above.

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
  "max_concurrent": 3
}
```

`queued` is the number of requests waiting for a run slot — the difference between a server that is busy and one that is saturated. It counts waiters from every entry point (HTTP chat completions, HTTP bundles, and gRPC).

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
| `RSERVE_TOKEN` | Require bearer authentication on native HTTP except `/health` |
| `RSERVE_WORK_ROOT` | Absolute root that confines HTTP bundle `work_dir` values |
| `RSERVE_ALLOW_INSECURE_REMOTE` | Set to `1` to permit an explicitly unsafe non-loopback native bind |
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
