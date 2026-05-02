# Headroom Sidecar Integration - Production Implementation Plan

Status: production-ready planning artifact, not yet implemented.

## Goal

Integrate Headroom into rcodegen as an opt-in managed sidecar so spawned tool
CLIs can route LLM traffic through a local compression proxy without changing
their request-generation logic.

Supported target surfaces:

- Single-tool CLIs: `rclaude`, `rcodex`, `rgemini`, `ropencode`, `rkilo`
- Bundle/orchestrated CLI: `rcodegen`
- Batch CLI: `rbatch`
- Server mode: `rserve` gRPC and OpenAI-compatible HTTP API

Primary success criteria:

- Existing behavior is unchanged when Headroom is disabled.
- When enabled and ready, supported subprocesses receive the correct provider
  routing configuration.
- When enabled but unavailable and `fail_open=true`, subprocesses run directly
  without proxy env/config injection.
- Headroom stats are captured from the real `/stats` schema and surfaced in
  CLI, JSON, gRPC, HTTP, and event outputs where applicable.

## Non-Goals

- Do not embed Headroom's Rust/Python internals or add cgo bindings.
- Do not implement `headroom learn`, shared memory, RTK output filtering, image
  compression, or code-graph setup in this plan.
- Do not mutate user-level tool config globally unless the tool cannot be
  routed safely by process-local configuration. Codex is the only known
  exception candidate and must prefer reversible, process-local config first.
- Do not claim Gemini/OpenCode/KiloCode routing works until their current CLI
  endpoint override contract is verified by source/docs and smoke tests.

## Grounded Facts From Current Code

- Headroom exposes `/readyz` for readiness and `/health` for aggregate health.
  `/health` currently returns HTTP 200 even when a component is unhealthy, so
  readiness checks must use `/readyz`.
- Headroom `/stats` is nested. Savings live under fields such as
  `tokens.saved`, `tokens.savings_percent`,
  `summary.compression.requests_compressed`, and
  `summary.compression.total_tokens_removed`, not top-level `tokens_saved`.
- Headroom's own Codex wrapper says Codex ignores `OPENAI_BASE_URL` for some
  WebSocket traffic unless a custom provider declares `supports_websockets =
  true`. Codex integration cannot be treated as a plain env-var injection.
- In rcodegen, only Claude currently sets an explicit command environment.
  Other `BuildCommand` implementations return `cmd.Env == nil`, which means
  they inherit the parent process environment. Env injection must preserve that
  inheritance.
- rcodegen has more execution paths than the single-tool CLIs. Bundle execution
  uses `pkg/executor`, server mode builds `runner.Config` in both
  `pkg/server` and `pkg/server/openai`, and `RunWithContext` rebuilds commands
  after calling `BuildCommand`.
- The rcodegen repo requires `make` builds with ldflags, not bare `go build`.
  Before build/debug work, run `go mod vendor` when local replace directives
  are active.

## Design Principles

- Desired config and effective runtime state are separate. A user can request
  Headroom, but env/config injection is allowed only after a compatible proxy is
  confirmed ready.
- Fail-open means "run directly", not "inject a dead proxy URL".
- Tool routing is tool-specific and minimal. Do not inject unrelated provider
  env vars into every subprocess by default.
- Process lifecycle is centralized per execution surface: once per single CLI
  run, once per bundle/batch, once per server process.
- Port collision policy is explicit and testable.
- Production verification uses unit tests for contracts, integration tests for
  command/env behavior, and smoke tests against a real Headroom binary.

## Architecture

Add a new `pkg/headroom` package with three layers:

1. Desired configuration from settings/env/CLI flags.
2. Manager lifecycle that starts or adopts a compatible Headroom proxy and
   returns an effective runtime state.
3. Tool-specific command routing helpers that apply env/config only when the
   effective state is active.

Core types:

