# First-Class Ollama and LM Studio Support in rcodegen

**Date:** August 27, 2026

## Summary

rcodegen today has **zero** Ollama or LM Studio awareness. The only path to a local model is indirect: configure a local provider inside the `opencode`/`kilocode` CLIs and pass `-m ollama/<model>` through untouched. This study defines what *first-class* support would look like, maps it onto the existing architecture (`runner.Tool`, `DirectAPIRunner`, settings, rserve model routing), and proposes a three-phase roadmap. The key insight: **"support" splits into two very different problems** — plain inference (easy, the `DirectAPIRunner` hook already exists) and agentic coding (hard, requires an agent harness rcodegen does not have).

---

## 1. Where the codebase stands today

### Architecture recap

- rcodegen is a **CLI-wrapper framework**: `runner.Tool` (`pkg/runner/tool.go:10`) is a ~25-method interface; five implementations shell out to external binaries: `claude`, `codex`, `gemini`, `opencode`, `kilocode`.
- Tool registration happens at **four sites**:
  - `cmd/<tool>/main.go` — one dedicated binary per tool (`rclaude`, `rcodex`, `rgemini`, `ropencode`, `rkilo`)
  - `cmd/rserve/main.go:98–102` — `map[string]func() runner.Tool` factories for the HTTP server
  - `pkg/batch/executor_local.go:37–41` — same map for rbatch
  - `pkg/orchestrator/orchestrator.go:212–216` — same map for bundles
- **`DirectAPIRunner`** (`pkg/runner/tool.go:102`) is the existing escape hatch for bypassing the CLI and calling an HTTP API directly. Gemini's image models already use it (`pkg/tools/gemini/image_api.go`) — including context cancellation, timeout capping, and writing to `cfg.Output`. This is the exact precedent a local-model tool needs.
- **Settings** (`~/.rcodegen/settings.json`) hold per-tool default structs (`pkg/settings/settings.go:63–69`); env overrides layer on top.
- **rserve routing**: `ParseModel` (`pkg/server/openai/models.go:17`) splits `tool:model` on the **first** colon only — so `ollama:llama3.1:8b` cleanly yields tool `ollama`, model `llama3.1:8b`. Tools returning `ValidModels() == nil` get a dynamic namespace (no validation); `/v1/models` enumerates fixed lists.

### The current indirect path

`ropencode`/`rkilo` forward `-m provider/model` blind (`pkg/tools/opencode/opencode.go:74`). The opencode CLI natively supports Ollama and LM Studio as providers (both expose OpenAI-compatible endpoints), so `ropencode -m ollama/qwen3:14b` works **today** — but only if the user has configured the provider inside opencode themselves. rcodegen has no knowledge, no validation, no model listing, no health checks. That is the definition of not-first-class.

---

## 2. The two local runtimes, as APIs

| | Ollama | LM Studio |
|---|---|---|
| Default port | `11434` | `1234` |
| OpenAI-compat surface | `/v1/chat/completions`, `/v1/completions`, `/v1/models`, `/v1/embeddings`, `/v1/responses` (v0.13.3+, non-stateful) | `/v1/chat/completions`, `/v1/completions`, `/v1/models`, `/v1/embeddings`, `/v1/responses` (Codex support) |
| Native API | `/api/tags` (list), `/api/chat`, `/api/generate`, `/api/show` — streams **NDJSON, not SSE** | `/api/v1/*` (LM Studio 0.4.0+): stateful chats, MCP via API, auth tokens, model download/load/unload. `/api/v0` is deprecated |
| Model naming | `name:tag` (e.g. `llama3.1:8b`, `qwen3:14b`) | Hugging-Face-style paths (e.g. `qwen/qwen3-14b`) |
| Env/config | `OLLAMA_HOST` | `lms` CLI; JIT model loading + auto-evict on by default |
| Tool calling | Yes on `/v1` — but **`tool_choice` is unsupported** | Yes (model-dependent quality) |
| Reasoning toggle | `/v1` accepts `reasoning_effort`/`reasoning`; native `think` takes `low/medium/high/max` | reasoning-effort field for some models |

Both speak OpenAI chat completions at the core, which means **one shared implementation covers both**, differing only in base URL and model-listing endpoint. The djb2 port rule in the workspace guidelines does not apply here — these are third-party servers with fixed conventional ports.

### 2.1 Where they break out of the OpenAI format (verified Aug 2026)

Neither is a faithful OpenAI clone. The divergences that matter to this design:

