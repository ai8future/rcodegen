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
  "session_id": "optional-session-id"
}
```

**Model format:** `{tool}` or `{tool}:{model}` -- e.g., `claude`, `claude:opus`, `codex:gpt-5.6-sol`, `gemini`, `gemini:gemini-3-flash-preview`. Claude and Codex also accept a supported `-{effort}` suffix, such as `claude:opus-max`, `codex:gpt-5.6-luna-max`, or bare `codex-ultra` for the configured default model. OpenCode and KiloCode accept dynamic `provider/model` identifiers.

**Request headers:**

| Header | Description |
|--------|-------------|
| `X-Show-Tool-Use: true` | Include tool-use summaries in streaming text output |

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
  "session_id": "abc123"
}
```

**Streaming response** (`"stream": true`):

Server-Sent Events format. Each event is `data: {json chunk}\n\n`. Final message is `data: [DONE]\n\n`.

Response headers: `Content-Type: text/event-stream`, `Cache-Control: no-cache`, `Connection: keep-alive`, `X-Accel-Buffering: no`.

**Error responses:**

```json
{"error": {"message": "unknown tool: foo", "type": "invalid_request_error", "code": "unknown_tool"}}
```

| HTTP Status | Meaning |
|-------------|---------|
| `400` | Bad request, unknown tool, invalid fixed model, or unsupported model/effort combination |
| `405` | Method not allowed |
| `503` | Request cancelled or disconnected while queued for a run slot |

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
- `X-Correlation-ID` is sanitized, echoed, and attached to the run registry entry. `RSERVE_WORK_ROOT` can confine `work_dir` values and must be absolute.

### GET /health

```json
{
  "status": "ok",
  "version": "<current server version>",
  "active_runs": 1,
  "max_concurrent": 3
}
```

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