```go
type Config struct {
    Enabled      bool
    Host         string
    Port         int
    BinaryPath   string
    LogPath      string
    StartTimeout time.Duration
    ProbeTimeout time.Duration
    FailOpen     bool
    Telemetry    bool
    AutoPort     bool
}

type EffectiveConfig struct {
    Config
    Active          bool
    AdoptedExternal bool
    ProxyURL        string
    DisableReason   string
}

func (e EffectiveConfig) ShouldInject() bool {
    return e.Enabled && e.Active && e.ProxyURL != ""
}
```

`runner.Config` should carry `Headroom *headroom.EffectiveConfig`, not just the
desired settings. Tool `BuildCommand` methods must never decide whether to
start a proxy.

## File Plan

Create:

- `pkg/headroom/doc.go`
- `pkg/headroom/config.go`
- `pkg/headroom/config_test.go`
- `pkg/headroom/settings.go`
- `pkg/headroom/settings_test.go`
- `pkg/headroom/probe.go`
- `pkg/headroom/probe_test.go`
- `pkg/headroom/manager.go`
- `pkg/headroom/manager_test.go`
- `pkg/headroom/env.go`
- `pkg/headroom/env_test.go`
- `pkg/headroom/codex.go`
- `pkg/headroom/codex_test.go`
- `pkg/headroom/stats.go`
- `pkg/headroom/stats_test.go`
- `pkg/headroom/testhelper_test.go`
- `docs/headroom-integration.md`

Modify:

- `pkg/settings/settings.go`
- `settings.json.example`
- `pkg/runner/config.go`
- `pkg/runner/runner.go`
- `pkg/runner/output.go`
- `pkg/runner/tool.go` if a tool-kind hook is needed
- `pkg/tools/claude/claude.go`
- `pkg/tools/codex/codex.go`
- `pkg/tools/gemini/gemini.go`
- `pkg/tools/opencode/opencode.go`
- `pkg/tools/kilocode/kilocode.go`
- `pkg/executor/tool.go`
- `pkg/orchestrator/orchestrator.go`
- `cmd/rcodegen/main.go`
- `cmd/rbatch/main.go`
- `cmd/rserve/main.go`
- `pkg/server/server.go`
- `pkg/server/openai/handler.go`
- `pkg/server/pb/rserve.proto` and generated files if gRPC response schema is
  extended
- `README.md`
- `CHANGELOG.md`
- `VERSION`
- `Makefile`
- `AGENTS.md` only if maintainers want agent-facing Headroom notes in repo
  instructions

## Phase 0 - Contract Validation Before Coding

This phase prevents building against guessed CLI contracts.

- [ ] Verify the installed/current Headroom command contract:
  - `headroom proxy --host 127.0.0.1 --port <port>`
  - `/readyz` returns 200 only when traffic-ready.
  - `/health` identifies Headroom and includes config/pid.
  - `/stats` contains `tokens.saved`,
    `summary.compression.requests_compressed`, and
    `summary.compression.total_tokens_removed`.
- [ ] Verify Codex process-local routing options:
  - First preference: pass `-c model_provider="headroom"` and
    `-c model_providers.headroom...` values on the `codex` command line.
  - Fallback: reversible temporary config file injection with backup/restore.
  - Do not ship Codex support if only `OPENAI_BASE_URL` is proven.
- [ ] Verify Gemini CLI endpoint override source/docs and smoke behavior against
  Headroom's `/v1beta/models/{model}:generateContent` route.
- [ ] Verify OpenCode and KiloCode endpoint override behavior per provider.
  If they use provider-specific settings files rather than env vars, document
  and implement that path separately.
- [ ] Record validation results in this proposal or the implementation PR.

Stop condition: do not start Phase 1 until Claude and Codex contracts are known.
Gemini/OpenCode/KiloCode may remain behind feature gates if their contracts are
not verified yet.

## Phase 1 - Headroom Package

### 1.1 Config and Validation

- [ ] Implement `Config`, `DefaultConfig`, `Validate`, and `BaseURL`.
- [ ] Defaults:
  - `Enabled=false`
  - `Host=127.0.0.1`
  - `Port=8787`
  - `FailOpen=true`
  - `StartTimeout=10s`
  - `ProbeTimeout=2s`
  - `Telemetry=false` when rcodegen manages the sidecar
