# Changelog

All notable changes to this project will be documented in this file.

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
