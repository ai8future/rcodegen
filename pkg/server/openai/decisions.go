// decisions.go implements POST /v1/decisions: typed, calibrated answers from
// TypeSafe's Jev decision model, proxied so the vendor key stays on the server.
package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"rcodegen/pkg/jev"
	"rcodegen/pkg/settings"
)

const maxDecisionBodyBytes = 512 << 10

// DecisionRequest is the POST /v1/decisions body. It mirrors the vendor
// request minus the key.
type DecisionRequest struct {
	State     string                  `json:"state"`
	Questions map[string]jev.Question `json:"questions"`
	Model     string                  `json:"model,omitempty"`
}

// DecisionResponse is the POST /v1/decisions body on success.
type DecisionResponse struct {
	Object        string                `json:"object"`
	Model         string                `json:"model"`
	Answers       map[string]jev.Answer `json:"answers"`
	Usage         *Usage                `json:"usage,omitempty"`
	CostUSD       float64               `json:"cost_usd"`
	StateBytes    int                   `json:"state_bytes"`
	LatencyMs     int64                 `json:"latency_ms"`
	CorrelationID string                `json:"correlation_id,omitempty"`
}

// decisionGate owns the client, the semaphore, and the counters.
type decisionGate struct {
	client *jev.Client
	model  string
	sem    chan struct{}
	sleep  jev.Sleeper

	total  atomic.Int64
	failed atomic.Int64
	mu     sync.Mutex
	cost   float64
}

func newDecisionGate(s *settings.Settings) *decisionGate {
	g := &decisionGate{sleep: jev.Sleep}
	if s == nil {
		g.sem = make(chan struct{}, settings.DefaultJevMaxConcurrent)
		return g
	}
	cfg := s.Defaults.Jev
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = settings.DefaultJevMaxConcurrent
	}
	g.sem = make(chan struct{}, cfg.MaxConcurrent)
	g.model = cfg.Model
	if cfg.ResolveAPIKey() == "" {
		return g
	}
	client, err := jev.NewClient(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "rserve: decisions disabled: %v\n", err)
		return g
	}
	g.client = client
	g.model = client.Model()
	return g
}

func (g *decisionGate) configured() bool {
	return g != nil && g.client != nil
}

func (g *decisionGate) inFlight() int {
	if g == nil {
		return 0
	}
	return len(g.sem)
}

func (g *decisionGate) health() *DecisionHealth {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	cost := g.cost
	g.mu.Unlock()
	return &DecisionHealth{
		Configured:    g.configured(),
		Model:         g.model,
		Total:         g.total.Load(),
		Failed:        g.failed.Load(),
		InFlight:      g.inFlight(),
		MaxConcurrent: cap(g.sem),
		CostUSD:       cost,
	}
}

func (g *decisionGate) record(cost float64, failed bool) {
	g.total.Add(1)
	if failed {
		g.failed.Add(1)
		return
	}
	g.mu.Lock()
	g.cost += cost
	g.mu.Unlock()
}

// handleDecisions serves POST /v1/decisions.
func (h *Handler) handleDecisions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, NewErrorResponse(
			"method not allowed", "invalid_request_error", codeMethodNotAllowed,
		))
		return
	}
	g := h.decisions
	if !g.configured() {
		writeJSON(w, http.StatusServiceUnavailable, NewErrorResponse(
			"decisions are not configured: set TYPESAFE_API_KEY or defaults.jev.api_key", "server_error", codeDecisionsUnconfigured,
		))
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxDecisionBodyBytes)
	var req DecisionRequest
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&req); err != nil {
		writeDecisionBodyError(w, err)
		return
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		if err != nil {
			writeDecisionBodyError(w, err)
			return
		}
		writeJSON(w, http.StatusBadRequest, NewErrorResponse(
			"request body must contain exactly one JSON object", "invalid_request_error", codeInvalidJSON,
		))
		return
	}
	if strings.TrimSpace(req.State) == "" {
		writeJSON(w, http.StatusBadRequest, NewErrorResponse(
			"state must not be empty", "invalid_request_error", codeInvalidDecision,
		))
		return
	}
	if err := jev.ValidateQuestions(req.Questions); err != nil {
		writeJSON(w, http.StatusBadRequest, NewErrorResponse(
			h.redactDecisionSecret(err.Error()), "invalid_request_error", codeInvalidDecision,
		))
		return
	}
	if req.Model != "" && req.Model != g.model && !strings.HasPrefix(req.Model, "jev-") {
		writeJSON(w, http.StatusBadRequest, NewErrorResponse(
			h.redactDecisionSecret(fmt.Sprintf("model %q is not a Jev model; use %q or a jev-* identifier", req.Model, g.model)),
			"invalid_request_error", codeInvalidModel,
		))
		return
	}

	home, username := jev.LocalIdentity()
	state := jev.ScrubState(req.State, home, username)
	if budget := jev.StateBudget(req.Questions); len(state) > budget {
		writeJSON(w, http.StatusRequestEntityTooLarge, NewErrorResponse(
			fmt.Sprintf("state is %d bytes; the limit for these questions is %d", len(state), budget),
			"invalid_request_error", codeStateTooLarge,
		))
		return
	}

	select {
	case g.sem <- struct{}{}:
		defer func() { <-g.sem }()
	default:
		writeJSON(w, http.StatusServiceUnavailable, NewErrorResponse(
			fmt.Sprintf("all %d decision slots are busy", cap(g.sem)), "server_error", codeDecisionCapacity,
		))
		return
	}

	start := time.Now()
	resp, err := g.decide(r.Context(), req.Model, state, req.Questions)
	latency := time.Since(start)
	if err != nil {
		g.record(0, true)
		h.writeDecisionError(w, err)
		return
	}
	cost := jev.CostUSD(resp.Usage.InputTokens)
	g.record(cost, false)
	writeJSON(w, http.StatusOK, DecisionResponse{
		Object:  "decision",
		Model:   resp.Model,
		Answers: resp.Answers,
		Usage: &Usage{
			PromptTokens:     resp.Usage.InputTokens,
			CompletionTokens: resp.Usage.OutputTokens,
			TotalTokens:      resp.Usage.InputTokens + resp.Usage.OutputTokens,
		},
		CostUSD:       cost,
		StateBytes:    len(state),
		LatencyMs:     latency.Milliseconds(),
		CorrelationID: correlationID(r),
	})
}