- [ ] Validation:
  - Disabled configs are valid.
  - Enabled config requires loopback host unless an explicit unsafe override is
    added later.
  - Port must be 1-65535 unless `AutoPort=true`.
  - `LogPath`, if set, must be creatable by the manager.
- [ ] Unit tests cover defaults, invalid ports, empty host, loopback guard,
  `AutoPort`, and fail-open defaults.

### 1.2 Settings Conversion

- [ ] Add `settings.HeadroomDefaults` without importing `pkg/headroom` into
  `pkg/settings`.
- [ ] Use setting fields that unmarshal cleanly from JSON:
  - `enabled`
  - `host`
  - `port`
  - `binary_path`
  - `log_path`
  - `fail_open`
  - `telemetry`
  - `auto_port`
  - optional timeout strings or millisecond integers, not raw `time.Duration`
- [ ] Add env overrides:
  - `RCODEGEN_HEADROOM_ENABLED`
  - `RCODEGEN_HEADROOM_HOST`
  - `RCODEGEN_HEADROOM_PORT`
  - `RCODEGEN_HEADROOM_BINARY`
  - `RCODEGEN_HEADROOM_LOG_PATH`
  - `RCODEGEN_HEADROOM_FAIL_OPEN`
  - `RCODEGEN_HEADROOM_TELEMETRY`
  - `RCODEGEN_HEADROOM_AUTO_PORT`
- [ ] Implement `headroom.FromSettings(settings.HeadroomDefaults) Config`.
- [ ] Preserve explicit `false` values. Do not use `x || defaultTrue` for
  `fail_open`.
- [ ] Tests prove merge order: defaults < settings.json < env vars < CLI flags.

### 1.3 Readiness Probe and Existing Proxy Adoption

- [ ] Implement:
  - `ProbeReady(baseURL string, timeout time.Duration) error`
  - `ProbeHealth(baseURL string, timeout time.Duration) (Health, error)`
  - `IsCompatibleHeadroom(baseURL string) bool`
- [ ] Use `/readyz` for readiness.
- [ ] Use `/health` only for identity/config/pid details.
- [ ] Tests use `httptest.Server` for:
  - ready 200
  - ready 503
  - unreachable
  - `/health` 200 but `/readyz` 503
  - non-Headroom service on the same port

### 1.4 Manager Lifecycle

- [ ] Implement `Manager.Start(ctx) (EffectiveConfig, error)` and
  `Manager.Stop(ctx) error`.
- [ ] Start policy:
  - If disabled: return inactive effective config.
  - If target port has a compatible ready Headroom proxy: adopt it and do not
    own its shutdown.
  - If target port has a non-Headroom service: fail-open or error.
  - If no listener exists: start `headroom proxy`.
  - If `AutoPort=true`: choose a free loopback port before start.
- [ ] Process env:
  - Set `HEADROOM_TELEMETRY=off` unless `Telemetry=true`.
  - Do not enable prompt/message logging by default.
  - Preserve user credentials and provider env vars for Headroom itself.
- [ ] Process safety:
  - Drain stdout/stderr to configured log path or bounded logger sink.
  - On Unix, terminate the process group where practical.
  - Stop with graceful signal, then kill on timeout.
  - Concurrent `Start` calls are idempotent.
  - `Stop` only stops processes started by this manager, never adopted proxies.
- [ ] Fail-open semantics:
  - If `FailOpen=true`, return `EffectiveConfig{Enabled:true, Active:false,
    DisableReason:<reason>}` and no error.
  - If `FailOpen=false`, return an error.
- [ ] Tests use a Go helper process, not shell/Python test fixtures, to avoid
  platform and interpreter dependencies.

### 1.5 Tool Routing Helpers

