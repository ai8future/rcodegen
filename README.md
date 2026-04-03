# rcodegen

Unified automation framework for AI-powered code analysis, generation, and reporting. Run multiple AI coding assistants (Claude, Codex, Gemini) in unattended, automated workflows against your codebases.

## What It Does

rcodegen provides two layers of automation:

**Single-tool wrappers** (`rclaude`, `rcodex`, `rgemini`) add task shortcuts, automated reporting, cost tracking, file locking, grading, and multi-codebase support on top of each tool's native CLI.

**Multi-tool orchestrator** (`rcodegen`) chains multiple AI tools together in defined workflows called "bundles" -- one model builds code, another reviews it, another tests it, with parallel execution, voting, and merging.

## Binaries

| Binary | Purpose |
|--------|---------|
| `rclaude` | Claude Code CLI automation wrapper |
| `rcodex` | OpenAI Codex CLI automation wrapper |
| `rgemini` | Google Gemini CLI automation wrapper |
| `rcodegen` | Multi-tool orchestrator for bundles |
| `rbatch` | Batch job runner for executing multiple coding agent tasks with concurrency control, session chaining, and checkpoint/resume |
| `rserve` | gRPC + OpenAI-compatible HTTP server exposing all tools and bundles (default gRPC port 14260, HTTP port 14261) |

## Prerequisites

- Go 1.25.5+
- One or more AI CLIs installed: `claude`, `codex`, `gemini`
- Python 3.11+ (optional, for credit tracking via iTerm2)

## Installation

```bash
# Build all 6 binaries into bin/
make

# Build individually
make rclaude
make rcodex
make rgemini
make rcodegen
make rbatch
make rserve

# Run tests
make test

# Clean
make clean
```

Binaries are built with `-ldflags` to embed the version so `-v` works from any directory. Do not use bare `go build`.

## First Run

If no settings file exists at `~/.rcodegen/settings.json`, an interactive setup wizard runs automatically. It asks for your code directory, preferred models, and budget/effort defaults. You can also copy the example:

```bash
mkdir -p ~/.rcodegen
cp settings.json.example ~/.rcodegen/settings.json
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
```

### Multi-Tool Orchestration

```bash
# Run a build-review-audit workflow
rcodegen build-review-audit -c myproject "Add user authentication"

# Force all Claude steps to use Opus
rcodegen build-review-audit -c myproject "task" --opus-only

# List available bundles
rcodegen list
```

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
```

### Other

```
-v, --version           Show version
-V, --verbose           Enable debug logging
-t, --tasks             List available task shortcuts
-f, --force             Bypass VERSION state check and force run
--status-only           Show status and exit
-h, --help              Show help
```

### Tool-Specific Flags

**rclaude:**
```
-b, --budget <usd>      Max budget in USD per run (default: 10.00, max: 1000.00)
-s, --status            Track credit usage before/after task
-S, --no-status         Disable credit usage tracking
```

**rcodex:**
```
-e, --effort <lvl>      Reasoning effort: low, medium, high, xhigh (default: xhigh)
-s, --status            Track credit usage before/after task
-S, --no-status         Disable credit usage tracking
```

**rgemini:**
```
--flash                 Use gemini-3-flash-preview model
-s, --status            Track usage before/after task
-S, --no-status         Disable usage tracking
```

**rcodegen:**
```
--opus-only             Force all Claude steps to use Opus model
--flash                 Force all Gemini steps to use flash model
--static                Use static display instead of animated TUI
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

### Bundle Features

- **Parallel execution** -- Run substeps concurrently with goroutines
- **Voting** -- Majority or unanimous voting across model outputs
- **Merging** -- Concatenate or deduplicate outputs from multiple steps
- **Conditional steps** -- `if`/`then`/`else` branching based on step results
- **Variable resolution** -- `${inputs.name}`, `${steps.stepname.stdout}`, `${steps.stepname.status}`
- **Session reuse** -- Pass session IDs between steps for context continuity
- **Cost tracking** -- Per-step and aggregate cost/token tracking

