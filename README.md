# rcodegen

Unified automation framework for AI-powered code analysis, generation, reporting, batch execution, and remote serving. Run Claude, Codex, Gemini, opencode, and kilocode in unattended workflows against one or many codebases.

## What It Does

rcodegen provides four connected surfaces:

**Single-tool wrappers** (`rclaude`, `rcodex`, `rgemini`, `ropencode`, `rkilo`) add task shortcuts, automated reporting, cost tracking, file locking, grading, and multi-codebase support on top of each tool's native CLI.

**Multi-tool orchestrator** (`rcodegen`) chains multiple AI tools together in defined workflows called "bundles" -- one model builds code, another reviews it, another tests it, with parallel execution, voting, and merging.

**Operational tools** (`rbatch`, `rserve`) execute large job manifests locally or remotely and expose the tools through streaming gRPC and OpenAI-compatible HTTP APIs.

**Dashboard and scheduler** provide local report browsing, grade history, and cron-driven task execution.

## Binaries

| Binary | Purpose |
|--------|---------|
| `rclaude` | Claude Code CLI automation wrapper |
| `rcodex` | OpenAI Codex CLI automation wrapper |
| `rgemini` | Google Gemini CLI automation wrapper |
| `ropencode` | opencode CLI wrapper for OpenAI-compatible providers, defaulting to DeepInfra Qwen3-Coder |
| `rkilo` | kilocode CLI wrapper for OpenAI-compatible providers, defaulting to DeepInfra Qwen3-Coder |
| `rcodegen` | Multi-tool orchestrator for bundles |
| `rbatch` | Batch runner with concurrency control, session-group ordering/reuse, budget policies, spool processing, and checkpoint/resume |
| `rserve` | gRPC + OpenAI-compatible HTTP server exposing all tools and bundles (default gRPC port 14260, HTTP port 14261) |

## Prerequisites

- Go 1.26.5+
- One or more AI CLIs installed: `claude`, `codex`, `gemini`, `opencode`, `kilocode`
- Python 3.11+ (optional, for credit tracking via iTerm2)
- Node.js 20+ and npm (optional, for the dashboard and scheduler)
- `GEMINI_API_KEY` (only for Gemini image generation with `banana` / `gemini-3.1-flash-image-preview`)

## Installation

```bash
# Build Linux amd64 + Darwin arm64 variants and launcher scripts for all 8 binaries
make

# Build individually
make rclaude
make rcodex
make rgemini
make ropencode
make rkilo
make rcodegen
make rbatch
make rserve

# Run tests
make test

# Clean
make clean
```

The root `VERSION` file is embedded at compile time so `-v` works from any directory; the Makefile also applies release linker flags and creates platform launchers. Use `make` rather than bare `go build` for distributable binaries.

Add the launchers to your `PATH`, for example:

```bash
export PATH="$PWD/bin:$PATH"
```

## First Run

If `~/.rcodegen/settings.json` does not exist, the single-tool wrappers create a secure (`0600`) file with sensible defaults and auto-detect `~/Desktop/_code` or `~/_code`. Edit that file, or install the example explicitly:

```bash
mkdir -p ~/.rcodegen
cp settings.json.example ~/.rcodegen/settings.json
chmod 600 ~/.rcodegen/settings.json
```

## Quick Start

### Single-Tool Usage

```bash
# Run a security audit on a project
rclaude -c myproject audit

# Run all 5 report types (audit, test, fix, refactor, quick)
rclaude -c myproject suite

# Use a specific model and budget
rclaude -c myproject -m opus -b 20.00 test

# Run across multiple codebases
rclaude -c proj1,proj2,proj3 audit

# Recursively find and audit all git repos in a directory
rclaude -r -d ~/code audit

# Run all git repos in a directory
rclaude -A ~/code audit

# Process specific subdirectories
rclaude -d ~/code --list proj1,proj2 audit

# Dry run (show command without executing)
rclaude -n -c myproject audit

# Generate or edit an image through Gemini's direct API
GEMINI_API_KEY=... rgemini -d /path/to/project -m banana -i reference.png \
  "Create a polished variation"
```

### Multi-Tool Orchestration

```bash
# Run a build-review-audit workflow
rcodegen build-review-audit -c myproject "Add user authentication"

# Force all Claude steps to use Opus
rcodegen build-review-audit -c myproject "task" --opus-only

# List available bundles
rcodegen list

# Preview and run a batch manifest
rbatch run batch.json --dry-run
rbatch run batch.json --concurrency 4

# Start gRPC on 14260 and HTTP on 14261
rserve
```

`rcodegen run <bundle>` and `rcodegen bundle <bundle>` are equivalent to the direct `rcodegen <bundle>` form.

## Task Shortcuts

All single-tool wrappers support these built-in task shortcuts:

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

Custom tasks can be added in `~/.rcodegen/settings.json`. Built-in task names are reserved and cannot be overridden.

## Common Flags

### Directory Options

```
-c, --code <path>       Project path relative to configured code_dir (comma-separated)
-d, --dir <path>        Absolute working directory (comma-separated)
--list <names>           Comma-separated subdirectory names to process
-A, --dir-all <path>    Run all git repos in directory (comma-separated)
-r, --recursive         Scan for git repos and run in each
--levels <N>            Depth of recursive scan (default: 1, max: 10)
-o, --output <path>     Custom output directory (replaces _rcodegen)
```

### Execution Options

```
-m, --model <name>      Specify model
-n, --dry-run           Show command without executing
-l, --lock              Queue behind other running instances
-j, --json              Output as newline-delimited JSON
-J, --stats-json        Output run statistics as JSON at completion
-x <key=value>          Set variable for task template (repeatable)
```

### Report Options

```
-D, --delete-old        Delete previous reports with same pattern after run
-R, --require-review    Skip if previous report unreviewed
--no-runlog             Suppress .runlog.md generation
```

