# Technology Stack

**Analysis Date:** 2026-01-14

## Languages

**Primary:**
- Go 1.25.5 - All application code (`go.mod`)
- Python 3.11+ - Status tracking and automation scripts (`get_claude_status.py`, `get_codex_status.py`, `codex_pty_wrapper.py`, `claude_question_handler.py`)

**Secondary:**
- Shell (Bash) - Wrapper scripts (`claude_wrapper.sh`, `codex_wrapper.sh`)
- JSON - Bundle configuration files (`pkg/bundle/builtin/*.json`)

## Runtime

**Environment:**
- Go runtime - Primary language for all binaries
- Python 3 (homebrew-preferred: `/opt/homebrew/bin/python3.13`) - Status tracking scripts
- iTerm2 (macOS terminal) - Required for credit tracking via Python API

**Package Manager:**
- Go modules - `go.mod`
- No external Go dependencies (stdlib only)
- Lockfile: None needed (no external deps)

## Frameworks

**Core:**
- None (vanilla Go with custom runner framework)
- Plugin-based tool abstraction (`pkg/runner/tool.go`)

**Testing:**
- Go standard `testing` package - Unit tests
- No external test framework

**Build/Dev:**
- Make - Build targets in `Makefile`
- Targets: `all`, `rcodex`, `rclaude`, `rcodegen`, `rgemini`, `clean`, `test`

## Key Dependencies

**Critical:**
- No external Go packages - Pure stdlib implementation
- `iterm2` Python package - Credit tracking via iTerm2 API

**Infrastructure:**
- Go stdlib: `os/exec`, `encoding/json`, `io`, `path/filepath`
- Go stdlib: `syscall` for file locking
- Python: `asyncio`, `dataclasses`, `re` (all stdlib)

## Configuration

**Environment:**
- `ITERM_SESSION_ID` - Required for credit tracking
- `RCLAUDE_DEBUG` - Debug mode flag
- No .env files required

**Build:**
- `go.mod` - Module definition (rcodegen, go 1.25.5)
- `Makefile` - Build targets

**User Configuration:**
- `~/.rcodegen/settings.json` - User preferences
- `settings.json.example` - Configuration template

## Platform Requirements

**Development:**
- macOS/Linux (any platform with Go 1.25.5+)
- Python 3.11+ (for status tracking)
- iTerm2 (macOS only, for credit tracking)

**Production:**
- Compiled binaries: `rclaude`, `rcodex`, `rgemini`, `rcodegen`
- Installed to user's PATH
- Runs on macOS with iTerm2 for full functionality

---

*Stack analysis: 2026-01-14*
*Update after major dependency changes*
