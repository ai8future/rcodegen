// Package executor provides the execution engine for running AI tool
// commands (Claude, Codex, Gemini) with streaming output and token tracking.
package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"rcodegen/pkg/bundle"
	"rcodegen/pkg/envelope"
	"rcodegen/pkg/orchestrator"
	"rcodegen/pkg/runner"
	"rcodegen/pkg/workspace"
)

type ToolExecutor struct {
	Tools map[string]runner.Tool
}

func (e *ToolExecutor) Execute(step *bundle.Step, ctx *orchestrator.Context, ws *workspace.Workspace) (*envelope.Envelope, error) {
	tool, ok := e.Tools[step.Tool]
	if !ok {
		return envelope.New().Failure("TOOL_NOT_FOUND", "Unknown tool: "+step.Tool).Build(), nil
	}

	// Resolve task template
	task := ctx.Resolve(step.Task)

	// Build config
	cfg := &runner.Config{
		Task: task,
	}
	workDir := ctx.Inputs["codebase"]
	if workDir == "" {
		workDir, _ = os.Getwd()
	}
	if workDir != "" {
		cfg.WorkDirs = []string{workDir}
	}

	// Apply tool-specific defaults (sets MaxBudget, etc.)
	tool.ApplyToolDefaults(cfg)

	// Override model if specified in step. A "-{effort}" suffix on the model
	// (e.g. "opus-max", "gpt-5.6-luna-high") sets the step's effort level.
	if step.Model != "" {
		base, effort := runner.SplitModelEffort(tool, step.Model)
		cfg.Model = base
		if effort != "" {
			if step.Effort != "" && step.Effort != effort {
				return envelope.New().Failure("INVALID_CONFIG", "conflicting model suffix and explicit effort").Build(), nil
			}
			cfg.Effort = effort
		}
	} else if cfg.Model == "" {
		cfg.Model = tool.DefaultModel()
	}
	if step.Effort != "" {
		cfg.Effort = step.Effort
	}
	if err := runner.ValidateModel(tool, cfg.Model); err != nil {
		return envelope.New().Failure("INVALID_CONFIG", err.Error()).Build(), nil
	}
	if err := runner.ValidateEffort(tool, cfg.Model, cfg.Effort); err != nil {
		return envelope.New().Failure("INVALID_CONFIG", err.Error()).Build(), nil
	}
	if err := tool.ValidateConfig(cfg); err != nil {
		return envelope.New().Failure("INVALID_CONFIG", err.Error()).Build(), nil
	}

	// Reuse session if available
	if sessionID := ctx.GetToolSession(step.Tool); sessionID != "" {
		cfg.SessionID = sessionID
	}

	// Create log file for real-time output
	logDir := filepath.Join(ws.JobDir, "logs")
	os.MkdirAll(logDir, 0755)
	logPath := filepath.Join(logDir, step.Name+".log")
	logFile, logErr := os.Create(logPath)

	stdout := runner.NewBoundedBuffer(64 << 10)
	stderr := runner.NewBoundedBuffer(64 << 10)
	if logErr == nil {
		// Write to both buffer and log file simultaneously
		cfg.Output = io.MultiWriter(stdout, logFile)
		cfg.Stderr = io.MultiWriter(stderr, logFile)
		defer logFile.Close()
	} else {
		// Fallback to buffer only
		cfg.Output = stdout
		cfg.Stderr = stderr
	}

	start := time.Now()
	var runResult *runner.RunResult
	var err error
	if direct, ok := tool.(runner.DirectAPIRunner); ok && direct.ShouldUseDirectAPI(cfg) {
		runResult = runner.NewRunner(tool).RunWithContext(ctx.Ctx(), cfg)
		if runResult.Error != nil {
			err = runResult.Error
		} else if runResult.ExitCode != 0 {
			err = fmt.Errorf("tool exited with code %d", runResult.ExitCode)
		}
	} else {
		cmd := tool.BuildCommand(cfg, workDir, task)
		cmd.Stdout = cfg.Output
		cmd.Stderr = cfg.Stderr
		err = runWithContext(ctx.Ctx(), cmd)
	}
	duration := time.Since(start)

	// Extract and store session ID for future reuse
	if runResult != nil && runResult.SessionID != "" {
		ctx.SetToolSession(step.Tool, runResult.SessionID)
	} else if sessionID := extractSessionID(step.Tool, stdout.String(), stderr.String()); sessionID != "" {
		ctx.SetToolSession(step.Tool, sessionID)
	}

	// Write output
	outputPath, _ := ws.WriteOutput(step.Name, map[string]interface{}{
		"stdout":           stdout.String(),
		"stderr":           stderr.String(),
		"output_truncated": stdout.Truncated() || stderr.Truncated(),
	})

	// Build envelope
	builder := envelope.New().
		WithTool(step.Tool).
		WithOutputRef(outputPath).
		WithDuration(duration.Milliseconds())

	if err != nil {
		// Distinguish cancellation (client disconnect, CancelRun, Ctrl+C) from
		// genuine failures; the non-nil error stops the orchestrator loop.
		if runCtx := ctx.Ctx(); runCtx != nil && runCtx.Err() != nil {
			return builder.Failure("CANCELLED", "execution cancelled: "+err.Error()).Build(), runCtx.Err()
		}
		return builder.Failure("EXEC_FAILED", err.Error()).Build(), nil
	}

	// Extract cost/token info
	usage := extractCostInfo(step.Tool, stdout.String(), stderr.String())
	if runResult != nil {
		if reporter, ok := tool.(runner.UsageReporter); ok {
			if reported, ok := reporter.ReportedUsage(runResult); ok {
				usage.InputTokens = reported.InputTokens
				usage.OutputTokens = reported.OutputTokens
				usage.CostUSD = reported.CostUSD
			}
		}
	}

	return builder.Success().
		WithResult("output_length", stdout.Len()).
		WithResult("output_truncated", stdout.Truncated() || stderr.Truncated()).
		WithResult("cost_usd", usage.CostUSD).
		WithResult("input_tokens", usage.InputTokens).
		WithResult("output_tokens", usage.OutputTokens).
		WithResult("cache_read_tokens", usage.CacheReadTokens).
		WithResult("cache_write_tokens", usage.CacheWriteTokens).
		WithResult("model", cfg.Model).
		Build(), nil
}