### Other

```
-v, --version           Show version
-V, --verbose           Enable debug logging
-t, --tasks             List available task shortcuts
-f, --force             Bypass VERSION state check and force run
--status-only           Show status and exit
--migrate-grades        Backfill .grades.json in the selected/current repo
--migrate-grades-all    Backfill grades for all repos under code_dir
-h, --help              Show help
```

### Tool-Specific Flags

**rclaude:**
```
-b, --budget <usd>      Max budget in USD per run (default: 10.00, max: 1000.00)
-e, --effort <level>    Effort level: low, medium, high, xhigh, max (default: xhigh)
-s, --status            Track credit usage before/after task
-S, --no-status         Disable credit usage tracking
```

**rcodex:**
```
-e, --effort <lvl>      Reasoning effort: low through ultra, model-dependent (default: xhigh)
-s, --status            Track credit usage before/after task
-S, --no-status         Disable credit usage tracking
```

**rgemini:**
```
--flash                 Use gemini-3-flash-preview model
-i, --image <files>     Input image(s), comma-separated
-s, --status            Track usage before/after task
-S, --no-status         Disable usage tracking
```

Use `-m banana` (alias for `gemini-3.1-flash-image-preview`) for image generation. That mode calls the Gemini REST API directly, requires `GEMINI_API_KEY`, accepts PNG/JPEG/GIF/WebP inputs, downscales edges above 1568px, and saves returned images in the working directory.

**rcodegen:**
```
--opus-only             Force all Claude steps to use Opus model
--flash                 Force all Gemini steps to use flash model
--static                Use static display instead of animated TUI
--live=false            Disable the animated display (equivalent intent to --static)
-j                      Emit the final envelope as JSON
```

## Bundles (Multi-Tool Workflows)

Bundles are JSON workflow definitions that orchestrate multiple AI tools in sequence or parallel. There are 9 built-in bundles:

| Bundle | Description |
|--------|-------------|
| `build-review-audit` | Claude builds, Gemini reviews, Claude improves, Gemini tests, Claude audits and grades |
| `ensemble` | 3 models propose in parallel, then majority vote |
| `compete` | 2 models implement in parallel, then cross-grade each other |
| `tdd` | Claude writes tests, Gemini implements, Claude reviews |
| `red-team` | Claude implements, Gemini attacks, Claude hardens |
| `security-review` | Claude + Gemini audit in parallel, Claude synthesizes |
| `article` | Gemini researches style, Codex drafts, Gemini edits |
| `article-parallel` | Research, 2 parallel drafts, 2 parallel edits |
| `summary` | Claude summarizes, Gemini verifies accuracy |

Custom bundles can be placed in `~/.rcodegen/bundles/`.

Minimal custom bundle (`~/.rcodegen/bundles/draft-review.json`):

```json
{
  "name": "draft-review",
  "description": "Draft with Claude, review with Gemini",
  "inputs": [{"name": "task", "required": true}],
  "steps": [
    {
      "name": "draft",
      "tool": "claude",
      "task": "${inputs.task}"
    },
    {
      "name": "review",
      "tool": "gemini",
      "task": "Review this draft:\n${steps.draft.stdout}"
    }
  ]
}
```

Bundle names are limited to letters, digits, hyphens, and underscores (100 characters maximum). A user bundle with the same filename as a built-in bundle takes precedence.

### Bundle Features

- **Parallel execution** -- Run substeps concurrently with goroutines
- **Voting** -- Majority or unanimous voting across model outputs
- **Merging** -- Concatenate or deduplicate outputs from multiple steps
- **Conditional steps** -- `if`/`then`/`else` branching based on step results
- **Variable resolution** -- `${inputs.name}`, `${steps.stepname.stdout}`, `${steps.stepname.status}`
- **Session reuse** -- Pass native session IDs between compatible steps (Claude, Gemini, and Codex; automatic extraction is not yet implemented for OpenCode/KiloCode)
- **Cost tracking** -- Per-step and aggregate cost/token tracking where the underlying CLI reports usage

Step types are selected by their fields: `tool` executes one CLI, `parallel` runs nested steps concurrently, `merge` combines named outputs (`concat`, `union`, or `dedupe`), `vote` selects among named outputs (`majority`, `unanimous`, or `ranked`), and `if` with `then`/`else` selects a branch. See `pkg/bundle/builtin/` for complete examples.

## Reports

### Output Location

Reports are saved as markdown files in `_rcodegen/` within each project directory by default. `-o`/`--output` or `RCODEGEN_OUTPUT_DIR` replaces that report directory.

### Filename Format

```
{codebase}-{tool}-{task}-YYYY-MM-DD_HHMM.md
```

Example: `myproject-claude-audit-2026-01-20_1430.md`

### Grading System

Each report type extracts a grade (0-100) from the AI's output using patterns like `TOTAL_SCORE: N/100`. Grades are persisted to `_rcodegen/.grades.json` with cross-process file locking for safe concurrent access.

Unless `--no-runlog` is set, each wrapper also maintains `_rcodegen/.runlog.md` with run metadata. Use `--migrate-grades` or `--migrate-grades-all` to backfill `.grades.json` from older report files.

The `build-review-audit` bundle uses a structured rubric:
- Functionality (20), Code Quality (20), User Experience (20)
- Security (10), Architecture (10), Testing (10)
- Innovation (5), Documentation (5)

### Review Workflow

1. Run creates report with `Date Created:` in header
2. Review the report and add `Date Modified:` in the header
3. Subsequent runs with `-R` skip tasks where previous reports are unreviewed

### Cleanup

