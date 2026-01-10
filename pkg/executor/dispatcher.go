package executor

import (
	"rcodegen/pkg/bundle"
	"rcodegen/pkg/envelope"
	"rcodegen/pkg/orchestrator"
	"rcodegen/pkg/runner"
	"rcodegen/pkg/workspace"
)

func init() {
	// Register dispatcher factory with orchestrator to break circular dependency
	orchestrator.DispatcherFactory = func(tools map[string]runner.Tool) orchestrator.StepExecutor {
		return NewDispatcher(tools)
	}
}

type Dispatcher struct {
	tool     *ToolExecutor
	parallel *ParallelExecutor
	merge    *MergeExecutor
	vote     *VoteExecutor
}

func NewDispatcher(tools map[string]runner.Tool) *Dispatcher {
	d := &Dispatcher{
		tool:  &ToolExecutor{Tools: tools},
		merge: &MergeExecutor{},
		vote:  &VoteExecutor{},
	}
	d.parallel = &ParallelExecutor{Dispatcher: d}
	d.merge.ToolExecutor = d.tool
	return d
}

func (d *Dispatcher) Execute(step *bundle.Step, ctx *orchestrator.Context, ws *workspace.Workspace) (*envelope.Envelope, error) {
	// Determine step type and dispatch
	switch {
	case len(step.Parallel) > 0:
		return d.parallel.Execute(step, ctx, ws)
	case step.Merge != nil:
		return d.merge.Execute(step, ctx, ws)
	case step.Vote != nil:
		return d.vote.Execute(step, ctx, ws)
	case step.Tool != "":
		return d.tool.Execute(step, ctx, ws)
	default:
		return envelope.New().Failure("UNKNOWN_STEP", "Cannot determine step type").Build(), nil
	}
}
