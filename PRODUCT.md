# rcodegen -- Product Overview

## What Is rcodegen?

rcodegen is a unified automation platform that runs AI-powered coding agents (Claude Code, OpenAI Codex, Google Gemini, opencode, kilocode) in fully unattended, hands-off workflows against software codebases. It transforms interactive, human-in-the-loop AI coding assistants into batch-capable, composable automation that can audit, test, fix, refactor, grade, build, generate images, and write content -- all without a human sitting at the keyboard.

It is a Go monorepo that ships **eight binaries** in three layers: single-tool wrappers (`rclaude`, `rcodex`, `rgemini`, `ropencode`, `rkilo`) that make each vendor CLI run unattended; a multi-tool orchestrator (`rcodegen`) that chains models into adversarial/collaborative "bundles"; and operational surfaces (`rbatch` for large-scale batch jobs, `rserve` for a network service). Within the broader suite it is the **AI-coding-execution layer** -- the component everything else calls when it needs an AI model to do work against a repository, and (via `rserve`) the OpenAI/gRPC-compatible gateway that fronts every supported model.

## Why Does It Exist?

Each major AI coding assistant requires a human operator to babysit prompts, approve permission dialogs, and manually review output one codebase at a time. That human-in-the-loop requirement is the bottleneck. rcodegen removes it by wrapping these tools in a unified framework that handles unattended execution, permission bypass, output capture, cost control, quality grading, idempotency, and multi-model orchestration.

The business case: one engineer (or a CI pipeline) can dispatch dozens or hundreds of AI coding tasks overnight across an entire portfolio of repositories and wake up to graded, actionable, reviewable reports -- instead of manually prompting one model on one repo at a time. It also hedges vendor risk: by abstracting Claude, Codex, Gemini, and any OpenAI-compatible provider behind one interface (and orchestrating them against each other), no single AI vendor is a hard dependency.

## Who Does It Serve?

1. **Individual developers** automating recurring code-quality checks across their projects -- overnight audits, security scans, and test proposals with zero manual effort.
2. **Engineering teams** wanting standardized, AI-generated code-quality scorecards across a repo portfolio, with historical grade tracking per codebase.
3. **Security teams** running automated, multi-model adversarial security reviews (`red-team`, `security-review` bundles).
4. **DevOps / CI pipelines and remote agents** that need programmable access to AI coding tools via gRPC or the OpenAI-compatible HTTP API exposed by `rserve`.
5. **Content creators** wanting multi-model article generation with style emulation and editorial QA, plus Gemini-based image generation.
6. **Other services in the suite** that consume `rserve` as the AI-execution backend rather than shelling out to vendor CLIs themselves.

---

## Business Capabilities

### 1. Unattended single-tool execution (`rclaude`, `rcodex`, `rgemini`, `ropencode`, `rkilo`)

Each wrapper converts a native interactive CLI into a one-shot, unattended execution engine. The critical enabler is **permission bypass** (`--dangerously-skip-permissions` for Claude/opencode/kilocode, `--dangerously-bypass-approvals-and-sandbox` for Codex, `--yolo` for Gemini), because no human is present to approve prompts. Each wrapper adds task shortcuts, automated report generation, grade extraction, run logging, cost/credit tracking, file locking, and multi-codebase fan-out on top of the underlying CLI.

**Business value:** Turns every supported AI assistant into a scriptable, headless worker, so a single command can do real work against real repositories with no operator attention.

### 2. Built-in task shortcuts and the `suite` meta-task

Six standard report tasks plus standalone shortcuts: `audit`, `test`, `fix`, `refactor`, `quick` (the five that compose `suite`), and `grade`, `generate`, `study` (standalone, not part of `suite`). Each shortcut is an engineered prompt that instructs the model to analyze the codebase, produce a structured report with patch-ready diffs, assign a 0-100 grade, save to a strict filename pattern, and **not** edit source code. Users can define custom task prompts in settings; built-in task names are reserved and cannot be overridden.

**Business value:** Standardized, repeatable analyses mean output is comparable across models, codebases, and time -- not freeform one-off prompts.

### 3. Multi-codebase and portfolio-scale fan-out

A single invocation can target many codebases via comma-separated paths (`-c`/`-d`), recursive git-repo discovery (`-r --levels N`, max depth 10), run-all-repos-in-a-directory (`-A`), or an explicit `--list`. Each codebase gets its own individually named report.

**Business value:** Portfolio-wide audits/tests in one command, instead of one repo at a time.

### 4. Gemini image generation (Nano Banana)