The `-D` flag keeps only the newest report for each task type, deleting older versions.

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
    "claude": { "model": "sonnet", "budget": "10.00", "effort": "xhigh" },
    "gemini": { "model": "gemini-3.1-pro-preview" },
    "opencode": {
      "model": "deepinfra/Qwen/Qwen3-Coder-480B-A35B-Instruct",
      "provider": "deepinfra"
    },
    "kilocode": {
      "model": "deepinfra/Qwen/Qwen3-Coder-480B-A35B-Instruct",
      "provider": "deepinfra"
    }
  },
  "tasks": {
    "my-custom-task": {
      "prompt": "Analyze this code for X. Save report as {report_file} in {report_dir}."
    }
  }
}
```

### Configuration Priority

1. Hardcoded defaults
2. `~/.rcodegen/settings.json`
3. Environment variables (`RCODEGEN_CODE_DIR`, `RCODEGEN_OUTPUT_DIR`, `RCODEGEN_MODEL`, `RCODEGEN_BUDGET`, `RCODEGEN_EFFORT`, `RCODEGEN_LOG_LEVEL`; `RCODEGEN_EFFORT` applies to Claude and Codex, with Codex levels validated against the selected model)
4. CLI flags (highest priority)

### Task Template Variables

| Variable | Expands To |
|----------|------------|
| `{report_file}` | Auto-generated filename |
| `{report_dir}` | Configured report directory (default: `_rcodegen`) |
| `{codebase}` | Codebase name from `-c` |
| `{timestamp}` | Current timestamp |
| `{variable}` | Custom value from `-x variable=value` |

In task text, `@path/to/file` is expanded to that file's contents before execution. Quote prompts containing spaces so they remain one task argument.

## Supported Models

### Claude
`fable`, `sonnet`, `opus`, `haiku` (settings default: `sonnet`). Effort levels: `low`, `medium`, `high`, `xhigh`, `max` (default: `xhigh`).

### Codex
`gpt-5.6-sol`, `gpt-5.6-terra`, `gpt-5.6-luna`, `gpt-5.5`, `gpt-5.4`, `gpt-5.3-codex`, `gpt-5.2-codex`, `gpt-4.1-codex`, `gpt-4o-codex` (default: `gpt-5.6-sol`). The configured default effort is `xhigh`. Sol and Terra support `low`, `medium`, `high`, `xhigh`, `max`, and `ultra`; Luna supports through `max`; older models support through `xhigh`.

### Gemini
`gemini-3.1-pro-preview`, `gemini-3.1-flash-image-preview`, `gemini-3-flash-preview`, `gemini-2.5-pro`, `gemini-2.5-flash`, `gemini-2.5-flash-lite`, plus the `banana` alias for the image-preview model (default: `gemini-3.1-pro-preview`)

### OpenCode / KiloCode
Any opencode or kilocode `provider/model` string, for example `deepinfra/Qwen/Qwen3-Coder-480B-A35B-Instruct` (default). Run `opencode providers login` or `kilocode auth login` once per provider before first use.

## Locking & Concurrency

The `-l` flag queues runs behind other active instances using `syscall.Flock`-based advisory file locking at `~/.rcodegen/locks/`:

```bash
# Terminal 1
rcodex -l -c project1 suite

