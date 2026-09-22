package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"rcodegen/pkg/jev"
	"rcodegen/pkg/server"
	"rcodegen/pkg/settings"
)

const decisionOK = `{"model":"jev-1.13.0","answers":{"verdict":{"type":"choice","choice":"incomplete","probabilities":{"complete":0.2,"incomplete":0.8},"confidence":0.6},"gaps":{"type":"noul","noul":0.9}},"usage":{"input_tokens":500,"output_tokens":0}}`

func decisionBody(state string) string {
	return `{"state":` + mustJSON(state) + `,"questions":{` +
		`"verdict":{"type":"choice","instructions":"pick","criteria":{"complete":"c","incomplete":"i"}},` +
		`"gaps":{"type":"noul","instructions":"gaps?"}}}`
}

func mustJSON(v interface{}) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func newDecisionHandler(t *testing.T, upstream http.HandlerFunc, maxConcurrent int, token ...string) (*Handler, *httptest.Server) {
	t.Helper()
	t.Setenv("TYPESAFE_API_KEY", "")
	if len(token) == 0 {
		t.Setenv("RSERVE_TOKEN", "")
	} else {
		t.Setenv("RSERVE_TOKEN", token[0])
	}
	srv := httptest.NewServer(upstream)
	t.Cleanup(srv.Close)
	s := settings.GetDefaultSettings()
	s.Defaults.Jev = settings.JevDefaults{
		BaseURL:        srv.URL,
		Model:          "jev-1.13.0",
		TimeoutSeconds: 2,
		MaxConcurrent:  maxConcurrent,
		APIKey:         "unit-key",
	}
	h := NewHandler(s, nil, server.NewRunRegistry(1), nil, nil, nil)
	return h, srv
}