## Reports

### Output Location

Reports are saved as markdown files in `_rcodegen/` within each project directory.

### Filename Format

```
{codebase}-{tool}-{task}-YYYY-MM-DD_HHMM.md
```

Example: `myproject-claude-audit-2026-01-20_1430.md`

### Grading System

Each report type extracts a grade (0-100) from the AI's output using patterns like `TOTAL_SCORE: N/100`. Grades are persisted to `_rcodegen/.grades.json` with cross-process file locking for safe concurrent access.

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
    "codex": { "model": "gpt-5.4", "effort": "xhigh" },
    "claude": { "model": "sonnet", "budget": "10.00" },
    "gemini": { "model": "gemini-3-pro-preview" }
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
3. Environment variables (`RCODEGEN_CODE_DIR`, `RCODEGEN_OUTPUT_DIR`, `RCODEGEN_MODEL`, `RCODEGEN_BUDGET`, `RCODEGEN_EFFORT`, `RCODEGEN_LOG_LEVEL`)
4. CLI flags (highest priority)

### Task Template Variables

| Variable | Expands To |
|----------|------------|
| `{report_file}` | Auto-generated filename |
| `{report_dir}` | `_rcodegen` |
| `{codebase}` | Codebase name from `-c` |
| `{timestamp}` | Current timestamp |
| `{variable}` | Custom value from `-x variable=value` |

## Supported Models

### Claude
`sonnet`, `opus`, `haiku` (default: `opus`)

### Codex
`gpt-5.4`, `gpt-5.3-codex`, `gpt-5.2-codex`, `gpt-4.1-codex`, `gpt-4o-codex` (default: `gpt-5.4`)

### Gemini
`gemini-3.1-pro-preview`, `gemini-3-flash-preview`, `gemini-2.5-pro`, `gemini-2.5-flash`, `gemini-2.5-flash-lite` (default: `gemini-3.1-pro-preview`)

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
- Color-coded by tool: magenta (Claude), yellow (Gemini), blue (Codex)
- Live activity feed parsed from stream-json output (e.g., "Reading files...", "Writing code...")

Use `--static` to disable animation.

## Tool Comparison

| Feature | rclaude | rcodex | rgemini |
|---------|---------|--------|---------|
| CLI Command | `claude -p` | `codex exec` | `gemini -p` |
| Permission Bypass | `--dangerously-skip-permissions` | `--dangerously-bypass-approvals-and-sandbox` | `--yolo` |
| Output Format | stream-json | --json | stream-json |
| Cost Tracking | iTerm2 API | iTerm2 API | iTerm2 API |
| Budget Control | `--max-budget-usd` | None | None |
| Default Model | opus | gpt-5.4 | gemini-3.1-pro-preview |

## Project Structure

```
rcodegen/
├── cmd/
│   ├── rclaude/main.go              # Claude CLI entry point (~15 lines)
│   ├── rcodex/main.go               # Codex CLI entry point (~15 lines)
│   ├── rgemini/main.go              # Gemini CLI entry point (~15 lines)
│   ├── rcodegen/main.go             # Orchestrator entry point
│   ├── rbatch/main.go               # Batch job runner entry point
│   └── rserve/main.go               # gRPC + HTTP server entry point (default port 14260)
├── pkg/
│   ├── runner/                      # Core execution framework
│   │   ├── tool.go                  # Tool interface (27 methods)
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
│   │   └── gemini/gemini.go         # Gemini tool implementation
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
├── bin/                             # Compiled binaries
├── Makefile                         # Build system with ldflags
├── settings.json.example            # Example config
├── get_codex_status.py              # Codex credit tracking (iTerm2)
├── get_claude_status.py             # Claude credit tracking (iTerm2)
├── get_gemini_status.py             # Gemini credit tracking (iTerm2)
├── claude_question_handler.py       # Claude question detection/answering
└── codex_pty_wrapper.py             # Codex PTY wrapper for session resume
```