# Terminal 2 (waits for Terminal 1 to finish)
rclaude -l -c project2 suite
```

Lock info files show which codebase holds the lock. Polling every 5 seconds with a 5-minute timeout.

## Live Display

The `rcodegen` orchestrator features an animated TUI with:

- Braille dot spinner animation at 100ms intervals
- Real-time elapsed time and cost counters
- Per-step status with tool/model indicators
- Color-coded by tool: magenta (Claude), yellow (Gemini), blue (Codex), white (opencode), bright magenta (kilocode)
- Live activity feed parsed from stream-json output (e.g., "Reading files...", "Writing code...")

Use `--static` to disable animation.

## Tool Comparison

| Feature | rclaude | rcodex | rgemini | ropencode | rkilo |
|---------|---------|--------|---------|-----------|-------|
| CLI Command | `claude -p` | `codex exec` | `gemini -p` | `opencode run` | `kilocode run` |
| Permission Bypass | `--dangerously-skip-permissions` | `--dangerously-bypass-approvals-and-sandbox` | `--yolo` | `--dangerously-skip-permissions` | `--dangerously-skip-permissions` |
| Output Format | stream-json | --json | stream-json | json | json |
| Cost Tracking | iTerm2 API | iTerm2 API | iTerm2 API | None in v1 | None in v1 |
| Budget Control | `--max-budget-usd` | None | None | Provider-side | Provider-side |
| Default Model | sonnet | gpt-5.6-sol | gemini-3.1-pro-preview | deepinfra/Qwen/Qwen3-Coder-480B-A35B-Instruct | deepinfra/Qwen/Qwen3-Coder-480B-A35B-Instruct |

## Project Structure

```
rcodegen/
├── cmd/
│   ├── rclaude/main.go              # Claude CLI entry point (~15 lines)
│   ├── rcodex/main.go               # Codex CLI entry point (~15 lines)
│   ├── rgemini/main.go              # Gemini CLI entry point (~15 lines)
│   ├── ropencode/main.go            # opencode CLI entry point (~15 lines)
│   ├── rkilo/main.go                # kilocode CLI entry point (~15 lines)
│   ├── rcodegen/main.go             # Orchestrator entry point
│   ├── rbatch/main.go               # Batch job runner entry point
│   └── rserve/main.go               # gRPC + HTTP server entry point
├── pkg/
│   ├── runner/                      # Core execution framework
│   │   ├── tool.go                  # Tool interface
│   │   ├── runner.go                # Main run loop
│   │   ├── config.go                # Config struct & colors
│   │   ├── flags.go                 # Flag parsing & reordering
│   │   ├── grades.go                # Grade extraction & persistence
│   │   ├── tasks.go                 # Task type constants
│   │   ├── output.go                # Banners, summaries, stats
│   │   ├── stream.go                # Stream-JSON parser
│   │   ├── validate.go              # Model validation
│   │   ├── versionstate.go          # VERSION-based skip state (per tool+task)
│   │   └── migrate.go               # Grade migration utilities
│   ├── tools/
│   │   ├── claude/claude.go         # Claude tool implementation
│   │   ├── codex/codex.go           # Codex tool implementation
│   │   ├── gemini/gemini.go         # Gemini tool implementation
│   │   ├── opencode/opencode.go     # opencode tool implementation
│   │   └── kilocode/kilocode.go     # kilocode tool implementation
│   ├── orchestrator/                # Multi-step workflow engine
│   │   ├── orchestrator.go          # Main orchestration loop
│   │   ├── context.go               # Variable resolution
│   │   ├── condition.go             # Conditional expressions
│   │   ├── live_display.go          # Animated TUI display
│   │   └── progress.go              # Static progress display
│   ├── executor/                    # Step execution dispatch
│   │   ├── dispatcher.go            # Routes steps to executors
│   │   ├── tool.go                  # Runs individual tool commands
│   │   ├── parallel.go              # Concurrent step execution
│   │   ├── merge.go                 # Merge outputs
│   │   └── vote.go                  # Voting/ensemble decisions
│   ├── bundle/                      # Workflow definitions
│   │   ├── bundle.go                # Bundle/Step structs
│   │   ├── loader.go                # Load from disk or builtin
│   │   └── builtin/                 # 9 embedded JSON bundles
│   ├── server/                      # gRPC + HTTP server implementation
│   │   ├── server.go                # RServe gRPC service handler
│   │   ├── registry.go              # Run concurrency registry
│   │   ├── session.go               # Multi-turn session store (TTL-based)
│   │   ├── openai/                  # OpenAI-compatible HTTP API
│   │   │   ├── handler.go           # /v1/chat/completions, /v1/models, /health
│   │   │   ├── bundles.go           # Bundle list/detail/run endpoints and artifacts
│   │   │   ├── types.go             # Request/response types
│   │   │   ├── models.go            # Model name parsing
│   │   │   ├── sse.go               # Server-sent events writer
│   │   │   └── files.go             # /v1/files upload/download endpoints
│   │   └── pb/                      # Generated protobuf/gRPC stubs
│   ├── batch/                       # Batch job execution engine
│   │   ├── runner.go                # Batch runner with concurrency control
│   │   ├── queue.go                 # Job queue
│   │   ├── scheduler.go             # Job scheduling and prioritization
│   │   ├── executor.go              # Executor interface
│   │   ├── executor_local.go        # Local process execution
│   │   ├── executor_remote.go       # Remote rserve gRPC execution
│   │   ├── checkpoint.go            # Checkpoint/resume state
│   │   ├── spool.go                 # Spool directory processing
│   │   ├── budget.go                # Budget-aware execution
│   │   ├── reporter.go              # Batch run reporting
│   │   └── manifest.go              # Job manifest parsing
│   ├── envelope/                    # Standardized result envelope
│   ├── workspace/                   # Job workspace management
│   ├── settings/                    # JSON config & setup wizard
│   ├── lock/                        # File-based locking (syscall.Flock)
│   ├── reports/                     # Report management
│   ├── tracking/                    # Credit/cost tracking (iTerm2)
│   └── colors/                      # ANSI color constants
├── dashboard/                       # Web-based reporting dashboard
├── scheduler/                       # Cron daemon used by dashboard schedules
├── bin/                             # Compiled binaries
├── Makefile                         # Build system with ldflags
├── settings.json.example            # Example config
├── get_codex_status.py              # Codex credit tracking (iTerm2)
├── get_claude_status.py             # Claude credit tracking (iTerm2)
├── get_gemini_status.py             # Gemini credit tracking (iTerm2)
├── claude_question_handler.py       # Claude question detection/answering
└── codex_pty_wrapper.py             # Codex PTY wrapper for session resume
```

## rbatch (Batch Runner)

`rbatch` runs JSON job manifests with bounded concurrency. Jobs that share a non-empty `session` value form one sequential group; independent groups can run concurrently. Execution is local by default or delegated to `rserve` with `--server`.

### Manifest Format

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
      "effort": "xhigh",
      "max_budget": "5.00"
    },
    {
      "name": "review-api",
      "task": "review the prior findings",
      "tool": "claude",
      "dir": "/path/to/api",
      "session": "api-review"
    }
  ]
}
```

Manifest defaults: `concurrency=1`, `tool=claude`, generated job names, `on_budget=stop`, `check_interval=3m`, and `max_wait=1h`. Valid tools are `claude`, `codex`, `gemini`, `opencode`, and `kilocode`. `on_budget` accepts `stop`, `wait`, or `ask`; `ask` behaves as `stop` because batch mode is non-interactive.

### Commands

```bash
# Execute locally; flags may appear before or after the manifest path
rbatch run batch.json --concurrency 4 --dry-run
rbatch run batch.json --threshold 20 --on-budget wait --max-wait 2h

# Execute jobs through plaintext gRPC
rbatch run batch.json --server 127.0.0.1:14260

# Process pending/*.json in filename order and move each manifest to done/ or failed/
rbatch spool /path/to/spool --server 127.0.0.1:14260

# Resume an explicit checkpoint, or omit it to select the newest checkpoint
rbatch resume ~/.rcodegen/batches/nightly-audits/state.json --concurrency 2
rbatch resume

# List all stored batches or inspect one
rbatch status
rbatch status nightly-audits
```

`rbatch run` flags: `--concurrency`, `--threshold`, `--on-budget`, `--max-wait`, `--server`, `--dry-run`, and `-v`. `spool` accepts `--server` and `-v`; `resume` accepts `--server`, `--concurrency`, and `-v`. `watch` is reserved but not implemented.

Summaries and checkpoints are stored under `~/.rcodegen/batches/<name>/`. A failed job stops only its session group; other groups finish. `Ctrl+C` creates a resumable checkpoint. Native conversation reuse works when the selected tool reports a session ID (currently the stream-parsed Claude and Gemini paths); group ordering still applies to every tool. Budget percentages use locally available Claude credit tracking; remote execution cannot query that budget and therefore continues when budget data is unavailable.