- [ ] Implement `InjectProxyEnv(cmd *exec.Cmd, eff *EffectiveConfig, tool ToolKind)`.
- [ ] If `cmd.Env == nil`, start from `os.Environ()` before appending.
- [ ] If `eff == nil` or `!eff.ShouldInject()`, leave `cmd.Env` unchanged.
- [ ] Respect user overrides for the specific provider variable being set.
- [ ] Be idempotent.
- [ ] Tool-specific routing:
  - Claude: `ANTHROPIC_BASE_URL=<proxy-url>`
  - OpenAI-compatible: `OPENAI_BASE_URL=<proxy-url>/v1`
  - Legacy OpenAI tools only when verified: `OPENAI_API_BASE=<proxy-url>/v1`
  - Gemini: only after Phase 0 confirms the current Gemini CLI override.
  - OpenCode/KiloCode: only after Phase 0 confirms provider-specific behavior.
- [ ] Add tests for nil env inheritance, override preservation, idempotency,
  disabled effective config, inactive fail-open config, and per-tool env sets.

### 1.6 Codex Routing

- [ ] Implement a dedicated Codex routing helper. Do not rely on
  `OPENAI_BASE_URL` alone.
- [ ] Preferred implementation:
  - Use Codex CLI `-c` overrides in `pkg/tools/codex.BuildCommand` to set a
    `headroom` model provider with `base_url=<proxy-url>/v1`,
    `env_key=OPENAI_API_KEY`, `requires_openai_auth=true`, and
    `supports_websockets=true`.
  - Keep all changes process-local.
- [ ] If process-local provider config is impossible:
  - Implement temporary config injection with marker blocks.
  - Snapshot and restore user config.
  - Use file locking to prevent concurrent corruption.
  - Add explicit tests for restore on success and failure.
- [ ] Add dry-run output that makes Codex routing visible without exposing keys.

### 1.7 Stats Parser

- [ ] Implement `FetchStats(baseURL string, timeout time.Duration) (Stats, error)`.
- [ ] Parse the real nested schema:
  - `tokens.saved`
  - `tokens.savings_percent`
  - `summary.compression.requests_compressed`
  - `summary.compression.total_tokens_removed`
  - optional `requests.total`, `requests.failed`, `savings.total_tokens`
- [ ] Be tolerant of missing fields and schema additions.
- [ ] Implement `Stats.Sub(before Stats) Stats` with clamping for counters that
  reset after proxy restart.
- [ ] Tests cover full payload, missing fields, bad JSON, unreachable server,
  counter reset, and the exact fields used by the UI/API.

## Phase 2 - Runner and Tool Wiring

### 2.1 Runner Config

- [ ] Add `Headroom *headroom.EffectiveConfig` to `runner.Config`.
- [ ] Add `HeadroomStats *HeadroomRunStats` to `RunResult`.
- [ ] Add matching fields to stats JSON types.

### 2.2 Single-Tool CLI Lifecycle

Centralize this in `runner.Run()` so `cmd/rclaude`, `cmd/rcodex`,
`cmd/rgemini`, `cmd/ropencode`, and `cmd/rkilo` do not duplicate logic.

- [ ] Add common flags in `runner.parseArgs`:
  - `--headroom`
  - `--no-headroom`
  - `--headroom-port`
  - `--headroom-binary`
  - `--headroom-log`
  - `--headroom-fail-closed`
  - `--headroom-auto-port`
- [ ] After settings and flags are resolved, start/adopt the manager once.
- [ ] Store the returned effective config on `cfg.Headroom`.
- [ ] Defer manager stop until all workdirs/reports finish.
- [ ] If fail-open produced inactive config, print/log a concise warning and
  run without injection.
- [ ] Tests cover flag override stacking and fail-open inactive config.

### 2.3 Tool BuildCommand Updates

- [ ] Claude: inject only `ANTHROPIC_BASE_URL` when active.
- [ ] Codex: use dedicated Codex provider routing helper plus
  `OPENAI_BASE_URL` if still useful.
- [ ] Gemini: gate behind verified Phase 0 routing contract.
- [ ] OpenCode/KiloCode: gate provider routing behind verified Phase 0 routing
  contracts.
- [ ] Update tests for each supported tool:
  - enabled active config injects expected routing
  - inactive fail-open config injects nothing
  - user override wins
  - nil `cmd.Env` still preserves parent env
  - resume path is covered where tools have special resume commands

