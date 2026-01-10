package orchestrator

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"rcodegen/pkg/bundle"
	"rcodegen/pkg/envelope"
	"rcodegen/pkg/runner"
	"rcodegen/pkg/settings"
	"rcodegen/pkg/tools/claude"
	"rcodegen/pkg/tools/codex"
	"rcodegen/pkg/tools/gemini"
	"rcodegen/pkg/workspace"
)

// StepExecutor is the interface for executing steps.
// This allows the orchestrator to use a dispatcher without circular imports.
type StepExecutor interface {
	Execute(step *bundle.Step, ctx *Context, ws *workspace.Workspace) (*envelope.Envelope, error)
}

// DispatcherFactory creates a dispatcher from a tool registry.
// This is set by the executor package to break the circular dependency.
var DispatcherFactory func(tools map[string]runner.Tool) StepExecutor

type Orchestrator struct {
	settings   *settings.Settings
	dispatcher StepExecutor
}

func New(s *settings.Settings) *Orchestrator {
	// Build tool registry
	tools := map[string]runner.Tool{
		"claude": claude.New(),
		"codex":  codex.New(),
		"gemini": gemini.New(),
	}

	var dispatcher StepExecutor
	if DispatcherFactory != nil {
		dispatcher = DispatcherFactory(tools)
	}

	return &Orchestrator{
		settings:   s,
		dispatcher: dispatcher,
	}
}

func (o *Orchestrator) Run(b *bundle.Bundle, inputs map[string]string) (*envelope.Envelope, error) {
	start := time.Now()

	// Validate required inputs
	for _, input := range b.Inputs {
		if input.Required {
			if _, ok := inputs[input.Name]; !ok {
				if input.Default != "" {
					inputs[input.Name] = input.Default
				} else {
					return envelope.New().
						Failure("MISSING_INPUT", "Required input: "+input.Name).
						Build(), nil
				}
			}
		}
	}

	// Create workspace
	wsDir := filepath.Join(os.Getenv("HOME"), ".rcodegen", "workspace")
	ws, err := workspace.New(wsDir)
	if err != nil {
		return envelope.New().Failure("WORKSPACE_ERROR", err.Error()).Build(), err
	}

	fmt.Printf("Job ID: %s\n", ws.JobID)
	fmt.Printf("Bundle: %s\n\n", b.Name)

	// Create context
	ctx := NewContext(inputs)

	// Execute steps
	for i, step := range b.Steps {
		fmt.Printf("[%d/%d] %s...\n", i+1, len(b.Steps), step.Name)

		// Check condition
		if step.If != "" && !EvaluateCondition(step.If, ctx) {
			fmt.Printf("  Skipped (condition false)\n")
			ctx.SetResult(step.Name, &envelope.Envelope{Status: envelope.StatusSkipped})
			continue
		}

		// Handle conditional step
		if step.Then != nil {
			if EvaluateCondition(step.If, ctx) {
				env, err := o.dispatcher.Execute(step.Then, ctx, ws)
				ctx.SetResult(step.Name, env)
				if err != nil {
					return env, err
				}
			} else if step.Else != nil {
				env, err := o.dispatcher.Execute(step.Else, ctx, ws)
				ctx.SetResult(step.Name, env)
				if err != nil {
					return env, err
				}
			}
			continue
		}

		// Execute step
		env, err := o.dispatcher.Execute(&step, ctx, ws)
		if err != nil {
			return env, err
		}

		ctx.SetResult(step.Name, env)

		fmt.Printf("  %s\n", env.Status)

		if env.Status == envelope.StatusFailure {
			return env, fmt.Errorf("step %s failed", step.Name)
		}
	}

	duration := time.Since(start)

	fmt.Printf("\nCompleted in %s\n", duration.Round(time.Second))
	fmt.Printf("Output: %s\n", ws.JobDir)

	return envelope.New().
		Success().
		WithResult("steps", len(b.Steps)).
		WithResult("job_id", ws.JobID).
		WithDuration(duration.Milliseconds()).
		Build(), nil
}