## rserve (gRPC + HTTP Server)

`rserve` exposes all tools and bundle orchestration via both a streaming gRPC API and an OpenAI-compatible HTTP API, intended for use by dashboards, remote agents, or any OpenAI SDK client.

**Default ports:** gRPC `14260`, HTTP `14261` (port+1). Binds to localhost only by default.

```bash
# Start with defaults
rserve

# Custom port and concurrency, still loopback-only
rserve -port 9000 -max-concurrent 5 -bind 127.0.0.1

# Show version
rserve -v
```

**Flags:**

| Flag | Description | Default |
|------|-------------|---------|
| `-port` | gRPC listen port (HTTP is port+1) | `14260` |
| `-bind` | Listen address; non-loopback values require an explicit unsafe-remote override | `127.0.0.1` |
| `-max-concurrent` | Max simultaneous runs | `3` |
| `-session-ttl` | Session inactivity TTL in minutes (`0` disables expiry) | `30` |
| `-v` | Show version and exit | |

Optional server environment variables:

| Variable | Effect |
|----------|--------|
| `RSERVE_TOKEN` | Require a bearer token on HTTP endpoints except `/health` |
| `RSERVE_WORK_ROOT` | Absolute root that confines HTTP bundle `work_dir` values |
| `RSERVE_ALLOW_INSECURE_REMOTE=1` | Permit a non-loopback native bind after acknowledging plaintext unauthenticated gRPC; prefer a loopback TLS gateway |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | Enable OpenTelemetry export to an OTLP endpoint |
| `KAFKAKIT_BOOTSTRAP_SERVERS` | Enable the optional kafkakit/lifecycle integration for Kafka/Redpanda brokers |
| `KAFKAKIT_TENANT_ID` | Set the event tenant ID (default: `ai8`) |

### gRPC API

| RPC | Description |
|-----|-------------|
| `RunTask` | Run a single tool (claude/codex/gemini/opencode/kilocode), stream events |
| `RunBundle` | Run a named bundle, stream events |
| `ListTasks` | List task shortcuts and bundles |
| `GetStatus` | Server health, active run count, run details |
| `CancelRun` | Cancel a run by ID |

gRPC reflection is enabled — use `grpcurl` or any gRPC client to discover the schema:

```bash
grpcurl -plaintext 127.0.0.1:14260 list
grpcurl -plaintext -d '{"tool":"claude","task":"hello","work_dirs":["/tmp"]}' \
  127.0.0.1:14260 rserve.RServe/RunTask
```

The gRPC listener is plaintext and has no authentication layer. Keep it on loopback or place it behind authenticated TLS transport before exposing it to another host.

### OpenAI-Compatible HTTP API

The HTTP API on port+1 is compatible with the OpenAI chat-completions shape plus rcodegen-specific `work_dirs`, `clone_work_dirs`, `session_id`, and `callback_url` fields. Model names follow `{tool}` or `{tool}:{model}` (for example `claude`, `claude:opus`, or `gemini:gemini-3.1-pro-preview`), with an optional **`-{effort}` suffix** on either form: `claude:opus-max`, `codex:gpt-5.6-luna-high`, or bare `codex-ultra` (the configured default model at that effort). The suffix is only treated as an effort when that specific model supports it, so hyphenated names like `gpt-5.6-luna` are never mangled; chat requests reject unsupported combinations such as `gpt-5.6-luna-ultra`. Supported suffixes also work on `model` fields in bundle step definitions. `/v1/models` enumerates fixed `tool:model` combinations for tools found on the server's `PATH`, flags the configured default with `"default": true`, and lists model-specific suffixes in `"efforts"`. OpenCode and KiloCode advertise `"dynamic": true`, list their configured default, and continue accepting arbitrary `provider/model` identifiers. Unknown models in fixed namespaces receive a 400 listing valid options. Chat request bodies are limited to 10MB; bundle run request bodies are limited to 1MB.

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/v1/chat/completions` | POST | Chat completion (streaming and non-streaming) |
| `/v1/models` | GET | Available tools, fixed models, dynamic namespaces, configured defaults, and model-specific efforts |
| `/v1/bundles` | GET | List available bundles with their inputs |
| `/v1/bundles/{name}` | GET | Bundle detail: full step DAG (parallel groups, vote/merge, conditionals) |
| `/v1/bundles/{name}` | POST | Run a bundle, return per-step results + inline artifacts (SSE streaming optional) |
| `/v1/files` | POST | Upload a file (multipart, 50MB limit) |
| `/v1/files` | GET | List uploaded files |
| `/v1/files/{id}` | GET | Get uploaded-file metadata |
| `/v1/files/{id}` | DELETE | Delete a file |
| `/v1/runs/{run_id}` | GET | Async run status and timings |
| `/v1/runs/{run_id}/result` | GET | Retained completion of a finished async run |
| `/v1/runs` | GET | Async run summaries, filterable by `?correlation_id=` |
| `/v1/runs/{run_id}` | DELETE | Cancel a queued or running async run |
| `/health` | GET | Server health and active run count |

```bash
# Non-streaming request
curl http://127.0.0.1:14261/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"claude","messages":[{"role":"user","content":"hello"}]}'

# Streaming request
curl http://127.0.0.1:14261/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"claude:opus","messages":[{"role":"user","content":"hello"}],"stream":true}'

# With working directories
curl http://127.0.0.1:14261/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"claude","messages":[{"role":"user","content":"audit this code"}],"work_dirs":["/path/to/project"]}'