### 2.4 Stats Around Runs

- [ ] Capture stats before and after each subprocess run when Headroom is
  active.
- [ ] Store deltas on `cfg.HeadroomStats` and `RunResult.HeadroomStats`.
- [ ] Do not fail the run if stats fetch fails; log/debug and continue.
- [ ] For multi-workdir and suite runs, aggregate deltas across subprocesses.
- [ ] Print a compact summary in the footer:
  - tokens saved
  - requests compressed
  - savings percent when available
- [ ] Add `headroom` block to `--stats-json`.

## Phase 3 - Bundle, Batch, and Server Surfaces

### 3.1 Bundle / `rcodegen`

- [ ] Start/adopt one Headroom manager for the entire bundle run in
  `cmd/rcodegen` or `orchestrator.New`.
- [ ] Pass effective config into `pkg/executor.ToolExecutor`.
- [ ] Set `cfg.Headroom` before each tool `BuildCommand`.
- [ ] Aggregate Headroom stats into step envelopes and final bundle summary.
- [ ] Tests use a fake executor/tool to prove the same effective config is used
  across steps and no dead proxy is injected on fail-open.

### 3.2 Batch / `rbatch`

- [ ] Start/adopt one manager at batch entry.
- [ ] Reuse the same effective config across all jobs.
- [ ] Stop only after all jobs finish.
- [ ] Add tests proving one manager start for multiple jobs.

### 3.3 Server / `rserve`

- [ ] Start/adopt one manager in `cmd/rserve` lifecycle before accepting runs.
- [ ] Pass effective config into both:
  - `pkg/server.Server`
  - `pkg/server/openai.Handler`
- [ ] Set `cfg.Headroom` on every per-request `runner.Config`.
- [ ] Add `headroom` to health/diagnostic output without leaking credentials.
- [ ] On shutdown, stop only if rserve started the sidecar.
- [ ] Add tests for gRPC and OpenAI HTTP config propagation.

### 3.4 gRPC / Event Schema

- [ ] Add a `HeadroomStats` protobuf message if API consumers need structured
  stats.
- [ ] Add optional `headroom_stats` to result events.
- [ ] Regenerate protobuf files using the repo's established command.
- [ ] Preserve backward compatibility by only adding optional fields.

### 3.5 Kafka / OTel

- [ ] Add OTel attributes only after stats are available:
  - `headroom.active`
  - `headroom.tokens_saved`
  - `headroom.requests_compressed`
- [ ] Add `headroom_stats` to `ai8.codegen.run.completed` events if the event
  payload path exists in current code.
- [ ] Missing stats must not block event publication.

## Phase 4 - Docs and Operator Experience

- [ ] Create `docs/headroom-integration.md` with:
  - install options
  - settings block
  - env vars
  - CLI flags
  - fail-open and fail-closed behavior
  - port collision policy
  - how to verify traffic is routed
  - how to read stats
  - telemetry behavior and how to opt in/out
  - security note: loopback bind only by default
  - Codex-specific routing caveat
  - status of Gemini/OpenCode/KiloCode support if gated
- [ ] Update README with a short "Token compression with Headroom" section.
- [ ] Update `settings.json.example`.
- [ ] Add Makefile targets:
  - `make headroom-install`
  - `make test-headroom`
- [ ] Update AGENTS.md only if maintainers want future agents to know about
  `--headroom` / `--no-headroom`.

## Phase 5 - Verification and Release Discipline

Before source changes:

- [ ] Re-read `/Users/cliff/Desktop/_code/codegen_suite/rcodegen/AGENTS.md`.
- [ ] Run `go mod vendor` if local replace directives are active.

After source changes:

- [ ] Run targeted tests:
  - `go test ./pkg/headroom/... -race -v`
  - `go test ./pkg/tools/...`
  - `go test ./pkg/runner/...`
  - server/orchestrator/batch targeted packages touched by the implementation
- [ ] Run repo tests:
  - `make test`
