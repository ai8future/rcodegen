package executor

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"sync"
	"time"

	"rcodegen/pkg/bundle"
	"rcodegen/pkg/envelope"
	"rcodegen/pkg/jev"
	"rcodegen/pkg/orchestrator"
	"rcodegen/pkg/settings"
	"rcodegen/pkg/workspace"
)

// DecideExecutor runs Jev decision steps and fails closed on unusable output.
type DecideExecutor struct {
	mu        sync.Mutex
	settings  *settings.Settings
	client    *jev.Client
	clientErr error
	built     bool
}

// SetSettings receives the orchestrator settings through runner.SettingsAware.
func (e *DecideExecutor) SetSettings(s *settings.Settings) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.settings = s
	e.client = nil
	e.clientErr = nil
	e.built = false
}

func (e *DecideExecutor) getClient() (*jev.Client, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.built {
		return e.client, e.clientErr
	}
	e.built = true
	if e.settings == nil {
		e.clientErr = jev.ErrNotConfigured
		return nil, e.clientErr
	}
	e.client, e.clientErr = jev.NewClient(e.settings.Defaults.Jev)
	return e.client, e.clientErr
}

func (e *DecideExecutor) Execute(step *bundle.Step, ctx *orchestrator.Context, ws *workspace.Workspace) (*envelope.Envelope, error) {
	builder := envelope.New().WithTool("jev")
	client, err := e.getClient()
	if err != nil {
		if errors.Is(err, jev.ErrNotConfigured) {
			return builder.Failure("JEV_UNCONFIGURED", err.Error()).Build(), nil
		}
		return builder.Failure("JEV_CONFIG_ERROR", err.Error()).Build(), nil
	}

	home, username := jev.LocalIdentity()
	state := jev.ScrubState(ctx.Resolve(step.Decide.State), home, username)
	state, truncated := jev.CapState(state, jev.StateBudget(step.Decide.Questions))

	start := time.Now()
	resp, err := client.Decide(ctx.Ctx(), step.Decide.Model, state, step.Decide.Questions)
	duration := time.Since(start)
	builder = builder.WithDuration(duration.Milliseconds())
	if err != nil {
		if runCtx := ctx.Ctx(); runCtx != nil && runCtx.Err() != nil {
			return builder.Failure("CANCELLED", "decision cancelled: "+err.Error()).Build(), runCtx.Err()
		}
		if errors.Is(err, jev.ErrInvalidResponse) {
			return builder.Failure("JEV_INVALID_RESPONSE", err.Error()).Build(), nil
		}
		return builder.Failure("JEV_ERROR", err.Error()).Build(), nil
	}

	names := make([]string, 0, len(step.Decide.Questions))
	for name := range step.Decide.Questions {
		names = append(names, name)
	}
	sort.Strings(names)
	values := make(map[string]string, len(names))
	for _, name := range names {
		values[name] = resp.Answers[name].Value()
	}
	stdoutJSON, _ := json.Marshal(map[string]interface{}{"answers": values})

	cost := jev.CostUSD(resp.Usage.InputTokens)
	sum := sha256.Sum256([]byte(state))
	outputPath, err := ws.WriteOutput(step.Name, map[string]interface{}{
		"stdout":          string(stdoutJSON),
		"model":           resp.Model,
		"answers":         resp.Answers,
		"usage":           resp.Usage,
		"state_bytes":     len(state),
		"state_sha256":    hex.EncodeToString(sum[:]),
		"state_truncated": truncated,
		"latency_ms":      duration.Milliseconds(),
	})
	if err != nil {
		return builder.Failure("OUTPUT_WRITE_FAILED", "write decision output: "+err.Error()).
			WithResult("model", resp.Model).
			WithResult("cost_usd", cost).
			WithResult("input_tokens", resp.Usage.InputTokens).
			WithResult("output_tokens", resp.Usage.OutputTokens).
			WithResult("state_truncated", truncated).
			Build(), nil
	}

	builder = builder.Success().
		WithOutputRef(outputPath).
		WithResult("stdout", string(stdoutJSON)).
		WithResult("model", resp.Model).
		WithResult("cost_usd", cost).
		WithResult("input_tokens", resp.Usage.InputTokens).
		WithResult("output_tokens", resp.Usage.OutputTokens).
		WithResult("state_truncated", truncated)
	for _, name := range names {
		ans := resp.Answers[name]
		builder = builder.WithResult(name, values[name])
		if ans.Confidence != nil {
			builder = builder.WithResult(name+"_confidence", strconv.FormatFloat(*ans.Confidence, 'f', 4, 64))
		}
		if len(ans.Probabilities) > 0 {
			builder = builder.WithResult(name+"_probabilities", ans.Probabilities)
		}
	}
	return builder.Build(), nil
}