# Against a throwaway copy, leaving the source tree untouched
curl http://127.0.0.1:14261/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"claude","messages":[{"role":"user","content":"audit this code"}],"work_dirs":["/path/to/project"],"clone_work_dirs":true}'
```

### Ephemeral Work Directories

Chat requests accept `"clone_work_dirs": true`, which copies every `work_dirs` entry into a private scratch root under `$TMPDIR` (`rserve-clone-{run_id}-*`, mode 0700) and runs the tool against the copy. Agent state such as `.omc/` therefore lands in the throwaway tree instead of the shared source, which is what keeps concurrent workers pointed at the same repo from colliding. The scratch root is deleted when the run ends — success, failure, or client disconnect — and a cleanup failure is logged rather than failing the run. On macOS the copy is an APFS copy-on-write clone (`cp -Rc`): near-instant, no extra disk until something is written, dotfiles included; filesystems that reject it fall back to a real recursive copy, and the choice is logged per directory as `method=cow` or `method=copy`. The flag defaults to `false` (unchanged behaviour: the tool runs in the caller's directories) and is a no-op when `work_dirs` is absent. When cloning happens the response carries `"cloned_work_dirs": {n}` — on the completion object, or on the final chunk when streaming. Bundle `work_dir` semantics are unchanged.

Sources are validated before the request queues for a run slot, so an unusable directory comes back right away instead of waiting behind other work. A missing or non-directory source is rejected with `400 invalid_work_dir`, and two shapes are refused because copying cannot isolate them. A tree containing an absolute symlink, or a relative symlink resolving above its root, is rejected with `400 unsafe_symlink`: the copy preserves symlinks, so the link would still aim at the original tree and a write through it would escape the scratch root. A source containing a regular file named `.git` — at the root or at any depth — is rejected with `400 unsupported_git_worktree`: that file is a gitdir pointer (a linked worktree at the root, a submodule checkout below it), so the clone would keep using the original repository and work inside it would mutate the caller's. Point `work_dirs` at a main worktree with no submodule checkouts, or let git create the working copy instead of copying one. Symlinks that stay inside the source are fine and keep working inside the clone, a `.git` **directory** clones normally at any depth (a vendored repository is fine), and a symlinked source root is resolved before any of this is checked.

```json
{
  "model": "claude:opus",
  "messages": [{"role": "user", "content": "audit this code"}],
  "work_dirs": ["/path/to/project"],
  "clone_work_dirs": true
}
```

For streaming requests, `X-Show-Tool-Use: true` includes Claude/Gemini tool-use summaries as text chunks. Claude and Gemini expose structured streaming events; Codex, OpenCode, and KiloCode stdout is forwarded as raw content chunks.

Chat requests also accept `X-Correlation-ID` — an external run identifier such as a Windmill job UUID. It is sanitized to `[A-Za-z0-9._-]` and capped at 128 characters, echoed back as the `X-Correlation-ID` response header and as `"correlation_id"` in the body (on the completion object, or the final chunk when streaming), and attached to the run registry entry so `GetStatus` shows which external job owns each slot. This is the same handling bundle runs have always had; the header echo now happens for every endpoint, including error responses.

### Async callback mode

A synchronous chat completion holds one connection for the entire run, ties the client's read timeout to the caller's step timeout to the instance timeout, and dies with the connection — a client disconnect cancels the run. For runs measured in minutes, that coupling is the biggest reliability gap there is. Send `"callback_url"` and the run is detached from the request instead:

```bash
curl http://127.0.0.1:14261/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "X-Correlation-ID: windmill-job-42" \
  -d '{"model":"claude:opus","messages":[{"role":"user","content":"audit this code"}],
       "work_dirs":["/path/to/project"],"clone_work_dirs":true,
       "callback_url":"https://windmill.example.com/api/w/aows/jobs_u/resume/..."}'
# → 202 {"run_id":"a1b2c3d4e5f60718","status":"queued","correlation_id":"windmill-job-42"}
```

The request is validated in full first — model, effort, `work_dirs` policies, the callback URL itself — so a bad request still comes back as the same `400` on the same connection. Only then does rserve answer `202` and let go. The run then proceeds exactly as the synchronous path does (same queue accounting, same cloning, same completion shape), and when it ends rserve POSTs the completion — the synchronous body plus `"run_id"` and `"status": "success"|"failure"` — to the callback URL: 10s per attempt, 3 attempts, backoff 2s then 8s, then it gives up and logs. A failure payload embeds the usual error envelope, `retryable` included. Delivery happens after the run slot is freed, so a slow receiver never holds capacity.

`callback_url` must be `https`, or plain `http` only for a loopback or RFC1918 host; anything else is `400 invalid_callback_url`. It cannot be combined with `"stream": true` (`400 callback_stream_conflict`). Optional `callback_headers` ride the POST verbatim for receivers that need their own auth. Header values are never logged, and neither is the callback URL's path or query — a Windmill resume URL is a secret in path form, so logs name only its scheme and host.

Poll or manage a run through `/v1/runs`: `GET /v1/runs/{run_id}` for `queued|running|success|failure` plus timings, `GET /v1/runs/{run_id}/result` for the retained payload, `GET /v1/runs?correlation_id=` to find the runs one job owns, and `DELETE /v1/runs/{run_id}` to cancel — which kills the CLI subprocess, removes the scratch clone, frees the slot, and delivers a `run_cancelled` failure callback. `DELETE` is what replaces "client disconnect cancels the run" once there is no connection to drop.

**Retention is in-memory and non-durable — this is deliberate.** Results are bounded to 100 or 1 hour, whichever binds first, with least-recently-used eviction; queued and running runs are never evicted; message content is capped at 64KB and truncated with `"output_truncated": true` rather than dropped. **A restart loses every pending run and every retained result.** In-flight runs get one best-effort `server_shutdown` failure callback (retryable) if their receiver is up, and nothing at all if it is not. rserve holds no durable state by design — the fleet keeps that in Postgres — so the caller's own timeout is the real guard. For a Windmill flow, that is the step's suspend timeout: submit with the step's resume URL as `callback_url`, suspend, and rserve resumes the flow with the completion. No held connection, no timeout coupling, and a worker restart no longer kills the run. `API.md` has the full pairing example.

### Cost, usage, and queue visibility

Chat completions report where their numbers came from. `"usage_source": "cli"` means the tool's CLI reported usage: `usage` is populated, and `"cost_usd"` too when the CLI reports a cost (Claude does; Gemini reports tokens only, so `cost_usd` is omitted rather than sent as zero). `"usage_source": "unreported"` means the CLI publishes none — Codex's JSON carries `usage: null`, and OpenCode and KiloCode have no usage channel at all — and then `usage` and `cost_usd` are omitted entirely. rserve never invents these numbers: an omitted `cost_usd` means "not measured", not "free", so anything summing costs across runs must treat `unreported` as unknown. Each tool adapter implements `runner.UsageReporter`, so a CLI that starts reporting usage is a one-adapter change.

When every run slot is busy, a request waits — which from outside is indistinguishable from a slow run. Streaming requests that wait now get told, before any completion chunk and only when a wait actually happened:

```
data: {"type": "queued", "position": 1}