- [ ] Compile binaries with repo Makefile:
  - `make`
- [ ] Run `go vet ./...` if compatible with current vendor/toolchain.
- [ ] Run smoke tests:
  - Disabled default: existing `rclaude`/`rcodex` behavior unchanged.
  - Claude active: proxy sees Anthropic request and stats delta increases.
  - Codex active: HTTP and WebSocket paths route through Headroom.
  - Fail-open: missing binary with enabled config runs directly and injects no
    proxy env/config.
  - Fail-closed: missing binary exits before spawning the tool.
  - Port collision compatible: existing Headroom proxy is adopted.
  - Port collision incompatible: fail-open runs direct, fail-closed errors.
  - User override: existing provider base URL is respected or explicitly
    reported as bypassing Headroom for that tool.
  - Server mode: gRPC and OpenAI HTTP runs include effective Headroom config.
  - Bundle/batch mode: one sidecar is reused.
- [ ] Run a reviewer-only pass over the final diff before merge.

Release discipline for this repo:

- [ ] Update `VERSION` and `CHANGELOG.md` according to repo instructions.
- [ ] Use `make`, not bare `go build`, for binary verification.
- [ ] Do not commit generated smoke logs under `_studies` unless explicitly
  requested by the maintainer.
- [ ] Commit/push according to the repo's AGENTS.md and Lore commit protocol.

## Acceptance Criteria

- Headroom disabled by default produces no env/config changes in spawned tools.
- `RCODEGEN_HEADROOM_ENABLED=true` or `--headroom` starts or adopts a local
  Headroom proxy and injects routing only after readiness is proven.
- Fail-open never injects dead proxy routing.
- `cmd.Env == nil` command paths keep the normal inherited environment.
- Codex support proves WebSocket routing, not just `OPENAI_BASE_URL`.
- Stats parsing reads the actual Headroom nested schema.
- CLI, batch, bundle, gRPC, and OpenAI HTTP surfaces all receive the same
  effective config semantics.
- The sidecar binds to loopback by default and does not enable Headroom
  telemetry unless configured.
- Tests and smoke checks cover disabled, active, fail-open, fail-closed,
  adoption, and port-collision cases.

## Risk Register

| Risk | Impact | Mitigation |
|---|---|---|
| Injecting a dead proxy URL after fail-open | Tool calls fail | Separate desired/effective config; inject only when `Active=true` |
| Losing inherited env when `cmd.Env` is nil | Missing PATH/API keys | Env helper starts from `os.Environ()` when needed |
| Codex WebSocket traffic bypasses proxy | False success claims | Dedicated Codex provider routing and WebSocket smoke test |
| Gemini/OpenCode/KiloCode endpoint contracts differ | Broken tools | Phase 0 verification and feature gates |
| Port 8787 collision | Startup failure or wrong service | Adopt compatible Headroom, otherwise fail-open/closed per config |
| Stats schema drift | Missing metrics | Tolerant nested parser, tests with real sample payload |
| Sidecar orphan process | Resource leak | Owned/adopted distinction, process group termination, shutdown tests |
| User provider override silently bypasses Headroom | Confusing savings | Respect override and log/report bypass reason |
| User config corruption for Codex fallback | High trust loss | Prefer process-local `-c`; if file edit is required, lock, backup, restore |
| Telemetry surprises users | Privacy concern | Managed sidecar defaults `HEADROOM_TELEMETRY=off`; docs explain override |

## Recommended Implementation Order

1. Phase 0 contract validation.
2. `pkg/headroom` config, probes, manager, env, stats.
3. Runner config and single-tool CLI lifecycle.
4. Claude end-to-end proof.
5. Codex end-to-end proof.
6. Bundle, batch, server propagation.
7. Remaining tools only after their routing contracts are proven.
8. Telemetry/stats surfaces.
9. Docs, version, changelog, full verification, reviewer pass.

Do not wire all five tools mechanically before proving Claude and Codex. Claude
validates the simple env-var path. Codex validates the complex provider-config
path. The other tools should follow only after their current routing contracts
are confirmed.
