# rbatch Design

**Date:** 2026-03-07
**Status:** Approved

## Overview

`rbatch` is a batch job runner for the rcodegen suite. It pulls jobs from a queue (manifest file, spool directory, or gRPC API), executes them with concurrency control, session continuity, and budget awareness, and produces comprehensive reporting.

## Architecture

```
rbatch
  |
  +-- Manifest Loader (JSON file)
  +-- Spool Watcher (directory inbox)
  +-- gRPC Client (rserve submissions)
  |
  +-- Job Queue (state machine: pending -> running -> done/failed)
  |
  +-- Scheduler
  |     - Builds session groups from flat job list
  |     - Session chains run sequentially (shared session ID)
  |     - Independent groups run in parallel up to concurrency limit
  |     - Budget checks at group boundaries
  |
  +-- Local Executor (pkg/runner, in-process)
  +-- Remote Executor (gRPC to rserve)
  |
  +-- Reporter (live display, per-job results, batch summary)
  +-- Checkpoint (save/resume state, crash recovery)
```

## Job Manifest Format

```json
{
  "name": "nightly-audit",
  "concurrency": 4,
  "budget": {
    "threshold_pct": 1,
    "on_budget": "stop",
    "check_interval": "3m",
    "max_wait": "1h"
  },
  "jobs": [
    {
      "name": "audit-projectA",
      "task": "audit",
      "tool": "claude",
      "dir": "/code/projectA",
      "session": "chain-1"
    },
    {
      "name": "fix-projectA",
      "task": "fix issues found above",
      "tool": "claude",
      "dir": "/code/projectA",
      "session": "chain-1"
    },
    {
      "name": "audit-projectB",
      "task": "audit",
      "tool": "claude",
      "dir": "/code/projectB",
      "session": "chain-2",
      "model": "opus"
    },
    {
      "name": "update-deps-C",
      "task": "update-deps",
      "tool": "codex",
      "dir": "/code/projectC",
      "effort": "high"
    },
    {
      "name": "lint-projectD",
      "task": "fix all lint warnings",
      "tool": "gemini",
      "dir": "/code/projectD"
    }
  ]
}
```

### Field Reference

- **name** - Batch and per-job names for reporting
- **concurrency** - Max parallel session groups (overridable via CLI)
- **budget.threshold_pct** - Check remaining credits via status-only; pause if below this percentage
- **budget.on_budget** - `stop` (checkpoint and exit), `wait` (poll and retry), `ask` (prompt user)
- **budget.check_interval** - How often to poll when in `wait` mode
- **budget.max_wait** - Give up waiting after this duration
- **jobs[].session** - Jobs with the same value form a chain (sequential, shared context). Omit for standalone jobs.
- **jobs[].task** - Literal prompt or a task shortcut name from settings.json
- **jobs[].tool** - `claude`, `codex`, or `gemini` (defaults to `claude`)
- **jobs[].model** - Override default model for this job
- **jobs[].effort** - Codex effort level override
- **jobs[].max_budget** - Claude per-job budget override
- **jobs[].dir** - Working directory for this job

## Scheduler & Session Groups

The scheduler builds a DAG from the flat job list:

```
Input:                          Scheduled as:

chain-1: [audit-A, fix-A]      --- chain-1: audit-A -> fix-A ------+
chain-2: [audit-B]             --- chain-2: audit-B ---------------+
(no session): [update-deps-C]  --- standalone: update-deps-C ------+ -> all done
(no session): [lint-D]         --- standalone: lint-D --------------+
```

Rules:
- Session groups are ordered internally - jobs run sequentially, each inheriting the session ID from the previous job's result
- Between groups - fully parallel up to concurrency limit
- Standalone jobs (no session) are each their own group of one
- Concurrency slots are per-group, not per-job - a chain occupies one slot for its entire duration
- Budget check happens at group boundaries (between session groups completing and new ones starting), not mid-chain

## Execution Modes

### Standalone Mode (default)
Uses library-level execution via `pkg/runner`. In-process, fast, direct access to `RunResult` and streaming events.

### Delegate Mode (`--server`)
Submits jobs to rserve via gRPC using existing `RunTask` RPC. Process isolation, dashboard integration.

Both modes implement the same executor interface. The scheduler doesn't care which backend is used.

## Spool Directory

```
jobs/
  pending/      # Drop job files here (single-job or mini-manifests)
  running/      # rbatch moves jobs here while executing
  done/         # Completed successfully
  failed/       # Failed jobs (with error info appended)
```