`rgemini` is not limited to code analysis: it drives Gemini's image models. Passing model `banana` (alias for `gemini-3.1-flash-image-preview`) selects image generation, and `-i`/`--image` supplies one or more input images (comma-separated) for editing/reference. `--flash` selects `gemini-3-flash-preview` for fast text work. Oversized inputs are auto-downscaled before sending to avoid API block errors.

**Business value:** The same unattended/batch/report/server machinery extends to image creation and editing, not just code.

### 5. Multi-tool orchestration via bundles (`rcodegen`)

The orchestrator runs **bundles** -- JSON workflow definitions that chain multiple AI tools in sequence or parallel with variable passing, conditional branching, voting, and merging. Nine bundles ship embedded in the binary (`pkg/bundle/builtin/`): `build-review-audit` (flagship full-lifecycle: Claude builds, Gemini reviews, Claude improves, Gemini tests, Claude/Opus audits with a structured rubric), `ensemble` (3 models propose in parallel, majority vote), `compete` (2 models implement, cross-grade), `tdd`, `red-team`, `security-review`, `article`, `article-parallel`, and `summary`. Each run emits a live animated TUI plus a `final-report.json` with per-model cost/token breakdowns, extracted grades, file stats, and a copy of the bundle for reproducibility.

**Business value:** Adversarial and collaborative multi-model workflows produce higher-quality, cross-checked output than any single model -- something no individual vendor CLI can do.

### 6. Batch execution and scheduling (`rbatch`)

A batch runner for large-scale, long-running AI task execution. Subcommands: `run` (execute a JSON manifest), `spool` (process a directory of manifests as a queue, moving each through pending/running/done/failed), `watch`, `resume` (continue a stopped/failed batch from a `state.json` checkpoint, carrying forward accumulated cost), and `status`. It supports configurable concurrency, **session chaining** (jobs sharing a session ID run sequentially with the session carried forward for multi-turn context), **budget-aware execution** (stop / wait / ask policies when spend thresholds are hit), checkpoint/resume, and both **local** (spawn processes directly) and **remote** (`rserve` gRPC) executors.

**Business value:** Makes hundreds of AI tasks survivable and cost-bounded -- resumable after failure, throttled by budget, distributable across machines.

### 7. Network service and API gateway (`rserve`)

