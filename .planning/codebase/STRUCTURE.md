# Codebase Structure

**Analysis Date:** 2026-01-14

## Directory Layout

```
rcodegen/
├── cmd/                          # Binary entry points
│   ├── rclaude/main.go          # Claude CLI wrapper
│   ├── rcodex/main.go           # Codex CLI wrapper
│   ├── rgemini/main.go          # Gemini CLI wrapper
│   └── rcodegen/main.go         # Bundle orchestrator
├── pkg/                          # Library packages
│   ├── runner/                  # Core execution framework
│   ├── tools/                   # Tool implementations
│   │   ├── claude/
│   │   ├── codex/
│   │   └── gemini/
│   ├── bundle/                  # Workflow definitions
│   │   └── builtin/             # Built-in bundles (JSON)
│   ├── orchestrator/            # Multi-step execution
│   ├── executor/                # Step dispatchers
│   ├── workspace/               # Job directory management
│   ├── settings/                # Configuration management
│   ├── reports/                 # Report file management
│   ├── lock/                    # Concurrency control
│   ├── tracking/                # Credit/token tracking
│   └── envelope/                # Result containers
├── .planning/                   # Planning documents (gitignored)
│   └── codebase/               # Codebase analysis docs
├── .github/workflows/          # CI configuration
├── *.py                         # Python automation scripts
├── *.sh                         # Shell wrapper scripts
├── Makefile                     # Build targets
├── go.mod                       # Go module definition
├── README.md                    # User documentation
├── CHANGELOG.md                 # Version history
├── VERSION                      # Current version
└── settings.json.example        # Configuration template
```

## Directory Purposes

