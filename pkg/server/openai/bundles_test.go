package openai

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rcodegen/pkg/bundle"
	"rcodegen/pkg/envelope"
	"rcodegen/pkg/server"
)

func newBundleTestHandler(t *testing.T, fn bundleRunFunc) *Handler {
	t.Helper()
	h := NewHandler(nil, nil, server.NewRunRegistry(2), nil, nil, nil)
	if fn != nil {
		h.runBundleFn = fn
	}
	return h
}

func TestListBundles(t *testing.T) {
	h := newBundleTestHandler(t, nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/bundles", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp BundleListResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode error: %v", err)
	}

	var ensemble *BundleInfo
	for i := range resp.Bundles {
		if resp.Bundles[i].Name == "ensemble" {
			ensemble = &resp.Bundles[i]
			break
		}
	}
	if ensemble == nil {
		t.Fatalf("expected builtin bundle 'ensemble' in list, got %d bundles", len(resp.Bundles))
	}
	if ensemble.StepCount != 2 {
		t.Errorf("ensemble step_count = %d, want 2", ensemble.StepCount)
	}
	if len(ensemble.Inputs) != 1 || ensemble.Inputs[0].Name != "task" || !ensemble.Inputs[0].Required {
		t.Errorf("ensemble inputs = %+v, want single required 'task'", ensemble.Inputs)
	}
}

func TestRunBundle_SuccessWithArtifactsAndCorrelation(t *testing.T) {
	workDir := t.TempDir()

	var gotInputs map[string]string
	fake := func(b *bundle.Bundle, inputs map[string]string) (*envelope.Envelope, error) {
		gotInputs = inputs
		// Simulate a bundle writing a report plus files that must be excluded.
		if err := os.WriteFile(filepath.Join(inputs["output_dir"], "REPORT.md"), []byte("# Findings\nAll good."), 0o644); err != nil {
			t.Fatalf("write artifact: %v", err)
		}
		if err := os.WriteFile(filepath.Join(inputs["output_dir"], ".hidden.md"), []byte("skip"), 0o644); err != nil {
			t.Fatalf("write hidden: %v", err)
		}
		if err := os.WriteFile(filepath.Join(inputs["output_dir"], "data.bin"), []byte{0x00, 0x01}, 0o644); err != nil {
			t.Fatalf("write binary: %v", err)
		}
		return envelope.New().
			Success().
			WithResult("job_id", "job-42").
			WithResult("total_cost_usd", 1.5).
			WithResult("input_tokens", 10).
			WithResult("output_tokens", 20).
			WithDuration(1234).
			Build(), nil
	}
	h := newBundleTestHandler(t, fake)

	body := `{"inputs":{"task":"analyze"},"work_dir":"` + workDir + `"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/bundles/ensemble", strings.NewReader(body))
	req.Header.Set("X-Correlation-ID", "wm-job-123")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp BundleRunResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode error: %v", err)
	}

	if resp.RunID == "" {
		t.Error("expected non-empty run_id")
	}
	if resp.CorrelationID != "wm-job-123" {
		t.Errorf("correlation_id = %q, want wm-job-123", resp.CorrelationID)
	}
	if rec.Header().Get("X-Correlation-ID") != "wm-job-123" {
		t.Errorf("X-Correlation-ID header = %q, want wm-job-123", rec.Header().Get("X-Correlation-ID"))
	}
	if resp.Status != "success" {
		t.Errorf("status = %q, want success", resp.Status)
	}
	if resp.JobID != "job-42" {
		t.Errorf("job_id = %q, want job-42", resp.JobID)
	}
	if resp.TotalCostUSD != 1.5 {
		t.Errorf("total_cost_usd = %v, want 1.5", resp.TotalCostUSD)
	}
	if resp.Usage == nil || resp.Usage.PromptTokens != 10 || resp.Usage.CompletionTokens != 20 || resp.Usage.TotalTokens != 30 {
		t.Errorf("usage = %+v, want 10/20/30", resp.Usage)
	}
	if resp.DurationMs != 1234 {
		t.Errorf("duration_ms = %d, want 1234", resp.DurationMs)
	}

	if gotInputs["task"] != "analyze" {
		t.Errorf("bundle input task = %q, want analyze", gotInputs["task"])
	}
	if gotInputs["output_dir"] != workDir {
		t.Errorf("output_dir input = %q, want %q", gotInputs["output_dir"], workDir)
	}

	if len(resp.Artifacts) != 1 {
		t.Fatalf("expected 1 artifact (hidden + binary excluded), got %d: %+v", len(resp.Artifacts), resp.Artifacts)
	}
	a := resp.Artifacts[0]
	if a.Path != "REPORT.md" {
		t.Errorf("artifact path = %q, want REPORT.md", a.Path)
	}
	if a.Content != "# Findings\nAll good." {
		t.Errorf("artifact content = %q", a.Content)
	}
	if a.Truncated {
		t.Error("artifact should not be truncated")
	}
}

func TestRunBundle_PreexistingFilesNotReturned(t *testing.T) {
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "OLD.md"), []byte("existing"), 0o644); err != nil {
		t.Fatalf("write preexisting: %v", err)
	}

	fake := func(b *bundle.Bundle, inputs map[string]string) (*envelope.Envelope, error) {
		if err := os.WriteFile(filepath.Join(inputs["output_dir"], "NEW.md"), []byte("fresh"), 0o644); err != nil {
			t.Fatalf("write new: %v", err)
		}
		return envelope.New().Success().Build(), nil
	}
	h := newBundleTestHandler(t, fake)

	body := `{"inputs":{"task":"x"},"work_dir":"` + workDir + `"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/bundles/ensemble", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	var resp BundleRunResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if len(resp.Artifacts) != 1 || resp.Artifacts[0].Path != "NEW.md" {
		t.Errorf("artifacts = %+v, want only NEW.md", resp.Artifacts)
	}
}