// runWithContext runs cmd to completion, killing the process if runCtx is
// cancelled (client disconnect, CancelRun, or Ctrl+C). Only the direct child
// is killed; processes it spawned may survive.
func runWithContext(runCtx context.Context, cmd *exec.Cmd) error {
	if runCtx == nil {
		return cmd.Run()
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-runCtx.Done():
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		<-done // reap the process; Wait's error is superseded by cancellation
		return runCtx.Err()
	}
}

// UsageInfo holds token and cost information
type UsageInfo struct {
	CostUSD          float64
	InputTokens      int
	OutputTokens     int
	CacheReadTokens  int
	CacheWriteTokens int
}

// extractCostInfo extracts cost and token information from tool output
func extractCostInfo(toolName, stdout, stderr string) UsageInfo {
	usage := UsageInfo{}

	switch toolName {
	case "claude":
		// Claude outputs streaming JSON with detailed usage in the result object
		lines := strings.Split(stdout, "\n")
		for i := len(lines) - 1; i >= 0; i-- {
			line := strings.TrimSpace(lines[i])
			if line == "" {
				continue
			}
			var obj map[string]interface{}
			if err := json.Unmarshal([]byte(line), &obj); err != nil {
				continue
			}
			if objType, _ := obj["type"].(string); objType == "result" {
				usage.CostUSD, _ = obj["total_cost_usd"].(float64)
				if u, ok := obj["usage"].(map[string]interface{}); ok {
					if v, ok := u["input_tokens"].(float64); ok {
						usage.InputTokens = int(v)
					}
					if v, ok := u["output_tokens"].(float64); ok {
						usage.OutputTokens = int(v)
					}
					if v, ok := u["cache_read_input_tokens"].(float64); ok {
						usage.CacheReadTokens = int(v)
					}
					if v, ok := u["cache_creation_input_tokens"].(float64); ok {
						usage.CacheWriteTokens = int(v)
					}
				}
				return usage
			}
		}
	case "codex":
		// Codex outputs "tokens used\n7,476\n" in stderr
		re := regexp.MustCompile(`tokens used\s*\n\s*([\d,]+)`)
		if matches := re.FindStringSubmatch(stderr); len(matches) > 1 {
			tokenStr := strings.ReplaceAll(matches[1], ",", "")
			tokens, _ := strconv.Atoi(tokenStr)
			// Codex doesn't break down input/output, estimate 70% input, 30% output
			usage.InputTokens = tokens * 7 / 10
			usage.OutputTokens = tokens * 3 / 10
			// Estimate cost: GPT-5.3 Codex pricing
			// Input: $0.01/1K, Output: $0.03/1K (rough estimates)
			usage.CostUSD = float64(usage.InputTokens)*0.00001 + float64(usage.OutputTokens)*0.00003
		}
	case "gemini":
		// Gemini outputs JSON with token breakdown in stats
		lines := strings.Split(stdout, "\n")
		for i := len(lines) - 1; i >= 0; i-- {
			line := strings.TrimSpace(lines[i])
			if line == "" {
				continue
			}
			var obj map[string]interface{}
			if err := json.Unmarshal([]byte(line), &obj); err != nil {
				continue
			}
			if objType, _ := obj["type"].(string); objType == "result" {
				if stats, ok := obj["stats"].(map[string]interface{}); ok {
					if v, ok := stats["input_tokens"].(float64); ok {
						usage.InputTokens = int(v)
					}
					if v, ok := stats["output_tokens"].(float64); ok {
						usage.OutputTokens = int(v)
					}
					if v, ok := stats["cached"].(float64); ok {
						usage.CacheReadTokens = int(v)
					}
					// Gemini 3 pricing (estimates)
					// Input: $0.0005/1K, Output: $0.0015/1K
					usage.CostUSD = float64(usage.InputTokens)*0.0000005 + float64(usage.OutputTokens)*0.0000015
				}
				return usage
			}
		}
	case "opencode":
		// opencode emits JSON events via `--format json`, but rcodegen does
		// not parse that stream yet. Return zero usage explicitly for v1.
		return UsageInfo{}
	case "kilocode":
		// kilocode emits JSON events via `--format json`, but rcodegen does
		// not parse that stream yet. Return zero usage explicitly for v1.
		return UsageInfo{}
	}
	return usage
}

// extractSessionID extracts the session ID from tool output for session reuse
func extractSessionID(toolName, stdout, stderr string) string {
	switch toolName {
	case "claude", "gemini":
		// Claude and Gemini output streaming JSON with session_id in init message
		lines := strings.Split(stdout, "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var obj map[string]interface{}
			if err := json.Unmarshal([]byte(line), &obj); err != nil {
				continue
			}
			if objType, _ := obj["type"].(string); objType == "system" || objType == "init" {
				if sessionID, ok := obj["session_id"].(string); ok {
					return sessionID
				}
			}
		}
	case "codex":
		// Codex outputs "session id: <uuid>" in stderr
		re := regexp.MustCompile(`session id: ([0-9a-f-]+)`)
		if matches := re.FindStringSubmatch(stderr); len(matches) > 1 {
			return matches[1]
		}
	case "opencode":
		// Session IDs are present in opencode JSON events, but parsing is not
		// implemented yet, so automatic chaining is disabled for v1.
		return ""
	case "kilocode":
		// Session IDs are present in kilocode JSON events, but parsing is not
		// implemented yet, so automatic chaining is disabled for v1.
		return ""
	}
	return ""
}
