package executor

import (
	"sync"

	"rcodegen/pkg/bundle"
	"rcodegen/pkg/envelope"
	"rcodegen/pkg/orchestrator"
	"rcodegen/pkg/workspace"
)

type ParallelExecutor struct {
	Dispatcher *Dispatcher
}

func (e *ParallelExecutor) Execute(step *bundle.Step, ctx *orchestrator.Context, ws *workspace.Workspace) (*envelope.Envelope, error) {
	var wg sync.WaitGroup
	results := make(map[string]*envelope.Envelope)
	var mu sync.Mutex
	var firstErr error

	for _, substep := range step.Parallel {
		wg.Add(1)
		go func(s bundle.Step) {
			defer wg.Done()
			env, err := e.Dispatcher.Execute(&s, ctx, ws)
			mu.Lock()
			defer mu.Unlock()
			if err != nil && firstErr == nil {
				firstErr = err
			}
			results[s.Name] = env
			ctx.SetResult(s.Name, env) // Make available to later steps
		}(substep)
	}

	wg.Wait()

	// Build aggregate result
	allSuccess := true
	for _, env := range results {
		if env.Status != envelope.StatusSuccess {
			allSuccess = false
			break
		}
	}

	status := envelope.StatusSuccess
	if !allSuccess {
		status = envelope.StatusPartial
	}

	return &envelope.Envelope{
		Status: status,
		Result: map[string]interface{}{
			"steps":     len(results),
			"completed": len(results),
		},
	}, firstErr
}