func TestRunBundle_UnknownBundle(t *testing.T) {
	h := newBundleTestHandler(t, nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/bundles/does-not-exist", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestRunBundle_MissingInputMapsTo400(t *testing.T) {
	fake := func(b *bundle.Bundle, inputs map[string]string) (*envelope.Envelope, error) {
		return envelope.New().Failure("MISSING_INPUT", "Required input: task").Build(), nil
	}
	h := newBundleTestHandler(t, fake)

	req := httptest.NewRequest(http.MethodPost, "/v1/bundles/ensemble", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestRunBundle_MethodNotAllowed(t *testing.T) {
	h := newBundleTestHandler(t, nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/bundles/ensemble", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}

func TestRunBundle_RelativeWorkDirRejected(t *testing.T) {
	h := newBundleTestHandler(t, nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/bundles/ensemble", strings.NewReader(`{"work_dir":"relative/path"}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestAuthToken(t *testing.T) {
	t.Setenv("RSERVE_TOKEN", "secret-token")
	h := newBundleTestHandler(t, nil)

	// Without token: 401.
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token, got %d", rec.Code)
	}

	// With wrong token: 401.
	req = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 with wrong token, got %d", rec.Code)
	}

	// With correct token: 200.
	req = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer secret-token")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 with correct token, got %d", rec.Code)
	}

	// /health stays open for monitoring.
	req = httptest.NewRequest(http.MethodGet, "/health", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on /health without token, got %d", rec.Code)
	}
}

func TestSanitizeCorrelationID(t *testing.T) {
	cases := []struct{ in, want string }{
		{"wm-job-123", "wm-job-123"},
		{"has spaces\nand\tcontrol", "hasspacesandcontrol"},
		{"UUID_ok.v2-x", "UUID_ok.v2-x"},
		{"", ""},
		{strings.Repeat("a", 200), strings.Repeat("a", 128)},
	}
	for _, c := range cases {
		if got := sanitizeCorrelationID(c.in); got != c.want {
			t.Errorf("sanitizeCorrelationID(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
