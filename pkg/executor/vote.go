package executor

import (
	"rcodegen/pkg/bundle"
	"rcodegen/pkg/envelope"
	"rcodegen/pkg/orchestrator"
	"rcodegen/pkg/workspace"
)

type VoteExecutor struct{}

func (e *VoteExecutor) Execute(step *bundle.Step, ctx *orchestrator.Context, ws *workspace.Workspace) (*envelope.Envelope, error) {
	// Count votes from input steps
	votes := make(map[string]int)

	for _, inputRef := range step.Vote.Inputs {
		// Extract step name from ${steps.name.output_ref}
		// For now, just count successful steps
		stepName := extractStepName(inputRef)
		if env, ok := ctx.GetResult(stepName); ok && env != nil {
			if env.Status == envelope.StatusSuccess {
				votes["success"]++
			} else {
				votes["failure"]++
			}
		}
	}

	total := votes["success"] + votes["failure"]

	var decision string
	switch step.Vote.Strategy {
	case "majority":
		if votes["success"] > total/2 {
			decision = "approved"
		} else {
			decision = "rejected"
		}
	case "unanimous":
		if votes["failure"] == 0 {
			decision = "approved"
		} else {
			decision = "rejected"
		}
	default:
		decision = "unknown"
	}

	outputPath, _ := ws.WriteOutput(step.Name, map[string]interface{}{
		"votes":    votes,
		"decision": decision,
	})

	return envelope.New().
		Success().
		WithOutputRef(outputPath).
		WithResult("decision", decision).
		WithResult("votes", votes).
		Build(), nil
}

func extractStepName(ref string) string {
	// ${steps.name.output_ref} -> name
	if len(ref) > 9 && ref[:8] == "${steps." {
		end := 8
		for i := 8; i < len(ref); i++ {
			if ref[i] == '.' {
				return ref[8:i]
			}
		}
		return ref[8:end]
	}
	return ref
}