func writeDecisionBodyError(w http.ResponseWriter, err error) {
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		writeJSON(w, http.StatusRequestEntityTooLarge, NewErrorResponse(
			fmt.Sprintf("request body exceeds %d bytes", maxDecisionBodyBytes), "invalid_request_error", codeStateTooLarge,
		))
		return
	}
	writeJSON(w, http.StatusBadRequest, NewErrorResponse(
		"invalid JSON body: "+err.Error(), "invalid_request_error", codeInvalidJSON,
	))
}

func (g *decisionGate) decide(ctx context.Context, model, state string, qs map[string]jev.Question) (*jev.Response, error) {
	return g.client.DecideWithSleep(ctx, model, state, qs, g.sleep)
}

func (h *Handler) writeDecisionError(w http.ResponseWriter, err error) {
	var apiErr *jev.APIError
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		writeJSON(w, http.StatusServiceUnavailable, NewErrorResponse(
			"decision cancelled or timed out", "server_error", codeDecisionUpstream,
		))
	case errors.As(err, &apiErr) && (apiErr.Status == http.StatusUnauthorized || apiErr.Status == http.StatusForbidden):
		writeJSON(w, http.StatusBadGateway, NewErrorResponse(
			"the vendor rejected rserve's API key", "server_error", codeDecisionUpstreamAuth,
		))
	case errors.As(err, &apiErr) && apiErr.Status == http.StatusUnprocessableEntity:
		writeJSON(w, http.StatusBadRequest, NewErrorResponse(
			"the vendor rejected the request: "+h.redactDecisionSecret(apiErr.Message), "invalid_request_error", codeInvalidDecision,
		))
	case errors.Is(err, jev.ErrInvalidResponse):
		writeJSON(w, http.StatusBadGateway, NewErrorResponse(
			"the vendor returned an unusable reply: "+err.Error(), "server_error", codeDecisionUpstreamInvalid,
		))
	case errors.As(err, &apiErr) && !apiErr.Retryable:
		writeJSON(w, http.StatusBadGateway, NewErrorResponse(
			"vendor rejected the request: "+h.redactDecisionSecret(apiErr.Message), "server_error", codeDecisionUpstreamRejected,
		))
	case errors.As(err, &apiErr):
		writeJSON(w, http.StatusServiceUnavailable, NewErrorResponse(
			"vendor call failed: "+h.redactDecisionSecret(apiErr.Message), "server_error", codeDecisionUpstream,
		))
	default:
		writeJSON(w, http.StatusBadRequest, NewErrorResponse(
			h.redactDecisionSecret(err.Error()), "invalid_request_error", codeInvalidDecision,
		))
	}
}

func (h *Handler) redactDecisionSecret(msg string) string {
	keys := []string{os.Getenv("TYPESAFE_API_KEY")}
	if h != nil && h.settings != nil {
		keys = append(keys, h.settings.Defaults.Jev.ResolveAPIKey())
	}
	for _, key := range keys {
		if key != "" {
			msg = strings.ReplaceAll(msg, key, "[redacted]")
		}
	}
	return msg
}