**Ollama `/v1` gaps:**
- **Unsupported request fields:** `tool_choice`, `logit_bias`, `n`, `user`, and all logprobs functionality. The missing `tool_choice` means a harness cannot *force* a tool call — a direct constraint on any Phase-3 agent loop built over the compat endpoint.
- **Context length is not settable via `/v1`.** `num_ctx` requires a Modelfile or the native API, and Ollama **silently truncates** prompts that exceed the model's context. For rcodegen tasks that pack source code into the prompt, this is a silent-corruption hazard: the model answers confidently about a truncated codebase. Mitigation: create derived models with a larger `num_ctx`, or use the native `/api/chat` with `options.num_ctx` per request.
- **Native-API-only controls:** `keep_alive` (model residency) and `think` live only on `/api/chat`/`/api/generate`. The `/v1` layer does accept `reasoning_effort`, which is the cleaner hook for effort mapping — though the native `think` levels have a known top-level-vs-`options` inconsistency (ollama#15831).
- Vision input is base64-only; image URLs are rejected. `/v1/responses` is the non-stateful flavor only.

**LM Studio extensions and behaviors:**
- **`ttl` is a custom non-OpenAI field accepted inside OpenAI-compat request bodies** — it sets the model's idle-unload timer. Harmless to omit, but proof the surface is extended, not cloned.
- **JIT loading + auto-evict (both default-on):** the first request to an unloaded model blocks while the model loads into memory, and loading a second model can evict the first. To a naive HTTP client this looks like a hang or a latency cliff. rcodegen's timeout design must distinguish load-time from inference-time — or pre-warm via a health probe before dispatching a run.
- The native surface moved: `/api/v0` is deprecated; LM Studio 0.4.0 ships `/api/v1` with stateful chats, MCP-via-API, API-token auth, and model download/load/unload endpoints. Model-management integration (Phase 2) should target `/api/v1`, not v0.

**Design consequences:**
1. Phase 1 stands — plain chat completions work identically on both — but the implementation must treat the compat layer as a *subset+extensions* dialect, not OpenAI proper: never send `tool_choice`/`logit_bias` to Ollama, and budget generous first-request timeouts for LM Studio JIT loads.
2. The prompt-truncation hazard promotes a new Phase-1 requirement: **estimate prompt size against the model's context window** (Ollama `/api/show` reports it; LM Studio `/api/v1` model metadata includes it) and refuse or warn instead of silently truncating.
3. Effort mapping (Phase 2) should ride `reasoning_effort` on `/v1` for Ollama rather than the native `think` parameter — simpler and one dialect fewer.
4. Any Phase-3 agent harness needs the *native* APIs (or must tolerate advisory-only tool selection), reinforcing the study's recommendation to delegate agentic work to opencode instead.

---

## 3. What "first-class" actually means — two tiers

### Tier A: Inference support (chat-style tasks)

Route `ollama:<model>` / `lmstudio:<model>` through rserve and the CLIs for **pure text generation**: the `generate` task, summarization, grading a provided report, bundle steps that transform text. No file editing, no shell.

This is genuinely cheap: a `runner.Tool` + `DirectAPIRunner` where `ShouldUseDirectAPI` always returns true, ~300 lines modeled directly on `image_api.go`.

### Tier B: Agentic support (audit / fix / refactor tasks)

rcodegen's bread-and-butter tasks require an agent that reads the codebase, runs commands, writes report files. A raw chat completion cannot do that. Three ways to get there, in ascending ambition:

1. **Ride opencode** (works now, polish it): opencode already has the agent loop and local-provider support. First-class polish = rcodegen-side provider config, model discovery, validation, health checks.
2. **Ride codex CLI**: codex supports `model_providers` with custom `base_url` (and an `--oss` mode targeting Ollama). Similar polish path.
3. **Build a native Go agent loop**: implement OpenAI tool-calling (`read_file`, `write_file`, `list_dir`, `run_command`) against the local endpoint. This makes rcodegen itself a coding agent — a major scope expansion with real security surface (arbitrary command execution driven by a local model) and quality risk (small local models are unreliable tool-callers).

---

## 4. Proposed design

### 4.1 New package: `pkg/tools/localai`

One implementation, two registrations:

```go
// pkg/tools/localai/localai.go
type Tool struct {
    settings *settings.Settings
    flavor   Flavor // FlavorOllama | FlavorLMStudio
}

func NewOllama() *Tool   { return &Tool{flavor: FlavorOllama} }
func NewLMStudio() *Tool { return &Tool{flavor: FlavorLMStudio} }
```

Key interface decisions:

| Method | Behavior |
|---|---|
| `Name()` | `"rollama"` / `"rlmstudio"` |
| `BinaryName()` | `""` — no CLI; runner must learn to skip the binary-existence check when a tool is pure-DirectAPI |
| `ValidModels()` | `nil` (dynamic namespace) — but see live discovery below |
| `ValidEfforts()` | `nil` initially; later map `low/high` → Ollama `think: false/true` for reasoning models |
| `ShouldUseDirectAPI()` | always `true` |
| `RunDirectAPI()` | POST `/v1/chat/completions` with context cancellation and a configurable timeout (local models on modest hardware can legitimately take minutes — the 5-minute image cap is the wrong default here) |
| `ReportedUsage()` | populate from the response `usage` block; `CostUSD` stays 0 with `ok=true` — local inference genuinely is free, which is the one case where $0.00 is honest |

### 4.2 Settings

```json
{
  "defaults": {
    "ollama":   { "base_url": "http://localhost:11434", "model": "qwen3:14b",        "timeout_seconds": 600 },
    "lmstudio": { "base_url": "http://localhost:1234",  "model": "qwen/qwen3-14b",   "timeout_seconds": 600 }
  }
}
```

Plus env overrides `RCODEGEN_OLLAMA_BASE_URL` / `RCODEGEN_LMSTUDIO_BASE_URL`. Respecting `OLLAMA_HOST` when set would match Ollama-ecosystem convention.

**Security note:** rserve already hardens callback dialing (4.3.x work on host policy at dial time). A user-configurable base URL is a new outbound-request primitive inside the server — it should be constrained to loopback/LAN by default with an explicit opt-out, or it becomes a server-side request forgery vector when rserve is exposed on a LAN.

### 4.3 Live model discovery in `/v1/models`

Today `/v1/models` enumerates static lists. Local runtimes make the real list knowable at request time:

- Ollama: `GET /api/tags`
- LM Studio: `GET /v1/models` (or `/api/v1` model endpoints for loaded-state metadata)

Add an optional interface:

```go
type DynamicModelLister interface {
    // ListModelsLive returns discovered model names, or (nil, err) when the
    // backend is unreachable. Callers must degrade gracefully.
    ListModelsLive(ctx context.Context) ([]string, error)
}
```

`BuildModelList` merges live results with a short per-request timeout (~500 ms) and a `"live": true` flag on entries; if the runtime is down, the tool is listed with zero models rather than failing the endpoint. This also fixes the silent-rejection class of bug (S2023) for local models: validation can check the live list and return a 400 naming what *is* installed.

### 4.4 Parsing edge cases (real, verified)

- `ParseModel` splits on the first colon only — Ollama tags survive (`ollama:llama3.1:8b` → `llama3.1:8b`). ✅
- `SplitModelEffort` strips effort suffixes like `-high` from model strings. `ollama:qwen3:14b-high` is ambiguous with quant-suffixed tags (`-q4_K_M`, `-instruct`). Since `ValidEfforts()` is nil, the suffix path is inert — keep it that way until effort mapping is designed deliberately.

### 4.5 Registration and binaries

- Add factories at all four registration sites (`rserve`, `rbatch`, orchestrator, plus optional dedicated binaries `cmd/rollama`, `cmd/rlmstudio`).
- Makefile: two new targets if dedicated binaries ship. Note `rkilo`/`ropencode` already exist in `cmd/` but the Makefile's documented six targets don't include them — precedent that server-only tools don't need standalone binaries. Recommend **server-only in Phase 1** (skip the binaries), matching that precedent and keeping scope small.

### 4.6 What Tier-B agentic support would need (Phase 3, if ever)

- A tool-calling loop in Go: define ~5 tools, execute them under the existing workspace/clone sandbox (`pkg/workspace`), enforce the artifact bounds and admission control that 4.3.x built for CLI runs.
- Guardrails: path confinement to the work dir, command allowlist, step budget, wall-clock budget.
- Honest expectations: 7B–32B local models complete multi-step coding tasks unreliably. The `audit`-style report tasks (read many files, write one report) are the most feasible; `fix` (edit code correctly) is the least.
- Alternative that avoids all of this: document and polish the opencode path (Phase 0/1) and let opencode own the agent loop. **This is the recommended posture** — building a native agent harness duplicates what three wrapped CLIs already do, just to serve weaker models.

---

## 5. Roadmap

| Phase | Scope | Effort | Value |
|---|---|---|---|
| **0** | Document the existing opencode/kilocode local-provider path (README + help text already show `provider/model` examples; add explicit Ollama/LM Studio recipes) | Hours | Unlocks agentic local models *now* |
| **1** | `pkg/tools/localai` DirectAPI tool, settings, registration in rserve/rbatch/orchestrator, `/v1/models` live discovery, base-URL security policy | Days | First-class inference: `curl rserve /v1/chat/completions -d '{"model":"ollama:qwen3:14b", ...}'` |
| **2** | Effort mapping via `reasoning_effort` on `/v1` (not native `think`), streaming SSE passthrough, LM Studio `/api/v1` metadata (loaded/quant/context) in model listing, health surfaced in `/healthz` detail | Days | Parity with cloud-tool ergonomics |
| **3** | Native Go agent loop for local models | Weeks | Probably not worth it — prefer Phase 0's delegation to opencode |

## 6. Open questions

1. **Server-only or standalone binaries?** Recommendation: server-only (rserve/rbatch/bundles) in Phase 1; add `rollama` later only if command-line demand appears.
2. **One tool name or two?** `ollama` and `lmstudio` as separate tool names is clearer than a generic `local:` prefix and matches the per-tool settings shape.
3. **Generic OpenAI-compatible endpoint?** The same `localai` package trivially generalizes to *any* OpenAI-compatible server (vLLM, llama.cpp server, LiteLLM). Naming it `openaicompat` with named endpoint profiles in settings may be the better long-term shape — Ollama and LM Studio become two built-in profiles rather than two hardcoded flavors.
4. **Does rserve want to be a proxy?** rserve already speaks the OpenAI chat-completions dialect on the front. With Phase 1, it becomes a unifying facade: one endpoint, cloud agentic CLIs and local models behind it, selected purely by model string. That symmetry is the strongest argument that this feature belongs in the codebase.