data: {"type": "started"}
```

Non-streaming requests get the total afterwards as the `X-Queue-Wait-Ms` response header, omitted when there was no wait. `/health` gains `"queued": N` alongside `active_runs`, counting waiters from every entry point, so a saturated server is visible as saturated rather than as slow.

### Error retryability

Every error response carries `"retryable"` alongside `message`, `type`, and `code`:

```json
{"error": {"message": "unknown tool: foo", "type": "invalid_request_error", "code": "unknown_tool", "retryable": false}}
```

It exists so an automatic retry policy — Windmill's per-step `retry`, for one — can tell "try again" from "doomed" without pattern-matching messages or guessing from the HTTP status, which cannot distinguish a transient 500 from a permanent one. The field is always present; `false` is a verdict, not a missing field.

`retryable: false` covers the malformed, the non-existent, and the refused-on-policy: `method_not_allowed`, `unauthorized`, `invalid_json`, `unknown_tool`, `empty_task`, `invalid_model`, `invalid_effort`, `invalid_work_dir`, `unsafe_symlink`, `unsupported_git_worktree`, `unknown_bundle`, `missing_input`, `invalid_upload`, `missing_file`, `invalid_id`, `not_found`, `no_file_store`, `invalid_callback_url`, `invalid_callback_headers`, `callback_stream_conflict`, and `run_cancelled` (the caller asked for it; retrying is a new decision, not a recovery).

`retryable: true` covers the transient: `concurrency_limit` (a slot wait interrupted before the work started), `clone_failed` and `work_dir_failed` (filesystem failures that are not policy rejections), `bundle_failed` (a CLI that crashed, exited unexpectedly, timed out, or hit a provider limit), `bundle_list_failed`, `save_failed`, and `server_shutdown` (an async run caught in flight by a restart).

The classification lives in one map in `pkg/server/openai/errorcodes.go`, and the tests parse the package to assert that every code it can emit is classified there — an unclassified code fails the build rather than defaulting quietly.

### Multi-Turn Sessions

Both the gRPC and HTTP APIs accept `session_id`. When a tool reports a native session identifier, the final response includes an opaque client-facing `session_id`; send it back with the **same tool** to resume the conversation. Automatic session discovery currently works for Claude and Gemini. Sessions are in memory only, expire after 30 minutes of inactivity by default, and are lost when `rserve` restarts. Change the TTL with `-session-ttl`.

```bash
# First request — get session_id from response
curl http://127.0.0.1:14261/v1/chat/completions \
  -d '{"model":"claude","messages":[{"role":"user","content":"read main.go"}]}'
# Response includes: "session_id": "abc123..."

# Continue the conversation
curl http://127.0.0.1:14261/v1/chat/completions \
  -d '{"model":"claude","messages":[{"role":"user","content":"now add tests"}],"session_id":"abc123..."}'
```

Unknown, expired, or tool-mismatched IDs are ignored and start a fresh run. The ID maps to the underlying CLI's native resume mechanism rather than exposing that native identifier to clients.

### Bundle Execution API

Bundles can be run over the native loopback HTTP API (in addition to gRPC `RunBundle`). Remote orchestration layers such as Windmill must reach it through authenticated TLS transport:

```bash
# List bundles with their inputs
curl http://127.0.0.1:14261/v1/bundles

# Run a bundle
curl -X POST http://127.0.0.1:14261/v1/bundles/ensemble \
  -H "Content-Type: application/json" \
  -H "X-Correlation-ID: windmill-job-42" \
  -d '{"inputs":{"task":"Design a rate limiter"},"work_dir":"/tmp/ensemble-run"}'
