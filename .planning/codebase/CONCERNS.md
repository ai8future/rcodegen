# Codebase Concerns

**Analysis Date:** 2026-01-14

## Tech Debt

**Python Dependency Management Missing:**
- Issue: No `requirements.txt`, `pyproject.toml`, or `setup.py` for Python dependencies
- Files: `get_codex_status.py`, `get_claude_status.py`, `claude_question_handler.py`, `codex_pty_wrapper.py`
- Why: Python scripts added incrementally without package management
- Impact: Users must manually install `iterm2` package; installation unclear from README
- Fix approach: Add `requirements.txt` with `iterm2` version pinning

**Bare Exception Handling in Python:**
- Issue: Overly broad exception handling swallows all errors
- Files: `codex_pty_wrapper.py:72`, `codex_pty_wrapper.py:83`, `codex_pty_wrapper.py:91`
- Why: Quick error suppression during development
- Impact: Silent failures make debugging difficult
- Fix approach: Catch specific exceptions (OSError, IOError, TimeoutError)

## Known Bugs

**No Known Bugs:**
- Codebase has no TODO/FIXME comments
- No open issues documented in code

## Security Considerations

**Script Discovery Complexity:**
- Risk: Multiple fallback paths for locating Python scripts could allow unintended execution
- Files: `pkg/tracking/codex.go:87-118`, `pkg/bundle/loader.go:70-100`
- Current mitigation: Scripts only loaded from trusted locations (executable dir, `~/.rcodegen/`)
- Recommendations: Document trusted locations; consider signature verification for production

**File Permission Inconsistency:**
- Risk: Report files world-readable (0644) while config uses restricted permissions (0600)
- Files: `pkg/orchestrator/orchestrator.go:340, 609, 1101` (0644), `pkg/settings/settings.go:522` (0600)
- Current mitigation: Reports don't contain secrets
- Recommendations: Standardize to 0600 if reports may contain sensitive analysis

**Unguarded Environment Variable Access:**
- Risk: Direct `os.environ` access without `.get()` fallback could fail
- Files: `codex_pty_wrapper.py:35`
- Current mitigation: Runs in terminal context where TERM is set
- Recommendations: Use `os.environ.get('TERM', 'xterm-256color')` default

## Performance Bottlenecks

**No Execution Timeout:**
- Problem: Tool execution has no timeout safeguard
- Files: `pkg/executor/tool.go:85` - `cmd.Run()` with no timeout
- Measurement: Long-running AI tasks could hang indefinitely
- Cause: No context.Context passed through execution chain
- Improvement path: Add execution timeout (e.g., 30 minutes) or context-based cancellation

## Fragile Areas

**Stream Parser Event Types:**
- Files: `pkg/runner/stream.go:74-100`
- Why fragile: Only processes 4 event types (system, assistant, user, result)
- Common failures: Unknown event types silently ignored
- Safe modification: Add logging for unknown types; document expected formats
- Test coverage: `pkg/runner/stream_test.go` (164 lines)

**Session ID Extraction:**
- Files: `pkg/executor/tool.go` - `extractSessionID()` function
- Why fragile: Regex-based extraction from tool output
- Common failures: Format changes in tool output break extraction
- Safe modification: Log extraction failures; validate format before caching
- Test coverage: No explicit test for session extraction

## Scaling Limits

**Not Applicable:**
- CLI tools, no server-side scaling concerns
- Single-process execution model

## Dependencies at Risk

**iterm2 Python Package:**
- Risk: API changes could break credit tracking
- Impact: Status tracking feature stops working
- Migration plan: None needed - graceful degradation already implemented
- Evidence: Code falls back to "data not available" if package unavailable

**External CLI Tools:**
- Risk: Claude/Codex/Gemini CLI changes could break command building
- Files: `pkg/tools/*/` command construction
- Impact: Tool execution fails
- Migration plan: Update flag handling when tools change

## Missing Critical Features

**No Major Gaps:**
- Core functionality complete
- All documented features implemented

**Nice-to-Have:**
- Requirements.txt for Python dependencies
- Execution timeouts
- Session cleanup for Codex

## Test Coverage Gaps

**Python Scripts Not Tested:**
- What's not tested: `get_claude_status.py`, `get_codex_status.py`, `codex_pty_wrapper.py`, `claude_question_handler.py`
- Risk: Python script changes could break credit tracking
- Priority: Low (graceful degradation exists)
- Difficulty: Would need iTerm2 mock

**Orchestrator Integration:**
- What's not tested: Full multi-step workflow execution
- Risk: Bundle execution could break without detection
- Priority: Medium
- Difficulty: Would need tool mocks

**Session Reuse:**
- What's not tested: Session ID extraction and reuse across steps
- Risk: Session optimization may silently break
- Priority: Low
- Difficulty: Would need regex match validation

## Documentation Gaps

**Python Setup Instructions:**
- Location: `README.md`
- Missing: Clear `pip install iterm2` instructions
- Impact: Users confused about credit tracking setup

**Platform Limitations:**
- Location: `README.md`
- Missing: Clear statement that credit tracking only works on macOS with iTerm2
- Impact: Linux/Windows users confused by "data not available"

**Bundle Loading Paths:**
- Location: `README.md`
- Missing: Document that bundles can come from `~/.rcodegen/bundles/` or built-in
- Impact: Users don't know they can add custom bundles

---

*Concerns audit: 2026-01-14*
*Update as issues are fixed or new ones discovered*
