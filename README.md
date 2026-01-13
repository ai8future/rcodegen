# rcodegen

Unified automation tools for AI-powered code analysis and reporting.

This monorepo contains automation wrappers for multiple AI coding assistants:

- **rclaude** - Automation for Claude Code CLI
- **rcodex** - Automation for OpenAI Codex CLI
- **rgemini** - Automation for Gemini CLI
- **rcodegen** - Multi-tool orchestrator for running bundles (workflows) across tools

All tools share common infrastructure and provide task automation capabilities for their respective AI platforms.

## Features

- **Task Shortcuts** - Pre-configured prompts for audit, test, fix, refactor, and more
- **Automated Reports** - Generates timestamped markdown reports in `_rcodegen/` directory (prefixed by tool name)
- **Cost Tracking** - Monitors credit usage (Codex) or token costs (Claude)
- **Lock Mode** - Queue multiple runs to prevent conflicts
- **Require Review** - Skip tasks if previous reports haven't been reviewed
- **Delete Old** - Automatically clean up old report versions
- **Unattended Mode** - Runs without permission prompts for automation workflows

## Installation

### Prerequisites

- Go 1.25.5 or later
- Either OpenAI Codex CLI or Claude Code CLI installed
- Python 3.11+ (for rcodex credit tracking via iTerm2)

### Build

```bash
# Build both binaries
make all

# Build individually
make rcodex
make rclaude

# Clean build artifacts
make clean
```

## Usage

### rcodex (OpenAI Codex)

```bash
# Run a single task
rcodex -c myproject audit

# Run all 4 reports sequentially
rcodex -c myproject all

# Run with custom settings
rcodex -c myproject -m gpt-5.2-codex -e xhigh test

# Delete old reports after completion
rcodex -c myproject -D audit

# Skip if previous report unreviewed
rcodex -c myproject -R audit

# Queue behind other runs
rcodex -c myproject -l all

# Check credit status only
rcodex --status-only
```

### rclaude (Claude Code)

```bash
# Run a single task
rclaude -c myproject audit

# Run all 4 reports sequentially
rclaude -c myproject all

# Run with custom model and budget
rclaude -c myproject -m opus -b 20.00 test

# Delete old reports after completion
rclaude -c myproject -D audit

# Skip if previous report unreviewed
rclaude -c myproject -R audit

# Queue behind other runs
rclaude -c myproject -l all
```

## Task Shortcuts

All tools support the following task shortcuts (configured in `~/.rcodegen/settings.json`):

| Shortcut | Description | Reports Generated |
|----------|-------------|-------------------|
| `audit` | Complete security and quality audit | 1 report with patch-ready diffs |
| `test` | Propose comprehensive unit tests | 1 report with test suggestions |
| `fix` | Find and fix bugs, issues, code smells | 1 report with fixes |
| `refactor` | Identify refactoring opportunities | 1 report (no diffs) |
| `quick` | All 4 analyses in a single combined report | 1 report (4 sections) |
| `grade` | Grade code quality and practices | 1 report with scores |
| `generate` | Template task with variables | Custom (use with -x flags) |

## Options

### Common Options (Both Tools)

```
-c, --code <path>    Project path relative to configured code directory
-d, --dir <path>     Absolute working directory
-j, --json           Output as newline-delimited JSON
-l, --lock           Queue behind other running instances
-D, --delete-old     Delete previous reports after successful run
-R, --require-review Skip if previous report unreviewed
-t, --tasks          List available task shortcuts
-h, --help           Show help message
```

### Configuration

Both tools use a unified JSON configuration file at `~/.rcodegen/settings.json`. This file contains:
- Your code directory path (supports `~` expansion)
- All task shortcuts and their prompts

To set up, copy the example file:

```bash
mkdir -p ~/.rcodegen
cp settings.json.example ~/.rcodegen/settings.json
# Edit code_dir to your code directory path
```

Example `settings.json`:
```json
{
  "code_dir": "~/code",
  "defaults": {
    "codex": {
      "model": "gpt-5.2-codex",
      "effort": "xhigh"
    },
    "claude": {
      "model": "sonnet",
      "budget": "10.00"
    },
    "gemini": {
      "model": "gemini-3"
    }
  },
  "tasks": {
    "audit": {
      "prompt": "Run a complete audit of this code..."
    }
  }
}
```

Then `-c myproject` will resolve to `~/code/myproject`.

If no settings file exists, both tools run an interactive setup wizard that helps you configure your code directory and default settings for each tool.

### rcodex-Specific Options

```
-e, --effort <lvl>   Reasoning effort: low, medium, high, xhigh (default: xhigh)
-m, --model <name>   Model name (default: gpt-5.2-codex)
-s, --status         Track credit usage (default: on)
-S, --no-status      Disable credit usage tracking
    --status-only    Show credit status and exit
-x <key=value>       Set variable for task template (can repeat)
```

### rclaude-Specific Options

```
-m, --model <name>   Model: sonnet, opus, haiku (default: sonnet)
-b, --budget <usd>   Max budget in USD per run (default: 10.00)
    --status-only    Show Claude Max credit status and exit
-x <key=value>       Set variable for task template (can repeat)
```

## Project Structure