- **One-shot:** `rbatch spool ./jobs/` - scans pending/, executes all, exits
- **Watch:** `rbatch watch ./jobs/` - monitors pending/ continuously via fsnotify
- **File format:** Each file is a single job JSON or a mini-manifest
- **Atomic pickup:** Move to running/ before execution prevents double-processing
- **Session chains:** Files sharing a session name are grouped and ordered by filename (e.g., `01-audit.json`, `02-fix.json`)

## Checkpoint & Resume

State file written to `~/.rcodegen/batches/<batch-name>/state.json`:

```json
{
  "batch": "nightly-audit",
  "checkpoint_at": "2026-03-07T14:32:00Z",
  "reason": "budget_threshold",
  "completed": [
    {"name": "audit-projectA", "cost": 0.12, "duration": "45s", "session_id": "sess_abc123"},
    {"name": "audit-projectB", "cost": 0.08, "duration": "30s"}
  ],
  "pending": [
    {"name": "fix-projectA", "session": "chain-1", "session_id": "sess_abc123"},
    {"name": "update-deps-C"},
    {"name": "lint-projectD"}
  ],
  "total_cost": 0.20
}
```

- Session IDs are preserved so chains can resume mid-sequence
- Checkpoint written on every job completion (not just on stop) for crash recovery
- Jobs in running state at crash time get retried on resume
- Resume: `rbatch resume` (latest) or `rbatch resume batch-state.json` (specific)

## CLI Interface

```bash
# Run a manifest
rbatch run jobs.json
rbatch run jobs.json --server          # delegate to rserve
rbatch run jobs.json --concurrency 8   # override manifest concurrency

# Spool directory
rbatch spool ./jobs/                   # one-shot scan
rbatch watch ./jobs/                   # continuous monitoring

# Resume from checkpoint
rbatch resume                          # resumes latest batch
rbatch resume batch-state.json         # resumes specific checkpoint

# Status
rbatch status                          # show active/recent batch status
rbatch status nightly-audit            # specific batch

# Budget flags (override manifest)
rbatch run jobs.json --threshold 5     # stop at 5% remaining
rbatch run jobs.json --on-budget wait  # wait instead of stop
rbatch run jobs.json --max-wait 2h     # give up waiting after 2h

# Common flags
rbatch run jobs.json --dry-run         # show execution plan without running
rbatch run jobs.json -v                # verbose logging
```

## Reporting

### Live Terminal Display

```
rbatch: nightly-audit  [======..............] 12/50 jobs | 4 active | $1.24 | 12m elapsed

  chain-1:  * fix-projectA      (claude/opus)     running 2m...
  chain-2:  + audit-projectB    (claude/sonnet)    $0.08  30s
  worker-3: * update-deps-C     (codex/gpt-5.3)   running 4m...
  worker-4: * lint-projectD     (gemini/2.5-pro)   running 1m...

  completed: 12  failed: 0  pending: 38  budget: 84% remaining
```

### Per-Job Results

Written to `~/.rcodegen/batches/<batch-name>/results/<job-name>.json` as each job completes.

### Batch Summary

Written to `~/.rcodegen/batches/<batch-name>/summary.json` at batch completion:

```json
{
  "name": "nightly-audit",
  "status": "completed",
  "jobs_total": 50,
  "jobs_succeeded": 48,
  "jobs_failed": 2,
  "total_cost": 4.82,
  "total_duration": "34m12s",
  "by_tool": {
    "claude": {"jobs": 30, "cost": 3.20},
    "codex": {"jobs": 15, "cost": 1.40},
    "gemini": {"jobs": 5, "cost": 0.22}
  },
  "failed_jobs": ["update-deps-X", "lint-projectQ"]
}
```

## Package Layout

```
cmd/rbatch/main.go              # CLI entrypoint, flag parsing, subcommands

pkg/batch/
  manifest.go                   # Manifest/job parsing and validation
  scheduler.go                  # Session group building, DAG, concurrency control
  queue.go                      # Job queue with state transitions
  executor_local.go             # Local execution via pkg/runner
  executor_remote.go            # Remote execution via gRPC to rserve
  checkpoint.go                 # Save/resume state
  spool.go                      # Spool directory scan + fsnotify watcher
  budget.go                     # Status-only credit checks, threshold logic
  reporter.go                   # Live display, per-job results, summary
  batch_test.go                 # Tests
```

### Key Design Decisions

- `executor_local.go` and `executor_remote.go` implement the same interface - scheduler is backend-agnostic
- `queue.go` is the core state machine - all job state transitions go through it, making checkpoint/resume straightforward
- No new protobuf needed initially - remote mode uses existing `RunTask` RPC
- `spool.go` uses `fsnotify` for watch mode
- Budget checks reuse existing status-only / TrackStatus infrastructure
- Makefile updated to build `rbatch` alongside existing binaries