**cmd/**
- Purpose: Binary entry points for CLI tools
- Contains: `main.go` files for each tool
- Key files: `rclaude/main.go`, `rcodex/main.go`, `rgemini/main.go`, `rcodegen/main.go`
- Pattern: Minimal main with tool instantiation

**pkg/runner/**
- Purpose: Core execution framework shared by all tools
- Contains: Interface definitions, runner logic, config, flags, stream parsing
- Key files: `tool.go` (interface), `runner.go` (main), `config.go`, `flags.go`, `stream.go`, `output.go`
- Tests: `runner_test.go`, `stream_test.go`

**pkg/tools/**
- Purpose: Individual tool implementations
- Contains: Claude, Codex, Gemini implementations
- Key files: `claude/claude.go`, `codex/codex.go`, `gemini/gemini.go`
- Tests: `claude/claude_test.go`

**pkg/bundle/**
- Purpose: Workflow definitions and bundle loading
- Contains: Bundle data structures, JSON loading logic
- Key files: `bundle.go`, `loader.go`
- Subdirectories: `builtin/` (10 built-in workflow bundles)
- Tests: `loader_test.go`

**pkg/orchestrator/**
- Purpose: Multi-step workflow execution
- Contains: Main orchestrator, context management, progress tracking
- Key files: `orchestrator.go`, `context.go`, `condition.go`, `progress.go`, `live_display.go`

**pkg/executor/**
- Purpose: Step execution dispatchers
- Contains: Tool, parallel, merge, vote executors
- Key files: `dispatcher.go`, `tool.go`, `parallel.go`, `merge.go`, `vote.go`

**pkg/workspace/**
- Purpose: Job workspace with isolated directories
- Contains: Directory management for outputs/logs
- Key files: `workspace.go`
- Tests: `workspace_test.go`

**pkg/settings/**
- Purpose: Configuration loading and interactive setup
- Contains: Settings struct, loading logic, setup wizard
- Key files: `settings.go`
- Tests: `settings_test.go`

**pkg/reports/**
- Purpose: Report file management
- Contains: Review detection, old report cleanup
- Key files: `manager.go`

**pkg/lock/**
- Purpose: File-based locking for concurrent runs
- Contains: Lock acquisition, release, identifier tracking
- Key files: `filelock.go`
- Tests: `filelock_test.go`

**pkg/tracking/**
- Purpose: Credit/token tracking via iTerm2 API
- Contains: Claude Max and Codex tracking
- Key files: `claude.go`, `codex.go`

**pkg/envelope/**
- Purpose: Result container with builder pattern
- Contains: Status, metrics, error wrapping
- Key files: `envelope.go`

## Key File Locations

**Entry Points:**
- `cmd/rclaude/main.go` - Claude CLI entry
- `cmd/rcodex/main.go` - Codex CLI entry
- `cmd/rgemini/main.go` - Gemini CLI entry
- `cmd/rcodegen/main.go` - Bundle orchestrator entry

**Configuration:**
- `go.mod` - Go module definition
- `Makefile` - Build targets
- `settings.json.example` - Configuration template
- `~/.rcodegen/settings.json` - User settings (runtime)

**Core Logic:**
- `pkg/runner/runner.go` - Main execution logic
- `pkg/runner/tool.go` - Tool interface definition
- `pkg/orchestrator/orchestrator.go` - Workflow execution
- `pkg/executor/dispatcher.go` - Step dispatch factory

**Testing:**
- `pkg/runner/runner_test.go` - Runner tests (68 lines)
- `pkg/runner/stream_test.go` - Stream parser tests (164 lines)
- `pkg/settings/settings_test.go` - Settings tests (51 lines)
- `pkg/tools/claude/claude_test.go` - Claude tests (30 lines)
- `pkg/workspace/workspace_test.go` - Workspace tests (98 lines)
- `pkg/lock/filelock_test.go` - Lock tests (64 lines)
- `pkg/bundle/loader_test.go` - Bundle tests (81 lines)

**Documentation:**
- `README.md` - User documentation
- `CHANGELOG.md` - Version history
- `AGENTS.md` - AI agent instructions

**Python Scripts:**
- `get_claude_status.py` - Claude Max credit tracking
- `get_codex_status.py` - Codex credit tracking
- `codex_pty_wrapper.py` - PTY wrapper for session resume
- `claude_question_handler.py` - Multiple choice automation

## Naming Conventions

**Files:**
- snake_case.go - Go source files
- *_test.go - Test files (co-located)
- kebab-case.json - Bundle files
- snake_case.py - Python scripts
- snake_case.sh - Shell scripts

**Directories:**
- lowercase - All directories
- Plural for collections: `tools/`, `builtin/`
- Singular for concepts: `bundle/`, `runner/`, `lock/`

**Special Patterns:**
- `main.go` - Entry points in cmd/
- `*_test.go` - Test files alongside source
- `*.json` - Bundle definitions in builtin/

## Where to Add New Code

**New Tool:**
- Implementation: `pkg/tools/{toolname}/{toolname}.go`
- Entry point: `cmd/r{toolname}/main.go`
- Tests: `pkg/tools/{toolname}/{toolname}_test.go`

**New Bundle:**
- Built-in: `pkg/bundle/builtin/{name}.json`
- User: `~/.rcodegen/bundles/{name}.json`

**New Executor Type:**
- Implementation: `pkg/executor/{type}.go`
- Registration: `pkg/executor/dispatcher.go`

**New Runner Feature:**
- Implementation: `pkg/runner/{feature}.go`
- Tests: `pkg/runner/{feature}_test.go`

**Utilities:**
- Shared helpers: Appropriate pkg/ subdirectory
- Type definitions: With related logic

## Special Directories

**pkg/bundle/builtin/**
- Purpose: Built-in workflow bundles
- Source: Embedded JSON files
- Committed: Yes

**_rcodegen/**
- Purpose: Generated reports per project
- Source: Created at runtime
- Committed: No (gitignored)

**~/.rcodegen/**
- Purpose: User configuration and scripts
- Contains: settings.json, scripts/, bundles/, locks/
- Committed: No (user home directory)

---

*Structure analysis: 2026-01-14*
*Update when directory structure changes*
