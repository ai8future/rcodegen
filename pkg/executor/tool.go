// Package executor provides the execution engine for running AI tool
// commands (Claude, Codex, Gemini) with streaming output and token tracking.
package executor

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
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
		Task:  task,
		Model: step.Model,
	}

	// Apply tool-specific defaults (sets MaxBudget, etc.)
	tool.ApplyToolDefaults(cfg)

	// Override model if specified in step
	if step.Model != "" {
		cfg.Model = step.Model
	} else if cfg.Model == "" {
		cfg.Model = tool.DefaultModel()
	}

	// Reuse session if available
	if sessionID := ctx.GetToolSession(step.Tool); sessionID != "" {
		cfg.SessionID = sessionID
	}

	// Get working directory
	workDir := ctx.Inputs["codebase"]
	if workDir == "" {
		workDir, _ = os.Getwd()
	}

	// Build and run command
	start := time.Now()
	cmd := tool.BuildCommand(cfg, workDir, task)

	// Create log file for real-time output
	logPath := filepath.Join(ws.JobDir, "logs", step.Name+".log")
	logFile, logErr := os.Create(logPath)

	var stdout, stderr bytes.Buffer
	if logErr == nil {
		// Write to both buffer and log file simultaneously
		cmd.Stdout = io.MultiWriter(&stdout, logFile)
		cmd.Stderr = io.MultiWriter(&stderr, logFile)
		defer logFile.Close()
	} else {
		// Fallback to buffer only
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
	}

	err := cmd.Run()
	duration := time.Since(start)

	// Extract and store session ID for future reuse
	if sessionID := extractSessionID(step.Tool, stdout.String(), stderr.String()); sessionID != "" {
		ctx.SetToolSession(step.Tool, sessionID)
	}

	// Write output
	outputPath, _ := ws.WriteOutput(step.Name, map[string]interface{}{
		"stdout": stdout.String(),
		"stderr": stderr.String(),
	})

	// Build envelope
	builder := envelope.New().
		WithTool(step.Tool).
		WithOutputRef(outputPath).
		WithDuration(duration.Milliseconds())

	if err != nil {
		return builder.Failure("EXEC_FAILED", err.Error()).Build(), nil
	}

	// Extract cost/token info
	usage := extractCostInfo(step.Tool, stdout.String(), stderr.String())

	return builder.Success().
		WithResult("output_length", stdout.Len()).
		WithResult("cost_usd", usage.CostUSD).
		WithResult("input_tokens", usage.InputTokens).
		WithResult("output_tokens", usage.OutputTokens).
		WithResult("cache_read_tokens", usage.CacheReadTokens).
		WithResult("cache_write_tokens", usage.CacheWriteTokens).
		Build(), nil
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
			// Estimate cost: GPT-5.2 Codex pricing
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
	}
	return ""
}