## rserve (gRPC + HTTP Server)

`rserve` exposes all tools and bundle orchestration via both a streaming gRPC API and an OpenAI-compatible HTTP API, intended for use by dashboards, remote agents, or any OpenAI SDK client.

**Default ports:** gRPC `14260`, HTTP `14261` (port+1). Binds to localhost only by default.

```bash
# Start with defaults
rserve

# Custom port, concurrency, and bind address
rserve -port 9000 -max-concurrent 5 -bind 0.0.0.0

# Show version
rserve -v
```

**Flags:**

| Flag | Description | Default |
|------|-------------|---------|
| `-port` | gRPC listen port (HTTP is port+1) | `14260` |
| `-bind` | Bind address (`0.0.0.0` for all interfaces) | `127.0.0.1` |
| `-max-concurrent` | Max simultaneous runs | `3` |
| `-v` | Show version and exit | |

### gRPC API

| RPC | Description |
|-----|-------------|
| `RunTask` | Run a single tool (claude/codex/gemini), stream events |
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

### OpenAI-Compatible HTTP API

The HTTP API on port+1 is compatible with any OpenAI SDK. Model names follow the format `{tool}` or `{tool}:{model}` (e.g., `claude`, `claude:opus`, `gemini:gemini-3.1-pro-preview`).

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/v1/chat/completions` | POST | Chat completion (streaming and non-streaming) |
| `/v1/models` | GET | List available tool/model combinations |
| `/v1/files` | POST | Upload a file (multipart, 50MB limit) |
| `/v1/files` | GET | List uploaded files |
| `/v1/files/{id}` | GET | Download a file |
| `/v1/files/{id}` | DELETE | Delete a file |
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
```

### Multi-Turn Sessions

Both the gRPC and HTTP APIs support multi-turn conversations via `session_id`. The first response includes a `session_id`; send it back in subsequent requests to resume the conversation. Sessions expire after 30 minutes of inactivity.

```bash
# First request — get session_id from response
curl http://127.0.0.1:14261/v1/chat/completions \
  -d '{"model":"claude","messages":[{"role":"user","content":"read main.go"}]}'
# Response includes: "session_id": "abc123..."

# Continue the conversation
curl http://127.0.0.1:14261/v1/chat/completions \
  -d '{"model":"claude","messages":[{"role":"user","content":"now add tests"}],"session_id":"abc123..."}'
```

Under the hood, `session_id` maps to the underlying CLI tool's native session resume mechanism (`claude --resume`, `codex` session resume, `gemini --resume`).

### File Uploads

Upload files to reference them in chat completions. Files are stored in `/tmp/rserve-files/` and automatically purged after 24 hours.

```bash
# Upload a file
curl http://127.0.0.1:14261/v1/files \
  -F purpose=assistants \
  -F file=@data.csv

# Reference it in a prompt (use the returned path)
curl http://127.0.0.1:14261/v1/chat/completions \
  -d '{"model":"claude","messages":[{"role":"user","content":"analyze /tmp/rserve-files/.../data.csv"}]}'
```

## Adding a New Tool

1. Create `pkg/tools/newtool/newtool.go` implementing the `runner.Tool` interface
2. Create `cmd/rnewtool/main.go`:
   ```go
   package main

   import (
       "fmt"
       "os"

       chassis "github.com/ai8future/chassis-go/v10"
       "github.com/ai8future/chassis-go/v10/logz"
       "github.com/ai8future/chassis-go/v10/registry"
       "rcodegen/pkg/runner"
       "rcodegen/pkg/tools/newtool"
   )

   func main() {
       chassis.RequireMajor(10)
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

Only use on trusted codebases in controlled environments. Lock files are stored in `~/.rcodegen/locks/` (not `/tmp/`) to prevent symlink attacks. Settings files are created with 0600 permissions.

## Version

Current version: **4.0.14**

See [CHANGELOG.md](CHANGELOG.md) for version history.
