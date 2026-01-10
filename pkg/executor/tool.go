package executor

import (
	"bytes"
	"os"
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
	if cfg.Model == "" {
		cfg.Model = tool.DefaultModel()
	}

	// Get working directory
	workDir := ctx.Inputs["codebase"]
	if workDir == "" {
		workDir, _ = os.Getwd()
	}

	// Build and run command
	start := time.Now()
	cmd := tool.BuildCommand(cfg, workDir, task)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	duration := time.Since(start)

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

	return builder.Success().
		WithResult("output_length", stdout.Len()).
		Build(), nil
}