`rserve` exposes every tool and bundle over two network APIs simultaneously: a **streaming gRPC API** (default port `14260`; RPCs `RunTask`, `RunBundle`, `ListTasks`, `GetStatus`, `CancelRun`; reflection enabled) and an **OpenAI-compatible HTTP API** (default port `14261` = gRPC+1; `/v1/chat/completions` streaming + non-streaming, `/v1/models`, `/v1/files` upload/download, `/health`). Model names use `{tool}` or `{tool}:{model}` form (e.g. `claude:opus`). It supports multi-turn sessions (client `session_id` mapped to each CLI's native resume mechanism, 30-minute inactivity TTL) and file uploads (50MB cap, stored in `/tmp/rserve-files/`, purged after 24h). A run registry caps concurrency (default 3) and provides cancellable run IDs.

`rserve` is a first-class **chassis-go v11 service**: it wires coordinated lifecycle/shutdown (SIGTERM/SIGINT), gRPC health checks, a port registry, optional OpenTelemetry OTLP tracing (enabled when `OTEL_EXPORTER_OTLP_ENDPOINT` is set), and an optional Kafka event publisher (enabled when `KAFKAKIT_BOOTSTRAP_SERVERS` is set; tenant defaults to `ai8`).

**Business value:** Any OpenAI-SDK client, dashboard, or remote agent can use rcodegen as a drop-in backend for whichever AI engine is best for a task -- making it the suite's universal AI-coding gateway.

### 8. Standardized quality measurement and grading

Every report task extracts a 0-100 grade from the model's output (e.g. `TOTAL_SCORE: N/100`) and appends it to a per-codebase `_rcodegen/.grades.json` with date/tool/task metadata, guarded by both an in-process mutex and a cross-process `syscall.Flock`. The `build-review-audit` rubric is weighted: Functionality 20, Code Quality 20, User Experience 20, Security 10, Architecture 10, Testing 10, Innovation 5, Documentation 5.

**Business value:** A longitudinal, machine-generated code-health scorecard trackable across releases and teams.

### 9. Cost visibility and budget control

Per-run and per-step cost/token tracking, Claude budget caps (`--max-budget-usd`), credit-status monitoring via iTerm2 Python API integration, and batch-level budget policies (stop/wait/ask).

**Business value:** Makes AI-powered analysis financially predictable instead of open-ended.

### 10. Reporting dashboard (`dashboard/`)

A Next.js (React 19) web dashboard that browses `_rcodegen` reports and `.grades.json` across repos, renders report markdown, and manages **schedules** (cron expressions persisted to `~/.rcodegen/schedules.json`) for recurring runs.

**Business value:** A human-facing view over the report corpus and a place to schedule recurring portfolio analyses.

---

## Business Logic and Rules / Key Design Decisions

- **VERSION-based idempotency.** If a target codebase has a `VERSION` file, each tool+task combination records the last-run VERSION to `_rcodegen/version_state.json`. On a later run, an unchanged VERSION causes the task to skip; `-f`/`--force` overrides. Suite mode records each sub-task individually so partial re-runs work. Codebases without a `VERSION` file always run. **Why this matters:** prevents paying for redundant AI calls against unchanged code -- central to running cheaply at portfolio scale.

- **Report lifecycle and review gating.** Reports follow `{codebase}-{tool}-{task}-YYYY-MM-DD_HHMM.md`, carry a `Date Created:` header, and gain a `Date Modified:` header once a human reviews them. `-R`/`--require-review` skips tasks whose prior report is unreviewed; `-D`/`--delete-old` keeps only the newest report per task type. **Why this matters:** enforces a human review loop and keeps the report directory from accumulating stale noise.

- **Layered configuration priority.** Hardcoded defaults < `~/.rcodegen/settings.json` < `RCODEGEN_*` environment variables < CLI flags. `RCODEGEN_EFFORT` applies to Claude and Codex; Codex effort support is model-specific (`max`/`ultra` on Sol and Terra, `max` on Luna, through `xhigh` on older models). **Why this matters:** supports both personal config and CI parameterization without code changes.

- **Default models (source of truth = each tool's `ValidModels`/`DefaultModel`).** Claude: `fable`/`sonnet`/`opus`/`haiku`, code default `opus`; Codex: `gpt-5.6-sol` (default), `gpt-5.6-terra`, `gpt-5.6-luna`, `gpt-5.5`, `gpt-5.4`, `gpt-5.3-codex`, `gpt-5.2-codex`, `gpt-4.1-codex`, `gpt-4o-codex`; Gemini: default `gemini-3.1-pro-preview` (also `gemini-3-flash-preview`, `gemini-3.1-flash-image-preview`/`banana`, `gemini-2.5-*`); opencode/kilocode: dynamic `provider/model` namespace (no fixed list), default `deepinfra/Qwen/Qwen3-Coder-480B-A35B-Instruct`. **Why this matters:** model lists are validated per tool; the setup-wizard prompts and `settings.json` may show different (sometimes shorthand, e.g. `gemini-3`) values than a tool's compiled default, so the code is authoritative.

- **Reasoning effort controls.** `rclaude` and `rcodex` expose `-e/--effort`; both default to `xhigh`. Claude supports low/medium/high/xhigh/max. Codex validates effort against the selected model: Sol/Terra through ultra, Luna through max, older models through xhigh. **Why this matters:** lets operators trade latency/cost against depth per run or via defaults without sending unsupported combinations.

- **Cross-process safety everywhere shared state is touched.** Grades file uses mutex + flock; the `-l` lock uses `syscall.Flock` advisory locks in `~/.rcodegen/locks/` (deliberately **not** `/tmp/`, to avoid symlink attacks; 5-minute timeout, 5-second polling). Settings files are written 0600, lock dirs 0700, world-writable settings trigger a warning. **Why this matters:** multiple agents/instances run concurrently on one machine; unguarded writes would corrupt grades and state, and bypassed-permission tooling is security-sensitive.

- **Permission bypass is intentional and load-bearing.** It is what makes unattended operation possible, and it is why the docs are explicit that these tools should only run on trusted codebases in controlled environments. **Why this matters:** this is the single most security-relevant decision in the product; changes here have real blast radius.

- **Graceful cancellation.** Signal-aware contexts propagate Ctrl+C through every execution layer (multi-codebase, suite, orchestrated bundles, server runs) with partial-result reporting. **Why this matters:** long unattended runs must stop cleanly without losing completed work.

- **Concise error on unknown flags.** Unknown flags print a two-line error + hint rather than Go's full usage dump (e.g. `rcodex -b` -- borrowed from rclaude muscle memory); `-h`/`--help` still prints full usage. **Why this matters:** a UX fix so a typo doesn't look like a crash.

- **`@file` reference expansion.** `@path/to/file` tokens in a prompt are replaced with the file's contents before reaching the AI (all tools and bundle inputs). **Why this matters:** lets prompts pull in real file content without manual copy/paste.

---

## How to Think About Code Changes

- **This repo owns AI-coding execution and orchestration only.** Wrappers normalize vendor CLIs into the shared `runner.Tool` interface; the orchestrator/executor/bundle packages compose them; `rbatch`/`rserve` are operational surfaces. Anything model-vendor-specific (command shape, permission flag, output parsing, session resume) belongs inside that tool's `pkg/tools/<tool>` package, never leaking into shared code.

- **Adding a new tool = implement `runner.Tool` + a tiny `cmd/<r-name>/main.go` + a Makefile/build-matrix entry**, then register it across `rserve`, `rbatch`, bundle orchestration, grade extraction, settings defaults, and OpenAI model parsing. Adding a new vendor that is OpenAI-compatible usually does **not** warrant a new wrapper -- prefer `ropencode`/`rkilo` with a `provider/model` string.

- **Version is embedded, not read at runtime from a path.** `appversion.go` at the module root (package `rcodegen`) uses `//go:embed VERSION` into `AppVersion`; the Makefile bakes it via ldflags. Always build with `make` (never bare `go build`, or `-v` reports `unknown`). Callers use `rcodegen.AppVersion` directly.

- **chassis-go v11 is a hard dependency of `rserve`** (`chassis.RequireMajor(11)`, lifecycle/health/otel/kafkakit/registry). It is wired via a local `replace` directive to `../../chassis_suite/chassis-go`, so changes to chassis affect the build. The README's "Adding a New Tool" snippet still shows a v10 import; treat the actual `go.mod`/`cmd` imports (v11) as authoritative.

- **Don't break the report/grade contract.** Filename pattern, `.grades.json` shape and locking, `version_state.json`, and the `Date Created`/`Date Modified` review fields are consumed by the dashboard and by `-R`/`-D` logic. Changing any of them is a cross-component change.

- **Bundles are data, not code.** Built-in bundles live as embedded JSON; prefer adding/adjusting JSON over hardcoding workflow logic. Note the `article`/`article-parallel` bundles are currently hardcoded to emulate a specific author's writing style and require editing for other authors.

---

## Deployment Model / Scale

- **CLIs (`rclaude`/`rcodex`/`rgemini`/`ropencode`/`rkilo`/`rcodegen`/`rbatch`)** run locally or in CI as one-shot processes; concurrency is operator-managed via `-l` locking and batch concurrency limits. State and config live under `~/.rcodegen/` (settings, locks, bundles, schedules) and per-repo `_rcodegen/` (reports, grades, version_state).
- **`rserve`** is a long-running localhost service by default (`-bind 127.0.0.1`), with `-port` and `-max-concurrent` tunable. The native gRPC listener is plaintext and unauthenticated, and native HTTP has no TLS; remote deployments must keep rserve loopback-only behind authenticated TLS transport rather than directly using `-bind 0.0.0.0`. Non-loopback binds are refused unless `RSERVE_ALLOW_INSECURE_REMOTE=1` explicitly acknowledges the risk. As a chassis service it participates in coordinated shutdown, exposes health, and -- when the relevant env vars are set -- emits OTLP traces and Kafka events. Built for single-host or small-fleet operation, not high-fanout multitenancy.
- **Build matrix:** the Makefile cross-compiles all eight binaries for Linux amd64 and Darwin arm64 (`CGO_ENABLED=0`) plus launcher scripts.

## Current State / Status

- **Version: 4.2.10** (embedded from the `VERSION` file; see `CHANGELOG.md`).
- **Built and working:** all eight binaries; the five single-tool wrappers with task shortcuts, grading, run logs, `@file` expansion, effort controls, and iTerm2 credit tracking; Gemini image generation (Nano Banana, multi-image input, auto-downscale); the nine built-in bundles with the live TUI and `final-report.json`; `rbatch` run/spool/watch/resume/status with session chaining, budget policies, checkpoint/resume, and local+remote executors; `rserve` gRPC + OpenAI HTTP APIs with sessions, file uploads, concurrency registry, health, OTLP, and chassis lifecycle; the Next.js dashboard with report browsing and cron schedules.
- **Scaffolded / planned (built vs. not-yet-wired):** the Kafka `ai8.codegen.run.completed` run-completed event publisher is implemented in `cmd/rserve/main.go` (`publishRunCompleted`) and the publisher is initialized when configured, but the function is **not yet invoked from the run path** -- event emission per run is wiring still to be connected. opencode/kilocode wrappers are v1: they do **not** parse JSON events for token/cost/session extraction (placeholder zero usage, manual session IDs), matching each other's behavior.
