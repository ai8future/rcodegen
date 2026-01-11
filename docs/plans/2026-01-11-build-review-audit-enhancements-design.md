# Build-Review-Audit Enhancements Design

**Date:** 2026-01-11
**Status:** Approved

## Overview

Enhance the `build-review-audit` bundle with:
1. Generic task input (not Quarto-specific)
2. Auto-copy bundle JSON to output directory
3. Machine-readable `final-report.json` alongside `final-report.md`
4. Standardized grading rubric
5. Detailed cost breakdowns

## Bundle Inputs

```json
{
  "inputs": [
    {
      "name": "output_dir",
      "required": false,
      "description": "Base directory (default from settings.default_output_dir)"
    },
    {
      "name": "project_name",
      "required": true,
      "description": "Project subdirectory name"
    },
    {
      "name": "task",
      "required": true,
      "description": "What to build (positional argument)"
    }
  ]
}
```

### CLI Usage

```bash
# Minimal (uses default output_dir from settings)
rcodegen build-review-audit project_name=weather-cli "Build a Python CLI that fetches weather"

# With explicit output_dir
rcodegen build-review-audit project_name=todo-api output_dir=/tmp/builds "Build a Go REST API for todos"
```

## Output Structure

```
{output_dir}/{project_name}/
├── bundle-used.json      # Copy of the bundle that generated this
├── final-report.md       # Human-readable audit report
├── final-report.json     # Machine-readable summary (detailed below)
├── README.md
├── review.md
├── decisions.md
├── test-results.md
├── src/
│   └── ...
└── samples/
    └── ...
```

## final-report.json Schema

```json
{
  "meta": {
    "job_id": "20260111-141630-10d48d46",
    "bundle": "build-review-audit",
    "bundle_source": "pkg/bundle/builtin/build-review-audit.json",
    "timestamp_start": "2026-01-11T14:16:30Z",
    "timestamp_end": "2026-01-11T14:33:15Z",
    "status": "success"
  },
  "summary": {
    "total_cost_usd": 2.4879,
    "duration_seconds": 1005,
    "duration_human": "16m 45s",
    "rcodegen_version": "1.4.0",
    "steps_total": 5,
    "steps_succeeded": 5,
    "steps_failed": 0,
    "models_used": ["claude-sonnet-4-20250514", "gemini-2.5-pro"]
  },
  "costs": {
    "total_usd": 2.4879,
    "by_model": {
      "claude": {
        "cost_usd": 2.3070,
        "input_tokens": 1926,
        "output_tokens": 35360,
        "cache_read_tokens": 3200000,
        "cache_write_tokens": 165000,
        "steps": ["build", "improve", "audit"]
      },
      "gemini": {
        "cost_usd": 0.1809,
        "input_tokens": 358907,
        "output_tokens": 7594,
        "cache_read_tokens": 57666,
        "cache_write_tokens": 5041,
        "steps": ["review", "test"]
      }
    },
    "totals": {
      "input_tokens": 360833,
      "output_tokens": 42954,
      "cache_read_tokens": 3257666,
      "cache_write_tokens": 170041
    }
  },
  "steps": [
    {
      "name": "build",
      "tool": "claude",
      "versions": {
        "tool": "claude-cli/1.0.17",
        "model": "claude-sonnet-4-20250514"
      },
      "status": "success",
      "cost_usd": 0.9377,
      "input_tokens": 20,
      "output_tokens": 19672,
      "cache_read_tokens": 0,
      "cache_write_tokens": 85000,
      "duration_seconds": 180,
      "output_file": "outputs/build.json"
    }
  ],
  "outputs": {
    "directory": "/Users/cliff/Desktop/_code/quarto-build-141605/quarto-pdf-generator",
    "files": [
      {"path": "src/main.py", "type": "source", "size_bytes": 5887, "lines": 194},
      {"path": "README.md", "type": "docs", "size_bytes": 8394},
      {"path": "final-report.md", "type": "report", "size_bytes": 20684}
    ],
    "stats": {
      "total_source_files": 1,
      "total_source_lines": 194,
      "total_doc_words": 8500
    }
  },
  "grade": {
    "score": 94,
    "letter": "A",
    "functionality": 20,
    "code_quality": 18,
    "security": 10,
    "user_experience": 17,
    "architecture": 10,
    "testing": 8,
    "innovation": 5,
    "documentation": 6
  },
  "inputs": {
    "output_dir": "/Users/cliff/Desktop/_code",
    "project_name": "weather-cli",
    "task": "Build a Python CLI that fetches weather"
  }
}
```

## Standardized Grading Rubric

The audit step enforces this rubric (100 points base, bonus allowed):

| Category | Max Points | Description |
|----------|------------|-------------|
| functionality | 20 | Does it work as specified? |
| code_quality | 20 | Clean, readable, documented code |
| security | 10 | No vulnerabilities |
| user_experience | 20 | Intuitive, helpful errors, good UX |
| architecture | 10 | Clean separation, maintainable |
| testing | 10 | Test coverage and verification |
| innovation | 5 | Creative solutions, clever approaches |
| documentation | 5 | README, comments, guides |

**Note:** Bonus points are allowed for exceptional work. Scores can exceed 100.

## Settings Addition

Add to `~/.rcodegen/settings.json`:

```json
{
  "default_output_dir": "/Users/cliff/Desktop/_code"
}
```

## Implementation Changes

### 1. pkg/bundle/builtin/build-review-audit.json
- Make generic (remove Quarto references)
- Add `task` input
- Update audit step with standardized grading rubric

### 2. pkg/settings/settings.go
- Add `DefaultOutputDir` field
- Load from settings.json

### 3. pkg/orchestrator/orchestrator.go
- After successful completion:
  - Copy bundle JSON to `{output_dir}/{project_name}/bundle-used.json`
  - Generate `{output_dir}/{project_name}/final-report.json`
- Add `generateFinalReportJSON()` function
- Add `extractGradeFromAudit()` function to parse JSON block from audit output

### 4. VERSION
- Bump to 1.5.0

## Step-by-Step Workflow

1. User runs: `rcodegen build-review-audit project_name=foo "Build X"`
2. Orchestrator resolves `output_dir` from settings if not provided
3. Steps execute: build → review → improve → test → audit
4. After audit completes:
   - Parse grade JSON from audit stdout
   - Scan output directory for files
   - Write `bundle-used.json`
   - Write `final-report.json`
5. Report success with cost summary

## Testing

Run end-to-end test:
```bash
rcodegen build-review-audit project_name=test-weather "Build a Python script that prints the current weather for a given city using wttr.in"
```

Verify:
- `final-report.json` exists and is valid JSON
- `bundle-used.json` matches source bundle
- Grade is extracted correctly
- Costs are accurate