func postDecision(h *Handler, body string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/decisions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestDecisionsSuccessScrubsStateAndReportsCost(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	var seen string
	var auth string
	h, _ := newDecisionHandler(t, func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		var req struct {
			State string `json:"state"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode upstream request: %v", err)
		}
		seen = req.State
		_, _ = w.Write([]byte(decisionOK))
	}, 4)

	rec := postDecision(h, decisionBody("report at "+home+"/repo/out.md"), map[string]string{"X-Correlation-ID": "job-7"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if auth != "Bearer unit-key" {
		t.Fatal("upstream Authorization header did not carry the fixture key")
	}
	if strings.Contains(seen, home) || !strings.Contains(seen, "~/repo/out.md") {
		t.Fatalf("state not scrubbed: %q", seen)
	}
	var resp DecisionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Object != "decision" || resp.Model != "jev-1.13.0" || resp.CorrelationID != "job-7" {
		t.Fatalf("resp = %+v", resp)
	}
	if resp.Answers["verdict"].Choice != "incomplete" || resp.Answers["gaps"].Noul == nil || *resp.Answers["gaps"].Noul != 0.9 {
		t.Fatalf("answers = %+v", resp.Answers)
	}
	if resp.Usage == nil || resp.Usage.PromptTokens != 500 || resp.CostUSD != jev.CostUSD(500) || resp.CostUSD <= 0 {
		t.Fatalf("usage/cost = %+v %v, want cost %v", resp.Usage, resp.CostUSD, jev.CostUSD(500))
	}
	if rec.Header().Get("X-Correlation-ID") != "job-7" {
		t.Fatal("correlation header not echoed")
	}
	if strings.Contains(rec.Body.String(), "unit-key") {
		t.Fatal("key leaked into response")
	}
}

func TestDecisionsUnconfiguredIs503NotRetryable(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "")
	t.Setenv("RSERVE_TOKEN", "")
	h := NewHandler(settings.GetDefaultSettings(), nil, server.NewRunRegistry(1), nil, nil, nil)
	rec := postDecision(h, decisionBody("s"), nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var er ErrorResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &er)
	if er.Error.Code != codeDecisionsUnconfigured || er.Error.Retryable {
		t.Fatalf("error = %+v", er.Error)
	}
}

func TestDecisionsNilSettingsIsUnconfigured(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "")
	t.Setenv("RSERVE_TOKEN", "")
	h := NewHandler(nil, nil, server.NewRunRegistry(1), nil, nil, nil)
	if rec := postDecision(h, decisionBody("s"), nil); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d", rec.Code)
	}
}

func TestDecisionsRejectsBadRequests(t *testing.T) {
	h, _ := newDecisionHandler(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(decisionOK)) }, 4)
	cases := []struct {
		name string
		body string
		code ErrorCode
		http int
	}{
		{"invalid json", `{"state":`, codeInvalidJSON, http.StatusBadRequest},
		{"trailing garbage", decisionBody("s") + ` trailing`, codeInvalidJSON, http.StatusBadRequest},
		{"two documents", decisionBody("s") + decisionBody("t"), codeInvalidJSON, http.StatusBadRequest},
		{"oversized trailing whitespace", decisionBody("s") + strings.Repeat(" ", maxDecisionBodyBytes), codeStateTooLarge, http.StatusRequestEntityTooLarge},
		{"body over limit", `{"state":"` + strings.Repeat("x", 600*1024) + `","questions":{"q":{"type":"noul","instructions":"x"}}}`, codeStateTooLarge, http.StatusRequestEntityTooLarge},
		{"empty state", `{"state":"","questions":{"q":{"type":"noul","instructions":"x"}}}`, codeInvalidDecision, http.StatusBadRequest},
		{"no questions", `{"state":"s","questions":{}}`, codeInvalidDecision, http.StatusBadRequest},
		{"bad type", `{"state":"s","questions":{"q":{"type":"essay","instructions":"x"}}}`, codeInvalidDecision, http.StatusBadRequest},
		{"foreign model", `{"state":"s","model":"gpt-5","questions":{"q":{"type":"noul","instructions":"x"}}}`, codeInvalidModel, http.StatusBadRequest},
		{"too large", `{"state":"` + strings.Repeat("x", 100*1024) + `","questions":{"q":{"type":"noul","instructions":"x"}}}`, codeStateTooLarge, http.StatusRequestEntityTooLarge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := postDecision(h, tc.body, nil)
			if rec.Code != tc.http {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			var er ErrorResponse
			_ = json.Unmarshal(rec.Body.Bytes(), &er)
			if er.Error.Code != tc.code {
				t.Fatalf("code = %q, want %q", er.Error.Code, tc.code)
			}
		})
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/decisions", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET status %d", rec.Code)
	}
}

func TestDecisionsRedactsSecretFromValidationErrors(t *testing.T) {
	h, _ := newDecisionHandler(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(decisionOK)) }, 4)
	cases := []string{
		`{"state":"s","model":"unit-key","questions":{"q":{"type":"noul","instructions":"x"}}}`,
		`{"state":"s","questions":{"unit-key":{"type":"essay","instructions":"x"}}}`,
	}
	for _, body := range cases {
		rec := postDecision(h, body, nil)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "unit-key") {
			t.Fatalf("key leaked through validation error: %s", rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "[redacted]") {
			t.Fatalf("validation error was not redacted: %s", rec.Body.String())
		}
	}
}

func TestDecisionsUpstreamFailuresMapToRetryability(t *testing.T) {
	t.Run("rate limited", func(t *testing.T) {
		h, _ := newDecisionHandler(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTooManyRequests) }, 4)
		h.decisions.sleep = func(context.Context, time.Duration) error { return nil }
		rec := postDecision(h, decisionBody("s"), nil)
		var er ErrorResponse
		_ = json.Unmarshal(rec.Body.Bytes(), &er)
		if rec.Code != http.StatusServiceUnavailable || er.Error.Code != codeDecisionUpstream || !er.Error.Retryable {
			t.Fatalf("status %d error %+v", rec.Code, er.Error)
		}
	})
	t.Run("forbidden key", func(t *testing.T) {
		h, _ := newDecisionHandler(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"message":"unit-key rejected"}}`))
		}, 4)
		rec := postDecision(h, decisionBody("s"), nil)
		var er ErrorResponse
		_ = json.Unmarshal(rec.Body.Bytes(), &er)
		if rec.Code != http.StatusBadGateway || er.Error.Code != codeDecisionUpstreamAuth || er.Error.Retryable {
			t.Fatalf("status %d error %+v", rec.Code, er.Error)
		}
		if strings.Contains(rec.Body.String(), "unit-key") {
			t.Fatal("key leaked")
		}
	})
	t.Run("upstream 422", func(t *testing.T) {
		h, _ := newDecisionHandler(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"error":{"message":"criteria invalid"}}`))
		}, 4)
		rec := postDecision(h, decisionBody("s"), nil)
		var er ErrorResponse
		_ = json.Unmarshal(rec.Body.Bytes(), &er)
		if rec.Code != http.StatusBadRequest || er.Error.Code != codeInvalidDecision || !strings.Contains(er.Error.Message, "criteria invalid") {
			t.Fatalf("status %d error %+v", rec.Code, er.Error)
		}
	})
	t.Run("not found is rejected", func(t *testing.T) {
		h, _ := newDecisionHandler(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"message":"model missing for unit-key"}}`))
		}, 4)
		rec := postDecision(h, decisionBody("s"), nil)
		var er ErrorResponse
		_ = json.Unmarshal(rec.Body.Bytes(), &er)
		if rec.Code != http.StatusBadGateway || er.Error.Code != codeDecisionUpstreamRejected || er.Error.Retryable {
			t.Fatalf("status %d error %+v", rec.Code, er.Error)
		}
		if strings.Contains(rec.Body.String(), "unit-key") {
			t.Fatal("key leaked")
		}
	})
	t.Run("unusable upstream answers are an upstream fault", func(t *testing.T) {
		h, _ := newDecisionHandler(t, func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"model":"jev-1.13.0","answers":{"verdict":{"type":"choice","choice":"maybe","probabilities":{"complete":0.5,"incomplete":0.5},"confidence":0.1},"gaps":{"type":"noul","noul":0.9}},"usage":{"input_tokens":5,"output_tokens":0}}`))
		}, 4)
		rec := postDecision(h, decisionBody("s"), nil)
		var er ErrorResponse
		_ = json.Unmarshal(rec.Body.Bytes(), &er)
		if rec.Code != http.StatusBadGateway || er.Error.Code != codeDecisionUpstreamInvalid || !er.Error.Retryable {
			t.Fatalf("status %d error %+v", rec.Code, er.Error)
		}
		if !strings.Contains(er.Error.Message, "maybe") {
			t.Fatalf("message should name the bad choice: %q", er.Error.Message)
		}
	})
}

func TestDecisionsCancellationDuringRetrySleepReleasesCapacity(t *testing.T) {
	h, _ := newDecisionHandler(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTooManyRequests) }, 1)
	enteredSleep := make(chan struct{})
	var once sync.Once
	h.decisions.sleep = func(ctx context.Context, d time.Duration) error {
		once.Do(func() { close(enteredSleep) })
		<-ctx.Done()
		return ctx.Err()
	}

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodPost, "/v1/decisions", strings.NewReader(decisionBody("s"))).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.ServeHTTP(rec, req)
	}()

	select {
	case <-enteredSleep:
	case <-time.After(2 * time.Second):
		t.Fatal("client did not enter retry sleep")
	}
	if got := h.decisions.inFlight(); got != 1 {
		t.Fatalf("inFlight while sleeping = %d, want 1", got)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("request did not finish after cancellation")
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if got := h.decisions.inFlight(); got != 0 {
		t.Fatalf("inFlight after cancellation = %d, want 0", got)
	}
}

func TestDecisionsCapacityIsFastRetryable503(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	h, _ := newDecisionHandler(t, func(w http.ResponseWriter, r *http.Request) {
		<-release
		_, _ = w.Write([]byte(decisionOK))
	}, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		postDecision(h, decisionBody("first"), nil)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for h.decisions.inFlight() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	start := time.Now()
	rec := postDecision(h, decisionBody("second"), nil)
	if rec.Code != http.StatusServiceUnavailable || time.Since(start) > time.Second {
		t.Fatalf("status %d after %s: %s", rec.Code, time.Since(start), rec.Body.String())
	}
	var er ErrorResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &er)
	if er.Error.Code != codeDecisionCapacity || !er.Error.Retryable {
		t.Fatalf("error = %+v", er.Error)
	}
	once.Do(func() { close(release) })
	<-done
}

func TestDecisionsRequireBearerWhenTokenSet(t *testing.T) {
	h, _ := newDecisionHandler(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(decisionOK)) }, 2, "t0k")
	if rec := postDecision(h, decisionBody("s"), nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token status %d", rec.Code)
	}
	if rec := postDecision(h, decisionBody("s"), map[string]string{"Authorization": "Bearer t0k"}); rec.Code != http.StatusOK {
		t.Fatalf("with token status %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHealthReportsDecisions(t *testing.T) {
	h, _ := newDecisionHandler(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(decisionOK)) }, 3)
	postDecision(h, decisionBody("s"), nil)
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var hr HealthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &hr); err != nil {
		t.Fatal(err)
	}
	d := hr.Decisions
	if d == nil || !d.Configured || d.Model != "jev-1.13.0" || d.Total != 1 || d.Failed != 0 || d.MaxConcurrent != 3 || d.CostUSD <= 0 {
		t.Fatalf("decisions health = %+v", d)
	}
}
