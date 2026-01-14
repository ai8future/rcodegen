# Coding Conventions

**Analysis Date:** 2026-01-14

## Naming Patterns

**Files:**
- Go: snake_case.go (`pkg/runner/runner.go`, `pkg/tools/claude/claude.go`)
- Tests: *_test.go alongside source (`pkg/runner/runner_test.go`)
- Python: snake_case.py (`get_claude_status.py`, `codex_pty_wrapper.py`)
- Bundles: kebab-case.json (`pkg/bundle/builtin/build-review-audit.json`)

**Functions:**
- Go exported: PascalCase (`NewRunner`, `BuildCommand`, `PrintStartupBanner`)
- Go unexported: camelCase (`runError`, `parseArgs`, `expandTilde`)
- Python: lowercase_with_underscores (`get_screen_text`, `parse_status_output`)

**Variables:**
- Go: camelCase for local variables
- Go receivers: Short 1-2 letter names (`t *Tool`, `r *Runner`, `s *Settings`)
- Constants: PascalCase in Go (`Bold`, `Cyan`, `MaxDisplayTaskLen`)
- Python constants: SCREAMING_SNAKE_CASE (`DEBUG_MODE`, `SCRIPT_DIR`)

**Types:**
- Go interfaces: PascalCase, no I prefix (`Tool`, not `ITool`)
- Go structs: PascalCase (`Config`, `RunResult`, `ClaudeStatus`)
- Python dataclasses: PascalCase (`QuestionOption`, `DetectedQuestion`)

## Code Style

**Formatting:**
- Go: Standard gofmt (tabs, no semicolons, Go conventions)
- Python: PEP 8 informal (4-space indent, lowercase functions)
- Line length: No strict limit, reasonable wrapping

**Linting:**
- Go: No explicit linter config, follows Go conventions
- Python: No linter config files
- Run: `go test ./pkg/...` via Makefile

## Import Organization

**Go Order:**
1. Standard library imports
2. Internal project imports (`rcodegen/pkg/...`)
3. No external dependencies

**Go Grouping:**
- Single import block
- Alphabetical within group

**Python Order:**
1. Standard library imports
2. Conditional imports (e.g., `iterm2`)

## Error Handling

**Go Patterns:**
- Explicit error checking: `if err != nil { return err }`
- Error wrapping: `fmt.Errorf("context: %w", err)`
- Exit codes: 0 success, 1 error, 2 lock conflict

**Go Error Types:**
- Return errors up the stack
- Log at boundaries (runner level)
- Use error messages for context

**Python Patterns:**
- Try/except at boundaries
- JSON error responses for scripts
- Graceful degradation (status tracking)

## Logging

**Framework:**
- No logging framework (stdout/stderr only)
- ANSI color codes for terminal output

**Go Patterns:**
- `fmt.Printf()` for normal output
- `fmt.Fprintf(os.Stderr, ...)` for errors
- Color constants: `Bold`, `Cyan`, `Green`, `Yellow`, `Dim`, `Reset`

**Python Patterns:**
- `print()` to stdout
- JSON output for programmatic use
- Debug mode via environment variable

## Comments

**When to Comment:**
- Package-level doc comments (required)
- Function-level for exported functions
- Complex logic explanation
- No obvious comments

**Go Doc Comments:**
```go
// Package runner provides the core execution framework for rcodegen tools.
// It handles argument parsing, task execution, and output formatting.
package runner
```

**Go Function Comments:**
```go
// RunAndExit runs the task and exits with the appropriate code.
// This is the entry point for CLI binaries.
func (r *Runner) RunAndExit()
```

**Python Docstrings:**
```python
"""
get_claude_status.py - Capture Claude Max credit status using iTerm2 API

Usage:
    python3 get_claude_status.py
"""
```

**TODO Comments:**
- None present in codebase (actively maintained)
- Format if used: `// TODO: description`

## Function Design

**Size:**
- Keep functions focused, extract helpers
- No strict line limit

**Parameters:**
- Go: Use Config struct for many parameters
- Go: Pointer receivers for methods
- Python: Keyword arguments for clarity

**Return Values:**
- Go: Explicit error returns
- Go: Named return values rarely used
- Python: Dict/dataclass for structured returns

## Module Design

**Exports:**
- Go: PascalCase for exported, camelCase for internal
- Each tool implements Tool interface
- Packages expose minimal public API

**Package Boundaries:**
- `runner/` - Framework shared by all tools
- `tools/` - Each tool isolated in own package
- `orchestrator/` - Workflow execution
- `executor/` - Step dispatchers
- Support packages: `settings/`, `lock/`, `reports/`, etc.

**Interfaces:**
```go
// Tool defines the interface that each AI tool must implement.
type Tool interface {
    Name() string
    BinaryName() string
    BuildCommand(cfg *Config, workDir, task string) *exec.Cmd
    // ... more methods
}
```

## Go-Specific Patterns

**Interface Satisfaction:**
- Tools implement `runner.Tool` interface
- Compile-time verification via assignment

**Configuration:**
- Use struct with exported fields
- Apply defaults via method calls
- Validate before execution

**Concurrency:**
- `sync.Once` for lazy initialization
- File-based locking for cross-process
- No goroutines in core execution

## Python-Specific Patterns

**Async/Await:**
- iTerm2 API uses asyncio
- `iterm2.run_until_complete(main)` pattern

**Dataclasses:**
```python
@dataclass
class QuestionOption:
    label: str
    description: str
    index: int
```

**Type Hints:**
- Function signatures have type hints
- Dataclass fields typed

---

*Convention analysis: 2026-01-14*
*Update when patterns change*
