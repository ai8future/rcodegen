# External Integrations

**Analysis Date:** 2026-01-14

## APIs & External Services

**AI Tool CLIs (Primary Integration):**

**Claude Code CLI:**
- Purpose: AI code assistance and automation
- Location: `pkg/tools/claude/claude.go`
- Invocation: `claude --resume [session] -p [task] --dangerously-skip-permissions --model [opus|sonnet|haiku] --max-budget-usd [budget]`
- Models: sonnet (default), opus, haiku
- Output format: `stream-json`, `json`
- Session support: Resume via `--resume` flag

**OpenAI Codex CLI:**
- Purpose: AI code generation and analysis
- Location: `pkg/tools/codex/codex.go`
- Invocation: `codex exec --dangerously-bypass-approvals-and-sandbox --model [name] -c model_reasoning_effort="[level]"`
- Models: gpt-5.2-codex (default), gpt-4.1-codex, gpt-4o-codex
- Reasoning levels: low, medium, high, xhigh
- Session support: Via PTY wrapper (`codex_pty_wrapper.py`)

**Gemini CLI:**
- Purpose: AI code assistance
- Location: `pkg/tools/gemini/gemini.go`
- Invocation: `gemini --resume [session] -p [task] --output-format stream-json --yolo`
- Models: gemini-2.5-pro (default), gemini-2.5-flash, gemini-2.0-pro, gemini-2.0-flash, gemini-3
- Output format: `stream-json`

## Data Storage

**Databases:**
- None - File-based storage only

**File Storage:**
- Reports: `_rcodegen/` directory per project
- Settings: `~/.rcodegen/settings.json`
- Lock files: `~/.rcodegen/locks/`
- Bundles: `~/.rcodegen/bundles/` (user), `pkg/bundle/builtin/` (built-in)
- Workspace: Temp directories via `os.MkdirTemp()`

**Caching:**
- None - Stateless execution

## Authentication & Identity

**Auth Provider:**
- None - Uses user's local CLI tool authentication
- Claude: User's Anthropic API key (managed by claude CLI)
- Codex: User's OpenAI API key (managed by codex CLI)
- Gemini: User's Google API key (managed by gemini CLI)

**OAuth Integrations:**
- None

## Monitoring & Observability

**Error Tracking:**
- None - Errors to stderr, exit codes for status

**Analytics:**
- None

**Logs:**
- Console output only
- Debug mode: `RCLAUDE_DEBUG` environment variable
- Workspace logs: `workspace/logs/{step}.log`

## CI/CD & Deployment

**Hosting:**
- Local CLI tools (not deployed as service)
- Binaries built via `make all`

**CI Pipeline:**
- GitHub Actions: `.github/workflows/ci.yml`
- Runs: `go test ./pkg/...`

## Environment Configuration

**Development:**
- Required: Go 1.25.5+
- Optional: Python 3.11+ (for credit tracking)
- Optional: iTerm2 (macOS, for credit tracking)

**User Configuration:**
- Location: `~/.rcodegen/settings.json`
- Template: `settings.json.example`
- Interactive setup: Run any tool without config

**Example Configuration:**
```json
{
  "code_dir": "~/code",
  "defaults": {
    "codex": {"model": "gpt-5.2-codex", "effort": "high"},
    "claude": {"model": "sonnet", "max_budget": "1.00"},
    "gemini": {"model": "gemini-2.5-pro"}
  },
  "tasks": {
    "audit": {"task": "Security audit", "shortcut": "a"}
  }
}
```

## Terminal Integration

**iTerm2 Python API:**
- Purpose: Credit tracking for Claude Max and Codex
- Scripts: `get_claude_status.py`, `get_codex_status.py`
- Requirement: iTerm2 on macOS with Python API enabled
- Package: `pip install iterm2`
- Fallback: Graceful degradation if unavailable

**How Credit Tracking Works:**
1. Script creates temporary iTerm2 tab
2. Runs tool CLI with `/status` command
3. Captures screen output
4. Parses credit percentages from ANSI text
5. Returns JSON with remaining credits

## Workflow Bundles

**Built-in Workflows:** (`pkg/bundle/builtin/`)

| Bundle | Description | Tools Used |
|--------|-------------|------------|
| `build-review-audit.json` | Build → Review → Audit | Claude, Gemini |
| `article.json` | Research → Draft → Edit | Gemini, Codex, Gemini |
| `article-parallel.json` | Parallel article generation | Multiple |
| `red-team.json` | Implement → Attack → Harden | Claude, Gemini, Claude |
| `compete.json` | Multi-model competition | Multiple |
| `ensemble.json` | Ensemble voting | Multiple |
| `security-review.json` | Security analysis | Multiple |
| `tdd.json` | Test-driven development | Multiple |
| `summary.json` | Content summarization | Multiple |

**Bundle Format:**
```json
{
  "name": "build-review-audit",
  "description": "Claude builds, Gemini reviews, Claude improves",
  "inputs": [
    {"name": "task", "required": true}
  ],
  "steps": [
    {"name": "build", "tool": "claude", "task": "${inputs.task}"},
    {"name": "review", "tool": "gemini", "task": "Review: ${steps.build.stdout}"},
    {"name": "improve", "tool": "claude", "task": "Fix: ${steps.review.stdout}"}
  ]
}
```

## Report Generation

**Report Location:**
- Directory: `_rcodegen/` (unified for all tools)
- Pattern: `{tool}-{codebase}-{taskname}-YYYY-MM-DD_HHMM.md`
- Example: `claude-myproject-audit-2026-01-08_1430.md`

**Report Contents:**
- Task description
- Tool output
- Token usage (if available)
- Execution time
- Cost tracking (before/after credits)

## Webhooks & Callbacks

**Incoming:**
- None

**Outgoing:**
- None

---

*Integration audit: 2026-01-14*
*Update when adding/removing external services*