```
rcodegen/
├── cmd/
│   ├── rclaude/main.go            # Claude automation binary
│   ├── rcodex/main.go             # Codex automation binary
│   ├── rgemini/main.go            # Gemini automation binary
│   └── rcodegen/main.go           # Multi-tool orchestrator
├── pkg/
│   ├── runner/                    # Shared runner framework
│   │   ├── tool.go                # Tool interface definition
│   │   ├── config.go              # Config struct & colors
│   │   ├── flags.go               # Flag parsing utilities
│   │   ├── output.go              # Banner, summary, stats
│   │   ├── stream.go              # Stream-JSON parser
│   │   └── runner.go              # Main orchestrator
│   ├── tools/
│   │   ├── claude/claude.go       # Claude tool implementation
│   │   ├── codex/codex.go         # Codex tool implementation
│   │   └── gemini/gemini.go       # Gemini tool implementation
│   ├── bundle/                    # Bundle (workflow) loading
│   ├── orchestrator/              # Multi-step workflow orchestration
│   ├── executor/                  # Step execution engine
│   ├── envelope/                  # Dispatch envelope format
│   ├── workspace/                 # Job workspace management
│   ├── settings/                  # Unified JSON config loading
│   ├── reports/                   # Report management
│   ├── lock/                      # File locking
│   └── tracking/                  # Cost tracking (Codex & Claude)
├── settings.json.example          # Example settings file
├── get_codex_status.py            # Codex credit tracking (iTerm2)
├── get_claude_status.py           # Claude credit tracking (iTerm2)
├── claude_question_handler.py     # Claude question detection/answering
├── codex_pty_wrapper.py           # Codex PTY wrapper for session resume
├── Makefile                       # Build configuration
└── README.md                      # This file
```

## Adding a New Tool

To add support for a new AI tool:

1. Create `pkg/tools/newtool/newtool.go` implementing `runner.Tool` interface
2. Create `cmd/rnewtool/main.go`:
   ```go
   package main

   import (
       "rcodegen/pkg/runner"
       "rcodegen/pkg/tools/newtool"
   )

   func main() {
       runner.NewRunner(newtool.New()).Run()
   }
   ```
3. Add build target to Makefile

## rcodegen Orchestrator

The `rcodegen` binary runs multi-step workflows (bundles) that can span multiple AI tools:

```bash
# Run a bundle
rcodegen build-review-audit -c myproject "Add user auth"

# Force Claude steps to use Opus
rcodegen build-review-audit -c myproject "task" --opus-only

# List available bundles
rcodegen list
```

Bundles are JSON workflow definitions stored in `~/.rcodegen/bundles/` or built-in.

## Key Differences Between Tools

| Feature | rclaude | rcodex | rgemini |
|---------|---------|--------|---------|
| **CLI Command** | `claude -p` | `codex exec` | `gemini -p` |
| **Permissions** | `--dangerously-skip-permissions` | `--dangerously-bypass-approvals-and-sandbox` | `--yolo` |
| **Output Format** | `stream-json` | `--json` | `stream-json` |
| **Cost Tracking** | iTerm2 API | iTerm2 API | Not yet |
| **Report Prefix** | `claude-` | `codex-` | `gemini-` |
| **Budget Control** | `--max-budget-usd` | None | None |
| **Default Model** | sonnet | gpt-5.2-codex | gemini-3 |

## Report Management

### Report Format

Reports are saved as markdown files with this naming pattern:
```
[tool]-[codebase]-[taskname]-YYYY-MM-DD_HHMM.md
```

Example: `claude-myproject-audit-2026-01-08_1430.md`

### Report Review Workflow

1. **Initial Run**: Creates report with "Date Created: ..." in header
2. **Review**: Edit report and add "Date Modified: ..." in header (top 10 lines)
3. **Subsequent Runs with -R**: Skips tasks if no "Date Modified:" found

### Cleanup with -D

The `-D` flag keeps only the newest report for each task type:
```bash
# Before
_rcodegen/claude-myproject-audit-2026-01-08-10.md
_rcodegen/claude-myproject-audit-2026-01-08-12.md
_rcodegen/claude-myproject-audit-2026-01-08-14.md  # newest

# After: rclaude -D audit
_rcodegen/claude-myproject-audit-2026-01-08-14.md  # only newest kept
```

## Locking & Concurrency

Both tools share a lock file at `/tmp/rcodegen.lock`:

```bash
# Terminal 1
rcodex -l -c project1 all

# Terminal 2 (waits for Terminal 1 to finish)
rclaude -l -c project2 all
```

This prevents concurrent runs from interfering with each other, whether using the same tool or mixing tools.

## Custom Tasks

Add custom tasks to your `~/.rcodegen/settings.json`:

```json
{
  "tasks": {
    "mytask": {
      "prompt": "Analyze this code for X. Save report as {report_file} in {report_dir}."
    }
  }
}
```

Placeholders:
- `{report_file}` - Auto-generated filename: `[tool]-[codebase]-[taskname]-[date].md`
- `{report_dir}` - Expands to `_rcodegen` (unified directory for all tools)
- `{codebase}` - The codebase name from `-c` flag
- `{variable}` - Custom variables passed with `-x variable=value`

## Security Notes

⚠️ **Both tools disable permission prompts** for unattended operation:
- `rcodex` uses `--dangerously-bypass-approvals-and-sandbox`
- `rclaude` uses `--dangerously-skip-permissions`

**Only use on trusted codebases in controlled environments.**

## Version

Current version: **1.8.1**

See [CHANGELOG.md](CHANGELOG.md) for version history.

## License

See LICENSE file for details.