```

- `work_dir` (optional, absolute path): created if missing, and injected as the `output_dir` bundle input unless one is set explicitly.
- **Per-step results:** the response includes a `steps` array (name, tool, model, status, cost, tokens, duration, and per-step output up to 64KB) plus a top-level `output` field carrying the last successful step's output — the natural place for a bundle's final answer or verdict.
- **Streaming:** set `"stream": true` for Server-Sent Events — `step_started`, `step_completed`, and `step_skipped` events during execution, then a final `bundle_completed` event carrying the full response. Lets callers (e.g. Windmill) show live per-step progress on long runs.
- **Options:** `"options": {"opus_only": true, "flash_only": true}` forces Claude steps to Opus / Gemini steps to flash (parity with gRPC `RunBundle`).
- **Inline artifacts:** text files (`.md`, `.txt`, `.json`, `.csv`, `.html`, `.htm`, `.xml`, `.yaml`, `.yml`, `.log`) created or modified under `work_dir` during the run are returned inline in the response (512KB/file, 2MB/response, and 100-artifact caps), so remote callers can review and publish reports without filesystem access to this host.
- **`X-Correlation-ID`:** pass an external run identifier (e.g. a Windmill job UUID); it is echoed in the response body and header, and attached to the run registry entry so `GetStatus` shows which external run owns each slot. Chat completions accept and echo it the same way.
- **`GET /v1/bundles/{name}`** returns the bundle's full step DAG (parallel groups, vote/merge nodes, `if/then/else`) for introspection or rendering by external UIs.
- **Cancellation:** disconnecting the HTTP request (or calling gRPC `CancelRun`) cancels the run — the orchestrator stops between steps and the in-flight step's CLI process is killed. Processes spawned *by* that CLI may survive.
- **Bounds and confinement:** the artifact scan inspects at most 10,000 directory entries. Set `RSERVE_WORK_ROOT` to an absolute directory to require every `work_dir` to live beneath it. Rooted filesystem operations prevent pre-existing symlink components from escaping that directory, and symlinks, FIFOs, and other non-regular files are never collected.
- Status mapping: missing required input → 400, unknown bundle → 404, bundle-logic failure → 200 with `"status": "failure"`. When all run slots are busy the request **queues** for a free slot; 503 is returned only if the client cancels or disconnects while waiting. In streaming mode all post-start outcomes (including errors) arrive as the `bundle_completed` event.

### Authentication

Set the `RSERVE_TOKEN` environment variable before starting `rserve` to require `Authorization: Bearer <token>` on all HTTP endpoints except `/health` (left open for monitoring). Unset means no HTTP authentication. This setting does **not** protect the plaintext gRPC listener, so a token alone is not sufficient before binding to a LAN with `-bind 0.0.0.0`; use loopback or put both listeners behind authenticated TLS transport.

### File Uploads

Upload files to reference them in chat completions. Uploads are limited to 50MB, stored beneath `rserve-files` in the operating system's temporary directory (`os.TempDir()`), tracked in memory, and automatically purged after 24 hours. The response's `path` field is the authoritative path to use in a prompt.

```bash
# Upload a file
curl http://127.0.0.1:14261/v1/files \
  -F purpose=assistants \
  -F file=@data.csv

# Reference it in a prompt (use the returned path)
curl http://127.0.0.1:14261/v1/chat/completions \
  -d '{"model":"claude","messages":[{"role":"user","content":"analyze <path returned by upload>"}]}'
```

The optional multipart `purpose` field defaults to `user_data`. File metadata is reconstructed from disk after a restart, but active chat sessions are not.

## Dashboard and Scheduler

The local Next.js dashboard scans repositories for `_rcodegen` reports and `.grades.json`, displays report and grade history, and manages recurring schedules.

```bash
# Dashboard: http://127.0.0.1:4847
(cd dashboard && npm ci && npm run dev -- --hostname 127.0.0.1)

# In another terminal, start the scheduler daemon
(cd scheduler && npm ci && npm start)
```

The dashboard scans `~/Desktop/_code` by default. Set `RCODEGEN_CODE_DIR` before starting it to use a different repository root:

```bash
RCODEGEN_CODE_DIR="$HOME/code" npm --prefix dashboard run dev -- --hostname 127.0.0.1
```

Schedules are stored in `~/.rcodegen/schedules.json`. The scheduler polls that file every 60 seconds, writes heartbeat and recent-run state to `~/.rcodegen/scheduler-status.json`, and invokes `rcodex <task>` in the selected repository. Cron evaluation currently uses the fixed `America/New_York` timezone. Added, enabled, disabled, or deleted schedules are detected while running; restart the scheduler after changing the cron expression or task of an already-active schedule.

The dashboard and scheduler have no authentication and can read reports or launch unattended Codex tasks. Keep the dashboard bound to localhost and use them only with trusted repositories.

## Adding a New Tool

1. Create `pkg/tools/newtool/newtool.go` implementing the `runner.Tool` interface
2. Create `cmd/rnewtool/main.go`:
   ```go
   package main

   import (
       "fmt"
       "os"

       chassis "github.com/ai8future/chassis-go/v11"
       "github.com/ai8future/chassis-go/v11/logz"
       "github.com/ai8future/chassis-go/v11/registry"
       "rcodegen/pkg/runner"
       "rcodegen/pkg/tools/newtool"
   )

   func main() {
       chassis.RequireMajor(11)
       logger := logz.New("info")
       if err := registry.InitCLI(chassis.Version); err != nil {
           logger.Error("registry init failed", "error", err)
           os.Exit(1)
       }
       tool := newtool.New()
       r := runner.NewRunner(tool)
       result := r.Run()
       if result.Error != nil {
           fmt.Fprintln(os.Stderr, result.Error)
       }
       registry.ShutdownCLI(result.ExitCode)
       os.Exit(result.ExitCode)
   }
   ```
3. Add a build target to the Makefile

## VERSION State Tracking

Tasks automatically skip if the target repository's `VERSION` file has not changed since the last successful run of the same tool+task combination. State is stored in `_rcodegen/version_state.json` within each project directory.

Use `-f`/`--force` to bypass the check and run regardless:

```bash
rclaude -f -c myproject audit
```

Suite mode records each sub-task individually, so partial re-runs work correctly. Codebases without a `VERSION` file are always eligible to run.

## Security Notes

All tools disable permission prompts for unattended operation:
- `rclaude` uses `--dangerously-skip-permissions`
- `rcodex` uses `--dangerously-bypass-approvals-and-sandbox`
- `rgemini` uses `--yolo`
- `ropencode` uses `--dangerously-skip-permissions`
- `rkilo` uses `--dangerously-skip-permissions`

Only use on trusted codebases in controlled environments. Lock files are stored in `~/.rcodegen/locks/` (not `/tmp/`) to prevent symlink attacks. Settings files are created with 0600 permissions.

## Version

Current version: **4.2.10**

See [CHANGELOG.md](CHANGELOG.md) for version history.
