# Changelog

All notable changes to this project will be documented in this file.

## [1.3.12] - 2026-01-11

### Changed
- Drafted a 1000+ word productivity article on getting back time in Seth Levine's style for docs/draft-codex.md

## [1.3.11] - 2026-01-11

### Changed
- Drafted a 1000+ word productivity article in Seth Levine's style for docs/draft-codex.md

## [1.3.10] - 2026-01-11

### Changed
- Wrote a 1000+ word productivity article in Seth Levine's style for docs/draft-codex.md

## [1.3.9] - 2026-01-11

### Changed
- Rewrote the productivity article in Seth Levine's style for docs/draft-codex.md

## [1.3.8] - 2026-01-11

### Changed
- Wrote a 1000+ word productivity article in Seth Levine's style for docs/draft-codex.md

## [1.3.7] - 2026-01-11

### Changed
- Rewrote the productivity article in Seth Levine's style for docs/draft-codex.md

## [1.3.6] - 2026-01-11

### Changed
- Drafted a 1000+ word productivity article in Seth Levine's style for docs/draft-codex.md

## [1.3.5] - 2026-01-11

### Changed
- Drafted a 1000+ word productivity article in Seth Levine's style for docs/draft.md

## [1.3.4] - 2026-01-11

### Fixed
- Weather v2 CLI returns non-zero when required fields are missing in API responses
- Weather v2 CLI surfaces API error messages from HTTP error bodies and differentiates timeouts

### Changed
- Weather v2 CLI parses responses into a typed structure before display
- Weather v2 CLI sends an explicit user agent header and adds an optional request timeout flag
- Weather v2 CLI warns when API keys are provided as positional arguments

## [1.3.3] - 2026-01-11

### Fixed
- Weather v2 CLI handles non-object payloads and body-level API error codes
- Weather v2 CLI reports rate limit responses with retry hints when available
- Weather v2 CLI avoids misleading zero defaults by showing N/A for missing values

### Changed
- Weather v2 CLI trims city input and sends an explicit JSON accept header

## [1.3.2] - 2026-01-11

### Fixed
- Weather CLI treats non-dict payloads and body-level error codes as API errors
- Weather CLI reports HTTP 429 rate limit responses explicitly
- Weather CLI guards against malformed sections when formatting output

## [1.3.1] - 2026-01-10

### Fixed
- Weather CLI reports invalid JSON responses and rate limits as API errors
- Weather CLI avoids misleading zero values by showing N/A for missing fields
- City input validation blocks empty submissions

### Changed
- Weather CLI output uses ASCII separators and unit labels for terminal compatibility

## [1.3.0] - 2026-01-09

### Added
- **Unified Runner Framework** - New `pkg/runner/` package provides shared infrastructure for all tools
- **Tool Interface** - `runner.Tool` interface allows easy addition of new AI tools (e.g., rgemini)
- **Tool Implementations** - `pkg/tools/claude/` and `pkg/tools/codex/` encapsulate tool-specific logic

### Changed
- **Major Refactoring** - Both tools now use shared runner framework
- `cmd/rclaude/main.go` reduced from ~900 lines to 12 lines
- `cmd/rcodex/main.go` reduced from ~900 lines to 12 lines
- Shared code (~1100 lines) moved to `pkg/runner/`
- Tool-specific code (~500 lines total) in `pkg/tools/`

### Technical Details
- Adding a new tool (e.g., rgemini) now requires only:
  1. Create `pkg/tools/gemini/gemini.go` (~200-250 lines)
  2. Create `cmd/rgemini/main.go` (12 lines)
- `runner.Tool` interface defines: Name, BuildCommand, ShowStatus, ValidModels, etc.
- `runner.SettingsAware` interface for tools that need settings access
- Tool-specific flags, help sections, and banner fields are customizable per tool

## [1.2.0] - 2026-01-09

### Added
- **Interactive Setup Wizard** - First-time users are interviewed to configure their code directory and tool defaults; automatically detects common locations and creates settings file with 6 default tasks
- **Per-tool Defaults** - Settings file now stores default model/budget for rclaude and model/effort for rcodex; setup wizard helps users choose (recommends sonnet for rclaude to maximize API credits)
- **Startup Banner** - Colorful configuration summary displayed at launch showing task, model, budget/effort, codebases, enabled options, and custom variables
- **JSON Stats Output** - New `-J` / `--stats-json` flag outputs run statistics as JSON at completion for automation/scripting
- **Multi-codebase support** - Run tasks across multiple codebases with comma-separated paths: `-c proj1,proj2,proj3`
- **Unified settings.json** - Single configuration file at `~/.rcodegen/settings.json` replaces separate `tasks.txt` and `default_code_dir.txt` files
- **Variable substitution** - Use `-x key=value` flags to substitute `{variable}` placeholders in task prompts
- **Model validation** - rclaude validates model parameter (sonnet, opus, haiku)
- `pkg/settings/` - Unified JSON configuration loading with tilde expansion
- `settings.LoadOrSetup()` - New function that loads settings or runs interactive wizard
- **Conflicting Flag Detection** - Errors if the same flag is specified multiple times with different values (e.g., `-m sonnet -m opus`)
- **Status Check for rclaude** - New `--status-only` flag launches Claude CLI and shows credit status, then exits

### Changed
- Configuration now loaded from `~/.rcodegen/settings.json` instead of text files
- Both tools share the same task definitions
- Lock file still at `/tmp/rcodegen.lock` (unchanged)
- Banner suppressed when `-J` flag is used for clean JSON output

### Technical Details
- `RunStats` struct captures tool, task, model, codebases, timing, exit code, options, and variables
- `printStartupBanner()` uses ANSI color codes for styled terminal output
- `outputStatsJSON()` produces machine-parseable run statistics

## [1.1.0] - 2026-01-08

### Added
- **rclaude** - New binary for Claude Code CLI automation
- Monorepo structure with shared `pkg/` packages
- `pkg/tasks/` - Shared task file parsing
- `pkg/reports/` - Shared report management
- `pkg/lock/` - Shared file locking (now `/tmp/rcodegen.lock`)
- `pkg/tracking/codex.go` - Extracted Codex credit tracking
- `pkg/tracking/claude.go` - New Claude JSON cost tracking
- `tasks_claude.txt` - Claude-specific task definitions
- `Makefile` - Build system for both binaries
- Budget control for rclaude (`--max-budget-usd`)
- Comprehensive README.md documentation

### Changed
- Renamed module from `rcodex` to `rcodegen`
- Refactored rcodex to use shared `pkg/` packages
- Lock file path: `/tmp/rcodex.lock` → `/tmp/rcodegen.lock`
- Lock now shared between rcodex and rclaude
- VERSION bumped to 1.1.0

### Technical Details
- Both tools can run side-by-side (not simultaneously with `-l`)
- rclaude saves reports to `_claude/` directory
- rclaude tracks costs via JSON parsing (no iTerm2 needed)
- Claude uses `--no-session-persistence` for clean execution
- All shared code in `pkg/` for maintainability

## [1.0.0] - 2026-01-07

### Added
- Initial release of rcodex - One-shot task runner for OpenAI Codex CLI
- Task shortcuts: audit, test, fix, refactor, all
- Credit usage tracking before/after tasks via iTerm2 API
- Colorized run summary with timing, model, effort, and credit usage
- Lock mode (-l) for queuing multiple rcodex instances
- Status-only mode (-x) to check credit status without running a task
- Support for custom working directories (-c, -d flags)
- Model and effort level configuration (-m, -e flags)
