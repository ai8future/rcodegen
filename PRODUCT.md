# rcodegen -- Product Overview

## What Is This Product?

rcodegen is a unified automation platform for running AI-powered coding agents (Claude, Codex, Gemini) in fully unattended, hands-off workflows against software codebases. It transforms interactive, human-in-the-loop AI coding assistants into batch-capable, composable automation tools that can audit, test, fix, refactor, grade, build, and write content -- all without a human sitting at the keyboard.

The product exists to solve a fundamental bottleneck: each major AI coding assistant (Anthropic's Claude Code, OpenAI's Codex, Google's Gemini CLI) requires a human operator to babysit prompts, approve permissions, and manually review output. rcodegen eliminates that bottleneck by wrapping these tools in a unified framework that handles unattended execution, output capture, cost control, quality grading, and multi-model orchestration.

---

## Business Goals

### 1. Maximize Developer Leverage Through AI Automation

The core value proposition is letting a single developer (or a CI pipeline) dispatch dozens or hundreds of AI coding tasks overnight, across many codebases, and wake up to graded, actionable reports. Instead of one engineer manually prompting one AI assistant on one codebase at a time, rcodegen allows one engineer to kick off audits, tests, and fixes across an entire portfolio of projects simultaneously.

### 2. Multi-Model Hedging and Quality Assurance

Rather than betting on a single AI vendor, rcodegen orchestrates multiple AI models in adversarial and collaborative workflows. The "ensemble" bundle has three models vote on an approach. The "compete" bundle has two models implement the same task and cross-grade each other. The "red-team" bundle has one model attack another's code. This multi-model approach produces higher-quality output than any single model alone and reduces vendor lock-in.

### 3. Standardized Code Quality Measurement

Every report type (audit, test, fix, refactor, quick) produces a numerical 0-100 grade extracted from the AI's output. Grades are persisted to a cross-process-safe `.grades.json` file per codebase with date, tool, and task metadata. This creates a longitudinal record of code health over time -- effectively an automated, AI-generated code quality scorecard that can be tracked across releases, teams, and projects.

### 4. Cost Visibility and Budget Control

AI API calls are expensive. rcodegen provides per-run cost tracking (token counts, USD costs), per-step cost breakdowns in orchestrated workflows, budget caps per run (Claude's `--max-budget-usd`), and credit status monitoring via iTerm2 integration. The batch runner includes budget-aware execution that can stop, wait, or pause when spending thresholds are reached. This makes AI-powered code analysis financially predictable.

### 5. Serving as an AI-Tool-Agnostic API Gateway

The `rserve` server binary exposes all tools through both a gRPC streaming API and an OpenAI-compatible HTTP API (`/v1/chat/completions`). This means any system that speaks the OpenAI SDK protocol can route requests through rcodegen to any of the three underlying AI engines. This positions rcodegen as a universal gateway and abstraction layer for AI coding tools, enabling dashboards, remote agents, and automated pipelines to leverage whichever AI is best for a given task.

---

## Core Business Logic

### Unattended Single-Tool Execution (rclaude, rcodex, rgemini)

Each wrapper binary (`rclaude`, `rcodex`, `rgemini`) converts the native interactive CLI of each AI tool into a one-shot, unattended execution engine:

- **Permission bypass**: Each tool's safety prompts are automatically bypassed (`--dangerously-skip-permissions`, `--dangerously-bypass-approvals-and-sandbox`, `--yolo`) because there is no human to approve them. This is the critical technical enabler that makes unattended operation possible.

- **Task shortcuts**: Eight built-in task types (`audit`, `test`, `fix`, `refactor`, `quick`, `grade`, `generate`, `study`) plus a `suite` meta-task that runs the five standard report types (`audit`, `test`, `fix`, `refactor`, `quick`) sequentially. The remaining three (`grade`, `generate`, `study`) are standalone shortcuts not included in `suite`. Each shortcut is a carefully engineered prompt that instructs the AI to analyze the codebase, produce a structured report with patch-ready diffs, assign a numerical grade, save the report with a specific filename pattern, and explicitly avoid editing the source code.

- **Multi-codebase execution**: A single command can target multiple codebases (via comma-separated paths, recursive git repo discovery, or directory listing). Each codebase gets its own individually-named report. This enables portfolio-wide analysis in a single invocation.

- **Report lifecycle management**: Reports follow a strict naming convention (`{codebase}-{tool}-{task}-YYYY-MM-DD_HHMM.md`) with automatic creation timestamps, a review workflow (reports get a `Date Created:` field; humans add a `Date Modified:` field after review), and automatic cleanup of old reports via the `-D` flag. The `-R` flag prevents re-running tasks whose previous reports have not been reviewed by a human.

- **VERSION-based idempotency**: If the target codebase contains a `VERSION` file, each tool+task combination records the last-run VERSION to `_rcodegen/version_state.json`. On subsequent runs, if the VERSION has not changed, the task is automatically skipped with a message. Use the `-f`/`--force` flag to run regardless of VERSION state. This prevents redundant AI calls against unchanged codebases.

- **Grade extraction and persistence**: After each task completes, the system scans the generated report for grade patterns (`TOTAL_SCORE: N/100`), extracts the numerical score, and appends it to a `.grades.json` file with cross-process file locking (both in-process mutex and `syscall.Flock`). This creates an auditable history of AI-assessed code quality.

- **Run logging**: Every execution produces a `.runlog` file with metadata (tool, model, codebase, command, start/end times, duration, exit code, token usage, cost). This provides an operational audit trail.

### Multi-Tool Orchestration (rcodegen)

The orchestrator executes "bundles" -- JSON workflow definitions that chain multiple AI tools in sequence or parallel with variable passing, conditional branching, voting, and merging:

- **build-review-audit**: The flagship workflow. Claude builds code, Gemini reviews it, Claude implements improvements, Gemini tests it, and Claude (Opus model) performs a final audit with a structured rubric (Functionality 20, Code Quality 20, User Experience 20, Security 10, Architecture 10, Testing 10, Innovation 5, Documentation 5). This is a full software development lifecycle in one command.

- **ensemble**: Three models (Claude, Gemini, Codex) independently propose approaches in parallel, then a majority vote determines which approach wins. This leverages diverse AI perspectives for decision-making.

- **compete**: Two models implement the same task in parallel, then cross-grade each other's work. This produces competitive quality pressure between models.

- **tdd**: Test-driven development -- Claude writes comprehensive failing tests, Gemini implements the code to pass them, Claude reviews the result. This enforces test-first methodology.

- **red-team**: Claude implements code, Gemini acts as a security researcher finding vulnerabilities and writing exploits, Claude then hardens the code against the found attacks. This is automated adversarial security testing.

- **security-review**: Claude and Gemini independently audit for security vulnerabilities in parallel, then Claude synthesizes both audits into a prioritized report. This provides multi-perspective security analysis.

- **article / article-parallel**: Content creation workflows where Gemini researches the writing style of Seth Levine (author of The New Builders), Codex drafts an article, and Gemini edits for authenticity. The parallel variant produces two competing articles (Codex and Gemini) for comparison. Note: these bundles are currently hardcoded to emulate Seth Levine's style; they require modification for other authors.

- **summary**: Claude summarizes content, Gemini verifies the summary's accuracy. A simple two-step verification workflow.

Each orchestrated run produces:
- Real-time animated TUI with per-step status, cost counters, and activity feeds
- A final-report.json with complete cost breakdowns by model, token counts, grade extraction, and file statistics
- A copy of the bundle used, for reproducibility

### Batch Execution (rbatch)

The batch runner enables large-scale, long-running AI task execution:

- **Manifest-driven**: Jobs are defined in JSON manifests specifying tool, task, model, working directory, and session grouping. Multiple jobs are executed with configurable concurrency.

- **Session chaining**: Jobs sharing a session identifier are executed sequentially within a group, with the session ID carried forward between jobs. This enables multi-turn AI conversations where context builds across tasks.

- **Budget awareness**: The batch runner checks remaining budget between job groups and can automatically stop, wait (polling at intervals with a max wait timeout), or ask for confirmation when spending thresholds are reached. This prevents runaway costs during large batch operations.

- **Checkpoint and resume**: After every batch run, a checkpoint (state.json) is saved with the current queue snapshot -- completed jobs, failed jobs, and pending jobs. The `rbatch resume` command can pick up where a stopped or failed batch left off, carrying forward accumulated costs.

- **Spool processing**: The `rbatch spool` command processes a directory of manifest files, executing each in turn, with manifests moving through pending/running/done/failed states. This enables a simple queue-based workflow.

- **Local and remote execution**: Jobs can be executed locally (spawning AI tool processes directly) or remotely via an `rserve` gRPC connection, enabling distributed execution.

### Server Mode (rserve)

The server binary exposes all capabilities via two network APIs:

- **gRPC API** (default port 14260): Streaming RPCs for RunTask, RunBundle, ListTasks, GetStatus, and CancelRun. Each run streams real-time events (text output, tool use, step progress, results) back to the client. gRPC reflection is enabled for schema discovery.

- **OpenAI-compatible HTTP API** (default port 14261): Full `/v1/chat/completions` compatibility (both streaming SSE and non-streaming), `/v1/models` listing, `/v1/files` upload/download, and `/health` endpoint. Model names use `{tool}:{model}` format (e.g., `claude:opus`). This allows any OpenAI SDK client to use rcodegen as a backend.

- **Concurrency control**: A run registry limits simultaneous executions (default 3 concurrent runs) and provides run IDs for cancellation.

- **Multi-turn sessions**: Session IDs map client-facing IDs to underlying tool-native session resume mechanisms (`--resume` for Claude and Gemini; a PTY wrapper script for Codex session continuation), with TTL-based expiry (30 minutes of inactivity). This enables conversational workflows over the API.

- **File uploads**: Files up to 50MB can be uploaded and referenced in subsequent prompts, with automatic 24-hour cleanup.

### Configuration and Setup

- **First-run wizard**: If no settings file exists, an interactive terminal wizard guides the user through configuring their code directory, preferred models, and budget defaults. This lowers the barrier to entry.

- **Layered configuration**: Settings are merged in priority order: hardcoded defaults < `~/.rcodegen/settings.json` < environment variables (`RCODEGEN_*`) < CLI flags. This supports both personal configuration and CI/CD pipeline parameterization.

- **Custom tasks**: Users can define custom task shortcuts in their settings file with prompt templates using variable substitution (`{report_dir}`, `{report_file}`, `{codebase}`, `{timestamp}`, and user-defined variables via `-x`). Built-in task names are reserved and cannot be overridden, protecting core workflow integrity.

- **Custom bundles**: User-defined workflow bundles can be placed in `~/.rcodegen/bundles/` alongside the 9 built-in bundles.

### Concurrency and Safety

- **File-based locking**: The `-l` flag enables `syscall.Flock`-based advisory locking stored in `~/.rcodegen/locks/` (not `/tmp/`, to prevent symlink attacks). This prevents multiple rcodegen instances from running simultaneously on the same machine when desired, with a 5-minute timeout and 5-second polling.

- **Cross-process grade file safety**: The `.grades.json` file uses both an in-process mutex and a cross-process file lock to prevent corruption from concurrent writers.

- **Graceful cancellation**: Signal-aware contexts propagate Ctrl+C through all execution layers, cleanly stopping multi-codebase runs, suite runs, and orchestrated workflows with partial-result reporting.

- **Security**: Settings files are created with 0600 permissions. Lock directories use 0700 permissions. The system warns about world-writable settings files. All of this reflects awareness that AI tools operating with bypassed permissions are a security-sensitive operation.

---

## Who Is This For?

1. **Individual developers** who want to automate recurring code quality checks across their projects -- running overnight audits, security scans, and test proposals without manual effort.

2. **Engineering teams** who want standardized, AI-generated code quality scorecards across a portfolio of repositories, with historical grade tracking.

3. **Security teams** who want automated, multi-model adversarial security reviews of codebases.

4. **DevOps / CI pipelines** that need programmable access to AI coding tools via gRPC or the OpenAI-compatible HTTP API.

5. **Content creators** who want multi-model article generation with style emulation and editorial quality control.

---

## What Makes This Different From Using AI Coding Tools Directly?

1. **Unattended operation**: Direct use of Claude Code, Codex, or Gemini CLI requires a human at the keyboard. rcodegen runs headless.

2. **Multi-model orchestration**: No single AI tool can build code with one model, have another model attack it, and then have the first model harden it. rcodegen chains tools together in adversarial and collaborative workflows.

3. **Portfolio scale**: Direct tools operate on one codebase at a time. rcodegen can recursively discover and process every git repository in a directory tree.

4. **Quality measurement**: Direct tools produce freeform output. rcodegen extracts structured grades, persists them with locking, and creates a historical quality record.

5. **Cost management**: Direct tools spend whatever they spend. rcodegen tracks costs per-run and per-step, enforces budget caps, and provides batch-level budget controls with stop/wait/ask policies.

6. **API gateway**: Direct tools are CLI-only. rcodegen exposes them via industry-standard APIs (gRPC and OpenAI-compatible HTTP) that any dashboard, agent, or SDK client can consume.
