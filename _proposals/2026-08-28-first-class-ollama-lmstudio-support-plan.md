# First-Class Ollama and LM Studio Support — Corrected Implementation Plan

**Updated:** 2026-08-28

**Goal:** Add `ollama` and `lmstudio` as first-class rserve, rbatch, and bundle tools that call local OpenAI-compatible HTTP endpoints directly, preserve chat-message semantics, expose the runtime's currently available model inventory, route reasoning effort without corrupting dynamic model identifiers, report backend failures correctly, and work without standalone CLI subprocesses.

**Stop condition:** The feature is complete only when deterministic tests prove all three execution surfaces work end to end:

1. rserve chat completions succeed and backend failures become explicit API failures.
2. Local and remote rbatch retain the local model's generated text in bounded per-job result files instead of discarding it.
3. bundle steps take the direct-API path and never execute the defensive `BuildCommand` fallback.

The implementation must also pass the repository's full vendor, build, race-test, lint, and versioned-release workflow.

---

## Current Runtime Facts

These facts replace the stale compatibility assumptions in the earlier draft. Re-check the linked official documentation immediately before implementation if the runtime versions have materially changed.

### Ollama

- Default local origin: `http://localhost:11434`.
- OpenAI-compatible chat endpoint: `POST /v1/chat/completions`.
- Ollama supports chat messages, streaming, tools, logprobs, `tool_choice`, `logit_bias`, `user`, `n`, and `reasoning_effort`. The first implementation deliberately sends a smaller common payload; it must not claim those omitted fields are unsupported.
- Supported Ollama reasoning efforts are `none`, `low`, `medium`, and `high`.
- `GET /api/tags` lists models pulled/installed in Ollama. It does **not** mean those models are currently loaded in memory.
- `GET /api/ps` is the loaded/running-model endpoint and is follow-up metadata, not the Phase 1 inventory source.
- Context size is not configurable through the OpenAI-compatible request. A Modelfile with `PARAMETER num_ctx` is still required.
- The local Ollama API does not require authentication. An optional bearer-token setting is still useful for authenticated reverse proxies or remote/self-hosted deployments.

Official references:

- [Ollama OpenAI compatibility](https://docs.ollama.com/api/openai-compatibility)
- [Ollama list models (`/api/tags`)](https://docs.ollama.com/api/tags)
- [Ollama list running models (`/api/ps`)](https://docs.ollama.com/api/ps)

### LM Studio

- Default local origin: `http://localhost:1234`.
- OpenAI-compatible chat endpoint: `POST /v1/chat/completions`.
- OpenAI-compatible inventory endpoint: `GET /v1/models`.
- When JIT loading is enabled, `/v1/models` can return every downloaded model, not only models already loaded in memory. When JIT is disabled, it generally returns the models visible to the running server. The plan therefore calls this an **available inventory**, not a loaded inventory.
- LM Studio can require an API token for every request. First-class support therefore needs an optional bearer-token setting from the start.
- LM Studio's native `/api/v1/*` API is now the richer API for stateful chat, loading, unloading, context configuration, and model metadata. Phase 1 intentionally keeps `/v1/chat/completions` as the common Ollama/LM Studio inference contract; richer native metadata remains a follow-up.

Official references:

- [LM Studio OpenAI compatibility](https://lmstudio.ai/docs/developer/openai-compat)
- [LM Studio chat completions](https://lmstudio.ai/docs/developer/openai-compat/chat-completions)
- [LM Studio OpenAI-compatible models endpoint](https://lmstudio.ai/docs/developer/openai-compat/models)
- [LM Studio headless mode and JIT loading](https://lmstudio.ai/docs/developer/core/headless)
- [LM Studio API authentication](https://lmstudio.ai/docs/developer/core/authentication)
- [LM Studio native REST API](https://lmstudio.ai/docs/developer/rest)

---

## Requirements Summary

### In scope

- Direct HTTP inference for Ollama and LM Studio through `runner.DirectAPIRunner`.
- Original rserve `system`, `user`, and `assistant` messages preserved in order for local API tools.
- Plain task strings from gRPC, rbatch, and bundles mapped to one `user` message.
- Optional LM Studio or reverse-proxy bearer authentication.
- Secure, operator-configured base URLs with no request-level endpoint override.
- Available-model discovery:
  - Ollama: `GET /api/tags`.
  - LM Studio: `GET /v1/models`.
- Graceful `/v1/models` behavior when either local runtime is stopped.
- Ollama `reasoning_effort` mapping for `none`, `low`, `medium`, and `high`.
- Explicit effort fields on HTTP, gRPC, rbatch, and bundle requests so arbitrary runtime-defined model identifiers are never rewritten as effort suffixes.
- Correct rserve synchronous, streaming-envelope, and async-callback failure reporting.
- Direct-API support in the bundle executor.
- Bounded output capture in rbatch.
- Registration in rserve, rbatch, and orchestrator.
- Documentation, settings example, tests, VERSION, CHANGELOG, bug notes, build, commit, and push.

### Explicitly out of scope

- Standalone `rollama` or `rlmstudio` binaries.
- Model downloading, loading, unloading, or lifecycle management.
- Advertising whether a model is currently resident in RAM/VRAM.
- Passing arbitrary OpenAI inference parameters through rserve.
- Tool/function-call execution.
- Image or multimodal inputs.
- Native upstream token-by-token SSE passthrough. In Phase 1, an rserve request with `stream:true` remains a valid SSE response, but the local backend response may arrive as one content chunk after inference completes.
- LM Studio `/api/v1/chat` stateful sessions.
- Ollama `/api/show` context-window preflight.

---

## Corrections Applied to the Earlier Draft

| Earlier assumption | Corrected requirement |
|---|---|
| Registering a `DirectAPIRunner` in orchestrator is sufficient. | `pkg/executor.ToolExecutor` bypasses `runner.RunWithContext`; add an explicit direct-API execution branch and prove `BuildCommand` is never called. |
| A nonzero `RunDirectAPI` result becomes an HTTP error automatically. | rserve currently ignores `RunResult.ExitCode`; explicitly propagate failure through synchronous, streaming, and async HTTP paths. |
| Passing the flattened task string preserves OpenAI chat semantics. | Store the original messages on `runner.Config` and send them to the local backend in order. |
| rbatch support only needs factory registration. | Local rbatch currently uses `io.Discard`, remote rbatch ignores streamed/final output, and `WriteJobResult` is not wired into command execution. Capture and persist bounded output in every rbatch mode. |
| The local model list represents loaded models. | It represents models available/visible to the runtime. Loaded-state metadata is a follow-up. |
| Ollama rejects `tool_choice`, `logit_bias`, `n`, and `user`. | Current Ollama documentation lists them as supported. Phase 1 remains deliberately minimal without calling them unsupported. |
| A hard-coded Qwen model is a safe default. | There is no universal installed model. Model defaults are optional and empty unless configured by the operator. |
| Checking only the initial base URL enforces the remote-host policy. | Reject redirects and validate an origin-only base URL before every request. |
| LM Studio needs no authentication setting. | LM Studio can require bearer authentication; add an optional API key/token setting. |
| `OLLAMA_HOST` is a useful client fallback. | It configures Ollama's server binding and was shadowed by populated rcodegen defaults. Use explicit rcodegen base-URL settings/env vars only. |
| rkilo/ropencode establish a no-binary precedent. | Both currently have standalone binaries. No new local-runtime binaries remains a deliberate scope choice, not a precedent. |
| The repository builds six binaries. | `Makefile` currently lists eight binaries. Use `make`; do not hard-code an obsolete count. |
| A `bool` with `json:"available,omitempty"` can explicitly report an unavailable configured default. | It cannot: false is omitted. Use an optional boolean so static entries omit the field while local discovered/default entries explicitly report true or false. |
| Existing `-{effort}` parsing is safe for runtime-defined model IDs. | An Ollama/LM Studio model may legitimately end in `-high`, `-low`, and similar text. Do not infer an effort suffix from explicit models in a dynamic namespace; use request-level effort fields. |

---

## Architecture

### 1. Shared local runtime adapter

Create `pkg/tools/localai`, parameterized by:

```go
type Flavor int

const (
    FlavorOllama Flavor = iota
    FlavorLMStudio
)
```

`Tool` implements:

- `runner.Tool`
- `runner.DirectAPIRunner`
- `runner.DynamicModelLister`
- `runner.UsageReporter`
- `runner.SettingsAware`

The tool holds only immutable flavor plus the injected settings pointer. Per-run output, error, messages, and usage remain on `runner.Config`/`runner.RunResult`; no mutable run state is stored on the shared orchestrator tool instance.

### 2. Preserve chat messages at the runner boundary

Add a runner-native message type so `pkg/runner` does not depend on `pkg/server/openai`:

```go
type ChatMessage struct {
    Role    string
    Content string
}
```

Add `Messages []ChatMessage` to `runner.Config`.

- For localai requests, rserve validates the supported standard roles and defensively copies the incoming message list into `cfg.Messages` before flattening it for CLI tools.
- Existing CLI adapters continue using `cfg.Task` and ignore `cfg.Messages`.
- localai uses `cfg.Messages` when non-empty.
- gRPC, rbatch, and bundles leave `cfg.Messages` empty, so localai constructs one `user` message from `task`.

### 3. Make direct execution a real cross-surface contract

- `runner.RunWithContext` remains the canonical direct-API dispatch for rserve, gRPC, and rbatch.
- `pkg/executor.ToolExecutor` gets an explicit direct-API branch because it currently builds subprocess commands itself.
- A nonzero run must set `RunResult.Error` to a non-nil generic execution error. localai must write only bounded, sanitized backend detail, and every durable caller must capture it with an explicit bound rather than an unbounded `bytes.Buffer`.
- HTTP response builders must inspect `ExitCode`/`Error` rather than treating every completed function call as success.

### 4. Available-model discovery

Introduce the explicit inventory interface:

```go
type DynamicModelLister interface {
    ListAvailableModels(ctx context.Context) ([]string, error)
}
```

Add this extension to model entries:

```go
Available *bool `json:"available,omitempty"`
```

`available:true` means the model identifier was returned by a successful live inventory request. `available:false` means an operator-configured default was not returned, including when the probe failed. It does not mean loaded in RAM/VRAM. Static CLI entries and bare dynamic tool entries omit the field because live runtime availability does not apply to them.

The configured default model is always represented once when non-empty:

- `default:true`
- explicit `available:true` only when the live inventory included it
- explicit `available:false` when the backend is unreachable or did not advertise it

No fabricated model entry is created when the operator has not configured a default.

### 5. Base URL and authentication policy

Base URLs are origins, not arbitrary URL prefixes:

- Scheme must be `http` or `https`.
- Host and optional port are required.
- User info, path other than `/`, query, and fragment are rejected.
- Without `allow_remote`, accept only exact `localhost`, loopback IPs, or literal private IPs.
- Reject unspecified bind addresses such as `0.0.0.0` and `::`; users must configure a connectable address.
- DNS names other than `localhost` require `allow_remote:true`; this avoids pretending a one-time DNS lookup is a durable private-network security boundary.
- Reject all redirects. Neither local API requires them, and following redirects would bypass the validated destination policy.
- Use a dedicated reusable `http.Client`, not `http.DefaultClient`.
- Do not use ambient HTTP proxy variables for local-runtime requests; connect directly to the configured runtime.
- Add `Authorization: Bearer <api_key>` when configured.
- Never include the API key in stats, banners, logs, errors, or model-list responses.

The redirect requirement is explicit because Go clients follow redirects by default; see [`net/http.Client`](https://pkg.go.dev/net/http#Client).

### 6. No universal default model

`DefaultModel()` and `DefaultModelSetting()` may return empty for localai.

- `ollama:<model>` and `lmstudio:<model>` always provide an explicit model.
- Bare `ollama` or `lmstudio` works only when the corresponding settings block contains `model`.
- A bare request without a configured model returns a deterministic 400 `invalid_model` response before acquiring a run slot.

### 7. Effort is explicit for dynamic model namespaces

Current `runner.SplitModelEffort` treats a trailing `-{effort}` as syntax when the remaining model is valid. That is safe for fixed model lists but ambiguous for Ollama and LM Studio, where every non-empty identifier is potentially valid.

- Add `reasoning_effort` to HTTP chat-completion requests.
- Add `effort` to bundle steps and gRPC `RunTaskRequest`; rbatch already has an `effort` field.
- An explicit effort field is authoritative when no conflicting syntax is present. If a fixed-model suffix and explicit field are both supplied and disagree, reject the request instead of silently choosing one.
- Change `SplitModelEffort` so it never strips a suffix from an explicit model when `ValidModels()` is empty.
- Existing suffix behavior remains unchanged for fixed namespaces. The unambiguous bare configured-default form (for example `ollama-high`) may continue to work in HTTP, but `ollama:some-model-high` always means the literal model identifier `some-model-high`.
- `ModelInfo.Efforts` describes accepted effort values. Documentation must explain whether a surface supplies them through a suffix or an explicit field.

### 8. Share one bounded capture primitive

rserve, gRPC, rbatch, and bundle execution all need an `io.Writer` that retains a prefix without making the producer fail after the cap. Add one small runner utility rather than four subtly different implementations:

- `Write(p)` retains only bytes that fit but returns `len(p), nil`;
- `Truncated()` reports whether bytes were dropped;
- `String()` trims an incomplete trailing UTF-8 rune so persisted JSON/text remains valid;
- construction rejects a non-positive limit;
- limits are selected by the caller (64 KiB for durable results/diagnostics, 32 MiB for the synchronous localai response bound).

The utility is run-local and need not be internally synchronized; callers that permit concurrent writes retain their existing mutex.

---

## File Map

| File | Action | Responsibility |
|---|---|---|
| `pkg/runner/config.go` | Modify | `ChatMessage`, `Config.Messages` |
| `pkg/runner/tool.go` | Modify | `DynamicModelLister` interface |
| `pkg/runner/runner.go` | Modify | nonzero `RunWithContext` result carries a non-nil error |
| `pkg/runner/runner_test.go` | Modify | direct-API failure-result regression |
| `pkg/runner/validate.go` | Modify | do not parse effort suffixes from dynamic model identifiers |
| `pkg/runner/validate_test.go` | Create | fixed versus dynamic suffix parsing regressions |
| `pkg/runner/bounded_buffer.go` | Create | shared prefix-retaining writer with truncation state |
| `pkg/runner/bounded_buffer_test.go` | Create | cap, write contract, UTF-8, zero/invalid limit tests |
| `pkg/settings/settings.go` | Modify | `LocalAIDefaults`, defaults, fallback fill, env overrides |
| `pkg/settings/settings_test.go` | Modify | defaults, fallback, env, secret behavior |
| `pkg/tools/localai/localai.go` | Create | flavor, Tool implementation, defaults/validation |
| `pkg/tools/localai/client.go` | Create | base-origin validation, reusable client, auth headers |
| `pkg/tools/localai/api.go` | Create | direct chat-completion execution and usage capture |
| `pkg/tools/localai/models.go` | Create | available-model inventory for both flavors |
| `pkg/tools/localai/*_test.go` | Create | tool, policy, inference, auth, inventory tests |
| `pkg/server/openai/models.go` | Modify | API-only detection and live inventory merge |
| `pkg/server/openai/types.go` | Modify | optional `ModelInfo.Available`, request `ReasoningEffort` |
| `pkg/server/openai/handler.go` | Modify | message preservation/validation, effort, tool validation, execution failure propagation |
| `pkg/server/openai/asyncruns.go` | Modify | async tool failures become failed runs |
| `pkg/server/openai/errorcodes.go` | Modify | retryable `tool_execution_failed` code |
| `pkg/server/openai/models_list_test.go` | Modify | inventory semantics and API-only detection |
| `pkg/server/openai/handler_test.go` | Modify | sync/stream localai success and failure |
| `pkg/server/openai/asyncruns_test.go` | Modify | async localai failure status/callback |
| `pkg/executor/tool.go` | Modify | direct-API bundle branch and usage capture |
| `pkg/executor/tool_test.go` | Modify | prove direct branch bypasses `BuildCommand` |
| `pkg/bundle/bundle.go` | Modify | explicit bundle-step `effort` field |
| `pkg/bundle/loader_test.go` | Modify | bundle effort decode regression |
| `pkg/batch/executor_local.go` | Modify | bounded output/error capture and factories |
| `pkg/batch/executor_remote.go` | Modify | retain bounded gRPC result output/error |
| `pkg/batch/queue.go` | Modify | output fields on `JobResult` |
| `pkg/batch/manifest.go` | Modify | permit both local-runtime tools and validate safe unique job names |
| `pkg/batch/manifest_test.go` | Modify | tool and result-filename validation |
| `pkg/batch/executor_test.go` | Modify | local direct-API output/failure/cancellation |
| `pkg/batch/executor_remote_test.go` | Modify | remote output/failure/truncation |
| `pkg/batch/reporter.go` | Modify | safe per-job result persistence if validation is not centralized there |
| `cmd/rserve/main.go` | Modify | factories for both tools; testable factory helper |
| `cmd/rserve/main_test.go` | Modify/Create | factory registration regression |
| `pkg/server/server.go` | Modify | gRPC effort, validation, and bounded output/error capture |
| `pkg/server/server_test.go` | Modify | gRPC localai success/failure/effort |
| `proto/rserve.proto` | Modify | request `effort`, result `output_truncated`, current tool comments |
| `pkg/server/pb/rserve*.go` | Regenerate | generated protobuf bindings |
| `cmd/rbatch/main.go` | Modify | supported-tool help and per-job result wiring in run/spool/resume |
| `cmd/rbatch/main_test.go` | Modify | output persistence across command modes |
| `pkg/orchestrator/orchestrator.go` | Modify | tools for both flavors |
| `pkg/orchestrator/orchestrator_test.go` | Modify | registration and settings injection |
| `README.md` | Modify | setup, capabilities, security, limitations, examples |
| `API.md` | Modify | HTTP/gRPC request fields, failure and model-inventory semantics |
| `settings.json.example` | Modify | optional local-runtime settings |
| `_bugs_fixed/2026-08-28-local-direct-api-integration-gaps.md` | Create | bundle, HTTP failure-path, and rbatch persistence bug record |
| `VERSION`, `CHANGELOG.md` | Modify last | release metadata only after implementation is green |

---

## Implementation Tasks

### Task 1: Lock the runner contracts

**Files:** `pkg/runner/config.go`, `pkg/runner/tool.go`, `pkg/runner/runner.go`, `pkg/runner/runner_test.go`, `pkg/runner/validate.go`, `pkg/runner/validate_test.go`, `pkg/runner/bounded_buffer.go`, `pkg/runner/bounded_buffer_test.go`

1. Add `runner.ChatMessage` and `Config.Messages`.
2. Add `DynamicModelLister.ListAvailableModels(context.Context)`.
3. Update `RunWithContext` so a nonzero exit code produces a non-nil `RunResult.Error` when the execution path did not already provide one. Do not fabricate an error for exit code zero.
4. Update `SplitModelEffort` to return an explicit dynamic model identifier unchanged when `ValidModels()` is empty. Fixed model namespaces retain existing suffix parsing.
5. Add the shared bounded capture writer described above.
6. Add regression tests for:
   - ordered chat-message storage;
   - successful direct execution with nil error;
   - failed direct execution with preserved exit code and non-nil error;
   - cancelled direct execution.
   - fixed model `model-high` suffix parsing still works;
   - dynamic model identifiers ending in `-none`, `-low`, `-medium`, or `-high` are never truncated.
   - bounded writes retain the prefix, return the original write length, mark truncation, and do not persist a partial UTF-8 rune.

**Acceptance:** `go test ./pkg/runner -run 'Test.*DirectAPI|Test.*ChatMessage|Test.*SplitModelEffort' -v` passes.

### Task 2: Add local-runtime settings without fake models

**Files:** `pkg/settings/settings.go`, `pkg/settings/settings_test.go`

Add:

```go
type LocalAIDefaults struct {
    BaseURL        string `json:"base_url,omitempty"`
    Model          string `json:"model,omitempty"`
    TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
    AllowRemote    bool   `json:"allow_remote,omitempty"`
    APIKey         string `json:"api_key,omitempty"`
}
```

Constants:

```go
DefaultOllamaBaseURL         = "http://localhost:11434"
DefaultLMStudioBaseURL       = "http://localhost:1234"
DefaultLocalAITimeoutSeconds = 600
```

Do **not** define hard-coded model constants.

Add `Defaults.Ollama` and `Defaults.LMStudio`. Fill missing base URLs and non-positive timeouts in `GetDefaultSettings` and `LoadWithFallback`; leave models empty unless configured.

Environment overrides:

- `RCODEGEN_OLLAMA_BASE_URL`
- `RCODEGEN_OLLAMA_MODEL`
- `RCODEGEN_OLLAMA_API_KEY`
- `RCODEGEN_LMSTUDIO_BASE_URL`
- `RCODEGEN_LMSTUDIO_MODEL`
- `RCODEGEN_LMSTUDIO_API_KEY`

Do not add these models to the global `RCODEGEN_MODEL` fan-out. Do not inspect `OLLAMA_HOST`.

Tests must cover default construction, partial-settings fallback, every environment override, no global-model poisoning, and preservation of `allow_remote`.

**Acceptance:** `go test ./pkg/settings -v` passes.

### Task 3: Implement the Tool skeleton

**Files:** `pkg/tools/localai/localai.go`, `pkg/tools/localai/localai_test.go`

Required behavior:

- Constructors: `NewOllama()` and `NewLMStudio()`.
- `Name()` returns `ollama` or `lmstudio`, matching registry keys.
- `BinaryName()` returns empty.
- `ShouldUseDirectAPI()` always returns true.
- `BuildCommand()` returns a defensive `exec.Command("false")` subprocess fallback. Tests on every supported surface must prove it is not invoked.
- `ValidModels()` returns nil because identifiers are runtime-defined.
- Ollama `ValidEfforts()` returns `[]string{"none", "low", "medium", "high"}`.
- LM Studio `ValidEfforts()` returns nil for Phase 1; its OpenAI chat reasoning contract is not universal across models.
- `DefaultModel()` and `DefaultModelSetting()` return the configured model or empty.
- `ApplyToolDefaults` sets `cfg.Model` only when a model is configured.
- `ValidateConfig` rejects an empty model with a message explaining explicit `tool:model` syntax and the corresponding settings field.
- `ValidateConfig` also rejects unsupported message roles and invalid Ollama/LM Studio effort combinations when called outside rserve.
- Help text states that the tool generates text only and does not edit files or execute commands.
- Stats/run-log fields may show flavor, model, and base origin, but never the API key.

Compile-time interface assertions are added only once Tasks 4-6 provide all required methods.

**Acceptance:** localai identity, default, effort, validation, and secret-redaction tests pass.

### Task 4: Build the hardened HTTP client and origin policy

**Files:** `pkg/tools/localai/client.go`, `pkg/tools/localai/client_test.go`

Implement:

- strict origin parsing and normalization;
- loopback/private/remote policy described above;
- rejection of user info, non-root paths, query, fragment, unspecified addresses, and unsupported schemes;
- a reusable direct `http.Client` whose cloned transport has ambient proxies disabled while retaining standard pooling/dial/TLS behavior;
- `CheckRedirect` that rejects every redirect;
- shared request builder that adds JSON headers and optional bearer auth;
- path construction using parsed URLs rather than raw string concatenation.
- a per-request `context.WithTimeout` derived from `timeout_seconds`, so shared client state is never mutated and a shorter caller/inventory deadline still wins.

Tests must include:

- localhost, IPv4 loopback, IPv6 loopback, and RFC1918/private literals;
- public IP and DNS host rejection by default;
- remote host acceptance only with `allow_remote:true`;
- `0.0.0.0`, `::`, credentials, paths, query, fragment, and bad schemes rejected;
- a private/localhost test server redirecting to another server is not followed;
- auth header present when configured and absent otherwise;
- API key absent from returned errors and stats.

**Acceptance:** policy tests pass under `go test -race`.

### Task 5: Implement direct chat inference

**Files:** `pkg/tools/localai/api.go`, `pkg/tools/localai/api_test.go`

Use the common payload:

```go
type chatRequest struct {
    Model           string        `json:"model"`
    Messages        []chatMessage `json:"messages"`
    Stream          bool          `json:"stream"`
    ReasoningEffort string        `json:"reasoning_effort,omitempty"`
}
```

Rules:

- Use `cfg.Messages` in order when non-empty.
- Otherwise send one `user` message containing `task`.
- Accept only non-empty `system`, `user`, and `assistant` roles in Phase 1; reject rather than silently dropping or rewriting unsupported roles.
- Send `stream:false` to the backend in Phase 1.
- Send `reasoning_effort` only for Ollama and only when `cfg.Effort` is non-empty.
- Use the configured whole-request timeout, including LM Studio JIT loading.
- Bound response bodies at 32 MiB and explicitly detect overflow rather than silently truncating before JSON parsing.
- Decode the first text choice and usage tokens.
- Write content to `cfg.Output`.
- Write a bounded, control-character-sanitized diagnostic to `cfg.Stderr` and return nonzero for policy, transport, timeout, non-2xx, malformed JSON, oversized response, or missing-choice failures. Prefer a decoded upstream `error.message` when present; never reflect an unbounded raw response body.
- Do not echo the API key or an authorization header in diagnostics.
- `ReportedUsage` returns token counts with zero cost and `ok:true` only when usage was actually reported.

Tests must exercise both flavors, ordered system/user/assistant history, unsupported/empty roles, task fallback, Ollama effort mapping, LM Studio auth, usage, backend 401/404/500, unreachable server, timeout, cancellation, malformed JSON, missing choices, and oversized bodies.

**Acceptance:** `go test ./pkg/tools/localai -run 'TestRunDirectAPI|TestChat' -race -v` passes.

### Task 6: Implement available-model inventory

**Files:** `pkg/tools/localai/models.go`, `pkg/tools/localai/models_test.go`

- Ollama requests `/api/tags` and reads `models[].name`.
- LM Studio requests `/v1/models` and reads `data[].id`.
- Both use the same client policy and bearer header behavior as inference.
- Filter empty identifiers, deduplicate, and sort for stable responses/tests.
- Return an error on policy, transport, non-200, malformed, or oversized responses.
- Do not reinterpret the returned list as loaded-state information.

Tests cover normal, empty, duplicate, unsorted, authenticated, unreachable, cancelled, malformed, non-200, and oversized responses for both flavors.

**Acceptance:** all `pkg/tools/localai` tests pass with the race detector.

### Task 7: Integrate inventory into `/v1/models`

**Files:** `pkg/server/openai/models.go`, `pkg/server/openai/types.go`, `pkg/server/openai/handler.go`, `pkg/server/openai/models_list_test.go`

1. Treat an empty `BinaryName()` as an API-only registered tool; do not call `exec.LookPath("")`.
2. Add `context.Context` to `BuildModelList` and pass `r.Context()` from the handler.
3. Add `Available *bool` to `ModelInfo`; omit it for entries where live availability is not applicable, and set it explicitly for discovered/configured local model entries.
4. For dynamic tools implementing `DynamicModelLister`, probe concurrently with independent 500 ms child contexts so two stopped runtimes do not impose additive one-second latency.
5. Inventory failures never fail `/v1/models`.
6. Update the `Efforts` field/comment to mean accepted effort values; Ollama inventory/default entries advertise `none`, `low`, `medium`, and `high`, while LM Studio entries omit it in Phase 1.
7. Merge rules:
   - one bare dynamic tool entry;
   - one entry per discovered model, explicitly marked `available:true`;
   - the configured default included exactly once when non-empty;
   - the default explicitly marked `available:true` only if discovered and `available:false` otherwise;
   - no fabricated model when no default exists;
   - stable sort and deduplication.

Tests must use real localai tools against `httptest` backends and cover API-only detection, parallel probes, success, empty inventory, unreachable runtime, deadline, configured default present, configured default absent, no configured default, and omission of `available` on static/bare entries.

**Acceptance:** `go test ./pkg/server/openai -run 'TestDetectAvailableTools|TestBuildModelList' -race -v` passes.

### Task 8: Preserve messages and propagate rserve execution failures

**Files:** `pkg/server/openai/types.go`, `pkg/server/openai/handler.go`, `pkg/server/openai/asyncruns.go`, `pkg/server/openai/errorcodes.go`, related tests

#### Planning/validation

- Add a `ReasoningEffort string` field tagged `json:"reasoning_effort,omitempty"` to `ChatCompletionRequest`.
- For localai, validate every role as `system`, `user`, or `assistant`, then defensively copy the original request messages into `cfg.Messages`.
- Continue producing `cfg.Task` through `ExtractTaskPrompt` for CLI tools.
- Apply explicit `reasoning_effort` after defaults. For fixed namespaces, reject a conflict with an effort suffix; for explicit dynamic model IDs, never attempt suffix parsing.
- After defaults and request overrides, call `tool.ValidateConfig(cfg)` before acquiring a run slot. This is required because `runner.RunWithContext` is intentionally an execution primitive and does not apply/validate tool configuration.
- Bare local tool requests without configured defaults return HTTP 400 with `codeInvalidModel`.
- Replace the unbounded stderr `bytes.Buffer` with a capped diagnostic writer. Preserve the existing 64 KiB cap for retained async results; synchronous HTTP content remains bounded by localai's explicit 32 MiB upstream-response limit rather than being silently reduced to the async retention limit. Synchronous streaming may emit the successful completion chunk directly, but errors remain bounded.

#### Failure contract

Add retryable error code:

```go
codeToolExecutionFailed ErrorCode = "tool_execution_failed"
```

Refactor non-streaming execution so it returns the `RunResult` plus either a completion or an execution failure; HTTP and async callers must not infer success merely because the helper returned. On nonzero exit:

- synchronous HTTP returns 502 with an OpenAI error envelope;
- the message contains a bounded/sanitized stderr diagnostic when available;
- streaming HTTP emits the error envelope as an SSE event and `[DONE]`, with no false final `finish_reason:"stop"` chunk;
- async runs end with `status:"failure"`, retain the error, and deliver a failure callback;
- cancellation/shutdown retains its existing specialized error codes.

Do not change gRPC's existing exit-code reporting except for the non-nil `RunResult.Error` improvement.

Deterministic tests must cover:

- a complete rserve chat call through a real localai tool and `httptest` backend;
- original message roles/order reaching the backend;
- explicit `reasoning_effort`, suffix conflict, and literal dynamic model IDs ending in `-high`;
- usage propagation;
- sync unreachable/non-200 backend returning 502 rather than empty 200;
- streaming failure event with no success terminator;
- async failed status, retained result, and callback payload;
- API key redaction from every response;
- bounded output/stderr behavior;
- bare tool with and without configured default.

**Acceptance:** all openai handler and async tests pass under `-race`.

### Task 9: Make bundle execution honor DirectAPIRunner

**Files:** `pkg/bundle/bundle.go`, `pkg/bundle/loader_test.go`, `pkg/executor/tool.go`, `pkg/executor/tool_test.go`

Before building a subprocess command:

1. Add optional `effort` to `bundle.Step` and prove JSON decoding. An explicit field takes precedence only when no conflicting fixed-model suffix was supplied; conflicts fail validation.
2. Configure 64 KiB retained stdout/stderr buffers and the per-step log writer. The log may retain the full stream; the envelope/output artifact records truncation explicitly instead of growing memory without bound.
3. Set `cfg.WorkDirs` from the resolved bundle work directory.
4. Apply model/effort overrides, then call `tool.ValidateConfig(cfg)` before execution; return an `INVALID_CONFIG` envelope without calling either execution path on failure.
5. If the tool implements `DirectAPIRunner` and chooses direct execution, call it through `runner.RunWithContext` using the bundle context.
6. Populate the step output artifact with stdout/stderr exactly as the subprocess branch does and add an `output_truncated` result field when retained output was capped.
7. Populate usage from `RunResult.TokenUsage`/`UsageReporter` rather than CLI-output parsing.
8. Return `EXEC_FAILED` on nonzero exit and `CANCELLED` when the bundle context is cancelled.
9. Keep the existing subprocess path unchanged for CLI tools.

The primary regression fake must panic or record a failure if `BuildCommand` is called, proving the direct branch is real rather than accidentally passing through `false`.

Tests cover explicit effort, suffix conflict, literal dynamic IDs ending in an effort-like suffix, successful output, usage, backend failure, cancellation, log output, and continued CLI-path behavior.

**Acceptance:** `go test ./pkg/bundle ./pkg/executor -run 'Test.*Effort|Test.*DirectAPI|Test.*ToolExecutor' -race -v` passes.

### Task 10: Make every rbatch mode retain local-model output

**Files:** `pkg/batch/executor_local.go`, `pkg/batch/executor_remote.go`, `pkg/batch/queue.go`, `pkg/batch/manifest.go`, `pkg/batch/reporter.go`, `cmd/rbatch/main.go`, and their tests

Extend `JobResult`:

```go
Output          string `json:"output,omitempty"`
OutputTruncated bool   `json:"output_truncated,omitempty"`
```

Use a bounded writer with a 64 KiB retained-output cap:

- preserve the prefix that fits;
- continue reporting successful writes to the tool after the cap so output truncation does not fail inference;
- mark `OutputTruncated` when bytes were dropped;
- capture bounded stderr and populate `JobResult.Error` on nonzero exit;
- do not include secrets.

Local execution calls `tool.ValidateConfig(cfg)` after all defaults/job overrides and before `RunWithContext`.

Remote execution must retain the final gRPC `ResultEvent.Output` and its truncation flag; use bounded text events only as a fallback when the final result has no output, so the same completion is not duplicated. Capture `ErrorEvent`/nonzero result detail in `JobResult.Error` without losing the final exit code.

Wire the existing `WriteJobResult` helper into successful and failed job events for `run`, `spool`, and `resume`; the current helper exists but is otherwise unused. Ensure concurrent jobs cannot race result persistence. Reuse the package's existing `validBatchName` basename policy for job names, require uniqueness before execution, and keep a defense-in-depth containment check in the writer before using names below `results/`.

The output fields apply generically to ordinary/direct output; existing job result JSON remains backward compatible because the fields are optional. Summary files may remain aggregate-only because the per-job files are the durable output contract.

Tests cover local and remote success, truncation, nonzero failure, cancellation, no duplicate gRPC content, safe/unique result filenames, concurrent persistence, JSON reporter output, and all three command modes writing per-job results.

**Acceptance:** `go test ./pkg/batch ./cmd/rbatch -race -v` passes.

### Task 11: Register every surface and test registration

**Files:** `cmd/rserve/main.go`, `cmd/rserve/main_test.go`, `pkg/server/server.go`, `pkg/server/server_test.go`, `proto/rserve.proto`, generated protobuf files, `pkg/batch/manifest.go`, `pkg/batch/executor_local.go`, `cmd/rbatch/main.go`, `pkg/orchestrator/orchestrator.go`, and related tests

Register keys:

```text
ollama
lmstudio
```

- Extract rserve's default factory map into a small testable helper.
- Add both factories to `NewLocalExecutor`.
- Add both names to rbatch manifest validation and help output; error messages must derive from the accepted set rather than retain a stale hard-coded list.
- Add both tools to `orchestrator.New` and inject settings as for other settings-aware tools.
- Add `effort` to `RunTaskRequest` and `output_truncated` to `ResultEvent` using new field numbers; regenerate bindings with `make proto`.
- Copy the request effort into `cfg.Effort`, build/apply/validate the fully resolved config before acquiring a gRPC run slot, and return `InvalidArgument` for configuration failures.
- Replace gRPC's unbounded stdout/stderr buffers with bounded captures, mark truncation in the result event, and preserve its existing exit-code contract.
- Update comments listing supported tools.
- Add registration tests for all three locations.

Do not add command directories, Makefile binary targets, or launcher scripts.

**Acceptance:** `go test ./pkg/server ./cmd/rserve ./pkg/orchestrator -race -v` and registration tests pass, generated protobuf files are current, and the repository compiles through `make`, not bare `go build`.

### Task 12: Documentation and examples

**Files:** `README.md`, `API.md`, `settings.json.example`

Document:

- tool names and default origins;
- optional configured default models;
- explicit `ollama:<model>` / `lmstudio:<model>` usage;
- querying rserve `/v1/models` before choosing a model;
- `available:true` semantics;
- installed/available versus loaded distinction;
- LM Studio JIT load latency and 600-second default timeout;
- LM Studio bearer authentication and environment variables;
- loopback/private/default security policy, remote opt-in, and redirect rejection;
- no use of `OLLAMA_HOST` as an rcodegen client setting;
- HTTP `reasoning_effort`, bundle/gRPC/rbatch `effort`, and the rule that explicit dynamic model IDs are never parsed for suffixes;
- text-only/no-file-editing behavior;
- rbatch output retention/truncation;
- Phase 1 streaming limitation;
- opencode/other agentic CLI configuration remains the route for local models that must edit files or run shell tools.

The settings example includes base URLs and timeouts but does not pretend a particular model is universally installed. Show optional model/API-key fields in prose or with clearly operator-supplied example values.

Use placeholders in curl examples:

```bash
curl -s http://localhost:PORT/v1/models | python3 -m json.tool

curl -s http://localhost:PORT/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"ollama:MODEL_FROM_V1_MODELS","reasoning_effort":"low","messages":[{"role":"user","content":"Reply with exactly OK"}]}'
```

### Task 13: Deterministic verification, release metadata, commit, and push

#### 13.1 Regenerate protobufs, then verify vendor before building/debugging

Regenerate the gRPC bindings after the schema change. `go.mod` has a local replace directive, so refresh vendor before any build/debug loop:

```bash
make proto
go mod vendor
git diff --name-only -- '*.go' | xargs gofmt -w
git status --short
```

Generated and vendor changes must be understood and committed if legitimate. A second `make proto` must produce no diff. Do not assume stdlib-only source changes imply the existing vendor tree was already current.

#### 13.2 Targeted verification

Run the focused package tests from Tasks 1-11 with `-race` where supported.

#### 13.3 Full verification

```bash
make
make test
make lint
go vet ./...
```

`make` must compile every binary currently declared by `Makefile`; do not hard-code an obsolete count. If `make lint` is unavailable because the external linter is not installed, record that validation gap and still run `go vet ./...`.

#### 13.4 Deterministic end-to-end test

The automated test suite must already prove behavior with `httptest` and in-process gRPC test servers; a developer machine must not need Ollama or LM Studio installed for the suite to pass.

#### 13.5 Optional real-runtime smoke tests

If a runtime is running, discover a model dynamically rather than hard-coding one:

```bash
curl -s http://localhost:11434/api/tags
curl -s http://localhost:1234/v1/models
```

Then verify rserve model listing and chat completion with the discovered identifier. If neither runtime is installed, the deterministic tests remain the release gate and the smoke-test gap is recorded.

#### 13.6 Bug record

Create `_bugs_fixed/2026-08-28-local-direct-api-integration-gaps.md` describing:

- bundle execution previously bypassed `DirectAPIRunner`;
- rserve previously converted nonzero tool exits into successful empty completions;
- local and remote rbatch previously discarded generated output and did not wire per-job persistence;
- the regression tests that now prevent all three behaviors.

Do not read unrelated files in `_bugs_fixed`.

#### 13.7 VERSION and CHANGELOG last

Only after every required test/build check is green:

1. Read `VERSION` for the first time during implementation.
2. Increment it using the repository convention.
3. Add a CHANGELOG entry covering local tools, message preservation, explicit effort fields, available-model inventory, LM Studio auth, URL policy, correct HTTP failures, gRPC bounds, bundle direct execution, and rbatch output persistence.
4. Run `make` again so released binaries contain the final version.
5. Verify representative binaries report the new version.

#### 13.8 Commit and push

```bash
git add -A
git commit -m "Add first-class Ollama and LM Studio support (X.Y.Z)

Agent: <actual coding agent and model>"
git push
```

Never hard-code another agent's identity. Stage the entire tree as required, including scratch-folder changes created by other actors.

---

## Testable Acceptance Criteria

- [ ] `ollama:<installed-model>` returns backend-generated text through rserve.
- [ ] `lmstudio:<visible-model>` returns backend-generated text through rserve.
- [ ] A configured LM Studio API key is sent as bearer auth and never appears in logs/responses.
- [ ] System, user, and assistant messages reach the local backend in original order.
- [ ] HTTP `reasoning_effort` and bundle/gRPC/rbatch `effort` reach Ollama as `reasoning_effort` only when valid.
- [ ] An explicit dynamic model identifier ending in `-none`, `-low`, `-medium`, or `-high` is preserved byte-for-byte; no effort is inferred from it.
- [ ] A bare local tool works only with a configured default model.
- [ ] A bare local tool without a configured default returns HTTP 400 before run-slot acquisition.
- [ ] An unreachable or rejecting backend returns synchronous HTTP 502, not HTTP 200 with empty content.
- [ ] Streaming failure produces an SSE error and `[DONE]`, not a false stop completion.
- [ ] Async backend failure ends in `status:"failure"` and delivers a failure callback.
- [ ] Cancellation aborts in-flight local HTTP requests and releases run slots.
- [ ] Bundle direct execution never calls `BuildCommand`.
- [ ] Bundle output, usage, failure, and cancellation are represented in step envelopes.
- [ ] Local and remote rbatch retain at most 64 KiB of generated output, mark truncation, and write safe per-job result files in run/spool/resume modes.
- [ ] `/v1/models` returns available Ollama and LM Studio identifiers when their backends respond.
- [ ] `/v1/models` remains successful within the bounded probe window when either backend is stopped.
- [ ] Discovered inventory entries explicitly say `available:true`; configured-but-undiscovered defaults explicitly say `available:false`; static/bare entries omit the field.
- [ ] The configured default appears exactly once and is marked available only when discovered.
- [ ] No hard-coded model is fabricated when no local default is configured.
- [ ] Redirects are rejected and cannot bypass the host policy.
- [ ] Public/DNS endpoints require `allow_remote:true`; invalid origins are rejected.
- [ ] Ollama sends only supported configured effort values.
- [ ] Existing Claude, Codex, Gemini, OpenCode, and KiloCode tests remain green.
- [ ] `make proto` is idempotent; `go mod vendor`, `make`, `make test`, available linting, and `go vet ./...` complete with no unexplained changes/failures.
- [ ] VERSION, CHANGELOG, bug record, commit attribution, commit, and push follow repository rules.

---

## Risks and Mitigations

### Public API behavior change

Existing rserve behavior can convert CLI/direct tool failures into empty HTTP 200 responses. Correcting this to 502 is externally visible.

**Mitigation:** Add explicit sync, stream, and async regression tests and document the corrected behavior in CHANGELOG.

### Inventory latency

Two stopped local runtimes could otherwise delay `/v1/models`.

**Mitigation:** Run probes concurrently, give each a 500 ms child context, and degrade without failing the endpoint. Add caching only if measured latency later justifies it.

### JIT load latency

LM Studio's first request may take minutes.

**Mitigation:** Default whole-request timeout to 600 seconds, preserve cancellation, and document the behavior.

### Secret leakage

Tokens stored in settings or environment could leak through diagnostics or stats.

**Mitigation:** Keep credentials in a dedicated field/header, never in the base URL, and add redaction assertions across tool/server tests.

### Bundle executor divergence

Adding a direct branch can drift from subprocess output/envelope behavior.

**Mitigation:** Share output finalization where possible and test both branches in `pkg/executor/tool_test.go`.

### Output memory pressure

Local models can emit large completions.

**Mitigation:** Detect the 32 MiB upstream response cap and retain only 64 KiB in rbatch, bundle envelopes, gRPC result events, and async durable result surfaces. Mark truncation instead of silently dropping bytes. Synchronous HTTP responses remain bounded by the upstream cap.

### Dynamic model/effort ambiguity

Runtime-defined model names may legitimately end with strings such as `-high`.

**Mitigation:** Never infer effort suffixes from explicit models in dynamic namespaces. Carry effort in dedicated HTTP/gRPC/rbatch/bundle fields and add literal-identifier regressions.

### rbatch result-path safety

Wiring the previously unused per-job writer would turn manifest job names into filenames.

**Mitigation:** Reuse the existing `validBatchName` policy, require unique job names before concurrent execution, retain a containment check in the writer, and prove result paths remain inside the batch result directory.

### Runtime API evolution

Ollama and LM Studio APIs continue to add features.

**Mitigation:** Depend only on documented stable endpoints, keep request types deliberately small, and link official sources in code comments/docs where compatibility choices are non-obvious.

---

## Follow-Up Plan

After this plan ships, evaluate a separate Phase 2 for:

1. True upstream SSE token passthrough and usage in the final stream chunk.
2. LM Studio native `/api/v1/models` metadata: loaded instances, quantization, context, and load configuration.
3. Ollama `/api/ps` loaded-state metadata.
4. Ollama `/api/show` prompt-size/context-window preflight to prevent silent truncation.
5. LM Studio native stateful chat or OpenAI Responses support.
6. Rich OpenAI parameter forwarding with per-flavor capability tests.
7. Backend reachability in `/health` without making local runtimes required for rserve health.
8. Optional model-inventory caching if measurements show `/v1/models` latency is material.

This follow-up must preserve the Phase 1 distinction between **available inventory** and **currently loaded models**.
