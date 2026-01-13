# rcodegen

Unified automation tools for AI-powered code analysis and reporting.

This monorepo contains two complementary tools that automate code audits, testing, and refactoring using different AI backends:

- **rcodex** - Automation for OpenAI Codex CLI
- **rclaude** - Automation for Claude Code CLI

Both tools share common infrastructure and provide identical task automation capabilities for their respective AI platforms.

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

Both tools support the following task shortcuts (configured in `tasks.txt` / `tasks_claude.txt`):

| Shortcut | Description | Reports Generated |
|----------|-------------|-------------------|
| `audit` | Complete security and quality audit | 1 report with patch-ready diffs |
| `test` | Propose comprehensive unit tests | 1 report with test suggestions |
| `fix` | Find and fix bugs, issues, code smells | 1 report with fixes |
| `refactor` | Identify refactoring opportunities | 1 report (no diffs) |
| `all_small` | Complete analysis in a single session | 4 reports (audit, test, fix, refactor) |
| `grade` | Grade code quality and practices | 1 report with scores |
| `all` | Run 4 tasks sequentially | 4 reports (slower, more thorough) |
| `complete` | Run all_small + all | 5 reports (maximum coverage) |

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
    }
  },
  "tasks": {
    "audit": {
      "pattern": "code_audit_report_",
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
│   ├── rcodex/main.go             # Codex binary (12 lines)
│   └── rclaude/main.go            # Claude binary (12 lines)
├── pkg/
│   ├── runner/                    # Shared runner framework
│   │   ├── tool.go                # Tool interface definition
│   │   ├── config.go              # Config struct & colors
│   │   ├── flags.go               # Flag parsing utilities
│   │   ├── output.go              # Banner, summary, stats
│   │   └── runner.go              # Main orchestrator
│   ├── tools/
│   │   ├── claude/claude.go       # Claude tool implementation
│   │   └── codex/codex.go         # Codex tool implementation
│   ├── settings/                  # Unified JSON config loading
│   ├── reports/                   # Report management
│   ├── lock/                      # File locking
│   └── tracking/                  # Cost tracking (Codex & Claude)
├── settings.json.example          # Example settings file (copy to ~/.rcodegen/)
├── get_codex_status.py            # Codex credit tracking script
├── Makefile                       # Build configuration
└── README.md                      # This file
```

## Adding a New Tool

To add support for a new AI tool (e.g., rgemini):

1. Create `pkg/tools/gemini/gemini.go` implementing `runner.Tool` interface (~200 lines)
2. Create `cmd/rgemini/main.go`:
   ```go
   package main

   import (
       "rcodegen/pkg/runner"
       "rcodegen/pkg/tools/gemini"
   )

   func main() {
       runner.NewRunner(gemini.New()).Run()
   }
   ```
3. Add to Makefile and build

## Key Differences: rcodex vs rclaude

| Feature | rcodex (Codex) | rclaude (Claude) |
|---------|----------------|------------------|
| **CLI Command** | `codex exec` | `claude -p` |
| **Permissions** | `--dangerously-bypass-approvals-and-sandbox` | `--dangerously-skip-permissions` |
| **Working Directory** | `-C workdir` flag | `cmd.Dir` (no flag) |
| **Output Format** | `--json` | `--output-format json` |
| **Session** | N/A | `--no-session-persistence` |
| **Cost Tracking** | iTerm2 API scraping `/status` | JSON response parsing |
| **Report Directory** | `_rcodegen/` (codex- prefix) | `_rcodegen/` (claude- prefix) |
| **Task Config** | `settings.json` (shared) | `settings.json` (shared) |
| **Budget Control** | None | `--max-budget-usd` |
| **Model Selection** | GPT-5.2-codex, etc. | sonnet, opus, haiku |

## Report Management

### Report Format

Reports are saved as markdown files with this naming pattern:
```
[pattern]_YYYY-MM-DD-HH.md
```

Example: `code_audit_report_2026-01-08-14.md`

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
      "pattern": "my_report_",
      "prompt": "Analyze this code for X. Save report as {report_file} in {report_dir}."
    }
  }
}
```

Placeholders:
- `{report_file}` expands to `[tool]-[codebase]-[taskname]-[date].md` (e.g., `claude-myproject-audit-2026-01-08.md`)
- `{report_dir}` expands to `_rcodegen` (unified directory for all tools)
- `{variable}` custom variables can be passed with `-x variable=value`

## Security Notes

⚠️ **Both tools disable permission prompts** for unattended operation:
- `rcodex` uses `--dangerously-bypass-approvals-and-sandbox`
- `rclaude` uses `--dangerously-skip-permissions`

**Only use on trusted codebases in controlled environments.**

## Version

Current version: **1.3.0**

See [CHANGELOG.md](CHANGELOG.md) for version history.

## License

See LICENSE file for details.
