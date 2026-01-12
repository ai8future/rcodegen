# Changelog

All notable changes to this project will be documented in this file.

## [1.7.0] - 2026-01-12

### Added
- **Animated live display mode** - New `--live` flag enables TUI-style animated progress
- Real-time spinner animation for running steps (braille dots)
- Live elapsed time counter updates every 100ms
- Live cost display updates as steps complete
- Display interface for pluggable progress implementations
- New `pkg/orchestrator/live_display.go` with full-screen animated display

### Technical Details
- LiveDisplay uses goroutine for animation loop at 100ms intervals
- ANSI cursor control for in-place screen updates
- Supports cursor hide/show, screen clear, home positioning
- Display interface abstracts ProgressDisplay and LiveDisplay
- Both implementations support: Start(), Stop(), SetStepRunning(), SetStepComplete(), SetStepSkipped(), PrintFinalSummary()

### Usage
```bash
rcodegen build-review-audit --live project_name=foo "Build X"
```

### Agent
- Claude:Opus 4.5

## [1.6.3] - 2026-01-11

### Added
- Colorful completion banners for build and article bundles
- Extract and display Overview from IMPLEMENTATION_SUMMARY.md
- Color-coded grade display (green=A, yellow=B, red=C/D/F)
- Bold cost display with green highlight
- Yellow time display for quick scanning
- Blue output path display for visibility

### Changed
- Improved final summary layout with box-drawing characters
- Article completion banner uses magenta theme
- Build completion banner uses green theme
- Grade display shows letter prominently with score in parentheses

### Agent
- Claude:Opus 4.5

## [1.6.2] - 2026-01-11

### Added
- Show output directory in progress header for article bundles
- Print generated article filenames at end of article bundle runs
- Print grade (letter and score) at end of build bundle runs
- Show topic input as task for article bundles in progress display
- Show "parallel" as tool type for parallel steps

### Changed
- Add cyan color for parallel steps in progress display

### Agent
- Claude:Opus 4.5

## [1.6.1] - 2026-01-11

### Fixed
- Fix format string bug in progress display header (extra %s specifier)

### Agent
- Claude:Opus 4.5

## [1.6.0] - 2026-01-11

### Added
- **Colorful progress display** - New visual output with ANSI colors and box-drawing characters when running bundles
- **Per-step progress boxes** - Each step shows numbered progress (Step 1/5), step name, and color-coded tool (magenta for Claude, yellow for Gemini, blue for Codex)
- **Real-time completion feedback** - Completed steps show checkmark, cost, and duration inline
- **Styled summary section** - Final summary with elapsed time, total cost, completion status, and token breakdown

### Changed
- Replaced plain text output with styled progress display using box-drawing characters (rounded corners)
- Step timing now tracked individually for better progress visibility
- Token counts displayed with cache hit/write breakdown

### Technical Details
- New `pkg/orchestrator/progress.go` with `ProgressDisplay` struct
- Methods: `PrintHeader()`, `PrintStepStart()`, `PrintStepComplete()`, `PrintStepSkipped()`, `PrintSummary()`
- ANSI color codes: cyan (borders), magenta (Claude), yellow (Gemini), green (success/cost), dim (metadata)

### Agent
- Claude:Opus 4.5

## [1.5.0] - 2026-01-11

### Added
- **Machine-readable final-report.json** - Detailed JSON report generated alongside final-report.md with full cost breakdowns by model, per-step token usage, file inventory, and extracted grades
- **Bundle auto-copy** - Bundle JSON copied to output directory as `bundle-used.json` for reproducibility
- **default_build_dir setting** - New settings field for default output directory in build bundles
- **Standardized grading rubric** - Audit step enforces consistent grading categories (functionality, code_quality, security, user_experience, architecture, testing, innovation, documentation) with bonus points allowed
- **Positional task argument** - CLI now accepts task description as positional argument: `rcodegen build-review-audit project_name=foo "Build X"`

### Changed
- **build-review-audit bundle** - Now generic (not Quarto-specific), accepts `task` and `project_name` inputs
- Bundle struct now tracks `SourcePath` for copying to output directories
- Settings `LoadWithFallback()` now defaults `DefaultBuildDir` to `CodeDir` if not set

### Technical Details
- `generateFinalReportJSON()` in orchestrator creates comprehensive JSON report
- `extractGradeFromReport()` parses JSON grade block from audit output
- `scanOutputFiles()` inventories output directory with file types and stats
- Grade extraction supports both wrapped `{"grade":{...}}` and direct format

### Agent
- Claude:Opus 4.5

## [1.4.0] - 2026-01-11

### Security
- Move lock files to ~/.rcodegen/locks/ (prevents symlink attacks)
- Add bundle name validation (blocks path traversal)
- Secure settings file permissions (0600)
- Make debug files opt-in via RCLAUDE_DEBUG/RCODEX_DEBUG env vars
- Add settings integrity check (warns if world-writable)

### Added
- GitHub Actions CI/CD workflow (build, test, lint, security scan)
- Package documentation for all packages
- Thread-safe Claude status caching with sync.Once

### Changed
- Replace O(n^2) bubble sort with sort.Slice in reports
- Extract magic numbers to named constants
- Standardize error message format
- Fix global variable mutation in task config
- Refactor parseArgs (189 -> 119 lines) with helper functions
- Add scanner error checking in report reviews
- Check JSON marshal errors in stats output
- Improve error messages with file paths

### Fixed
- JSON marshal error handling in stats output
- Scanner.Err() check in IsReportReviewed

## [1.3.15] - 2026-01-11

### Changed
- Edited and finalized the productivity article in Seth Levine's style, saved as `docs/article-2026-01-11-11-29-04/The Productivity Myth - Codex.md`

## [1.3.14] - 2026-01-11

### Changed
- Drafted a 1000+ word productivity article on getting back your time in Seth Levine's style for docs/article-2026-01-11-11-29-04/draft-codex.md

## [1.3.13] - 2026-01-11

### Changed
- Drafted a 1000+ word productivity article on getting back your time in Seth Levine's style for docs/article-2026-01-11-11-09-30/draft-codex.md

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
