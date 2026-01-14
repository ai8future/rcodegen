# Architecture

**Analysis Date:** 2026-01-14

## Pattern Overview

**Overall:** Modular CLI Framework with Plugin-Based Tool Abstraction

**Key Characteristics:**
- Multi-tool wrapper with unified interface (Claude, Codex, Gemini)
- Plugin-based extensibility via Tool interface
- Bundle/workflow orchestration for multi-step AI tasks
- Stream-JSON parsing for real-time output formatting
- File-based state (no database)

## Layers

**CLI Entry Layer:**
- Purpose: Parse arguments and bootstrap tool execution
- Contains: `cmd/rclaude/main.go`, `cmd/rcodex/main.go`, `cmd/rgemini/main.go`, `cmd/rcodegen/main.go`
- Depends on: Runner framework, Tool implementations
- Used by: Direct CLI invocation

**Runner Framework Layer:**
- Purpose: Unified execution framework for all tools
- Contains: `pkg/runner/runner.go`, `pkg/runner/config.go`, `pkg/runner/flags.go`, `pkg/runner/stream.go`, `pkg/runner/output.go`, `pkg/runner/tool.go`
- Depends on: Settings, Reports, Lock packages
- Used by: All tool implementations

**Tool Implementation Layer:**
- Purpose: Tool-specific command building and status tracking
- Contains: `pkg/tools/claude/claude.go`, `pkg/tools/codex/codex.go`, `pkg/tools/gemini/gemini.go`
- Depends on: Runner framework, Tracking package
- Used by: Runner layer

**Orchestration Layer:**
- Purpose: Multi-step workflow execution
- Contains: `pkg/orchestrator/orchestrator.go`, `pkg/orchestrator/context.go`, `pkg/orchestrator/condition.go`, `pkg/orchestrator/progress.go`, `pkg/orchestrator/live_display.go`
- Depends on: Bundle, Executor, Workspace packages
- Used by: rcodegen main

**Execution Layer:**
- Purpose: Dispatch step execution to appropriate handler
- Contains: `pkg/executor/dispatcher.go`, `pkg/executor/tool.go`, `pkg/executor/parallel.go`, `pkg/executor/merge.go`, `pkg/executor/vote.go`
- Depends on: Bundle, Workspace, Envelope packages
- Used by: Orchestrator

**Support Services Layer:**
- Purpose: Cross-cutting concerns
- Contains: `pkg/settings/`, `pkg/reports/`, `pkg/lock/`, `pkg/workspace/`, `pkg/tracking/`, `pkg/envelope/`
- Depends on: Go stdlib only
- Used by: All layers

## Data Flow

**Single Tool (rclaude/rcodex/rgemini) Flow:**

1. User runs: `rclaude -t "analyze code"`
2. Runner parses flags (`pkg/runner/flags.go`)
3. Settings loaded from `~/.rcodegen/settings.json` (`pkg/settings/settings.go`)
4. Task shortcuts expanded (`pkg/runner/runner.go:expandTaskShortcut()`)
5. Lock acquired if `-l` flag (`pkg/lock/filelock.go`)
6. For each working directory:
   - Tool.BuildCommand() constructs exec.Cmd
   - Command executed, output streamed
   - Stream-JSON parsed if applicable (`pkg/runner/stream.go`)
   - Report written to `_rcodegen/` directory
7. Summary printed, exit with status code

**Multi-Tool Bundle Flow (rcodegen):**

1. User runs: `rcodegen build-review-audit`
2. Bundle loaded from JSON (`pkg/bundle/loader.go`)
3. Orchestrator created with tool registry (`pkg/orchestrator/orchestrator.go`)
4. For each step in bundle:
   - Dispatcher selects executor type (`pkg/executor/dispatcher.go`)
   - Step executed (tool, parallel, merge, or vote)
   - Output written to workspace (`pkg/workspace/workspace.go`)
   - Context updated with step results
5. Final summary with totals printed
6. JSON envelope output if `-j` flag

**State Management:**
- File-based: Reports in `_rcodegen/`, settings in `~/.rcodegen/`
- Lock files in `~/.rcodegen/locks/`
- No persistent in-memory state across runs

## Key Abstractions

**Tool Interface:**
- Purpose: Define contract for AI tool implementations
- Location: `pkg/runner/tool.go`
- Methods: BuildCommand, ValidateConfig, ShowStatus, DefaultModel, etc.
- Implementations: Claude, Codex, Gemini

**Config Struct:**
- Purpose: Runtime configuration for task execution
- Location: `pkg/runner/config.go`
- Contains: Task, Model, OutputDir, Vars, SessionID, etc.
- Pattern: Value object passed through execution

**Bundle/Step:**
- Purpose: Define multi-step workflow
- Location: `pkg/bundle/bundle.go`
- Contains: Steps (tool, parallel, merge, vote), Inputs, Metadata
- Pattern: Declarative workflow definition

**Envelope:**
- Purpose: Wrap step execution results
- Location: `pkg/envelope/envelope.go`
- Contains: Status, Stdout, Stderr, Metrics, Error
- Pattern: Result container with builder API

**Orchestrator:**
- Purpose: Execute bundle workflows
- Location: `pkg/orchestrator/orchestrator.go`
- Pattern: Interpreter pattern for bundle execution

## Entry Points

**CLI Entry:**
- Location: `cmd/rclaude/main.go`, `cmd/rcodex/main.go`, `cmd/rgemini/main.go`
- Pattern: Minimal main with tool instantiation + runner
- Example: `tool := claude.New(); runner.NewRunner(tool).RunAndExit()`

**Bundle Entry:**
- Location: `cmd/rcodegen/main.go`
- Pattern: Standalone main with flag parsing and orchestration
- Triggers: `rcodegen <bundle-name> [options]`

## Error Handling

**Strategy:** Throw errors, catch at boundaries, log and exit

**Patterns:**
- Go: Explicit error checking with `if err != nil`
- Error wrapping: `fmt.Errorf("context: %w", err)`
- Exit codes: 0 (success), 1 (error), 2 (lock conflict)

## Cross-Cutting Concerns

**Logging:**
- Console output with ANSI colors
- No logging framework (stdout/stderr only)
- Debug mode via `RCLAUDE_DEBUG` env var

**Validation:**
- Bundle name validation via regex (`pkg/bundle/loader.go:16`)
- Model validation against allowed list per tool
- Placeholder validation before execution

**File Operations:**
- Settings file: 0600 permissions (owner only)
- Lock files: 0600 permissions (owner only)
- Report files: 0644 permissions (readable)

**Status Tracking:**
- Credit tracking via iTerm2 Python API
- Before/after snapshots for cost calculation
- Graceful degradation if iTerm2 unavailable

---

*Architecture analysis: 2026-01-14*
*Update when major patterns change*
