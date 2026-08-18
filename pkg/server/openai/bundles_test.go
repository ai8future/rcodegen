package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"rcodegen/pkg/bundle"
	"rcodegen/pkg/envelope"
	"rcodegen/pkg/orchestrator"
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
	fake := func(ctx context.Context, b *bundle.Bundle, inputs map[string]string, opts bundleRunOpts) (*envelope.Envelope, error) {
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

	fake := func(ctx context.Context, b *bundle.Bundle, inputs map[string]string, opts bundleRunOpts) (*envelope.Envelope, error) {
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
	fake := func(ctx context.Context, b *bundle.Bundle, inputs map[string]string, opts bundleRunOpts) (*envelope.Envelope, error) {
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

	req := httptest.NewRequest(http.MethodDelete, "/v1/bundles/ensemble", nil)
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

func TestGetBundleDetail(t *testing.T) {
	h := newBundleTestHandler(t, nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/bundles/ensemble", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp BundleDetailResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if resp.Name != "ensemble" || resp.StepCount != 2 || len(resp.Steps) != 2 {
		t.Fatalf("detail = name %q, step_count %d, steps %d; want ensemble/2/2", resp.Name, resp.StepCount, len(resp.Steps))
	}
	if len(resp.Steps[0].Parallel) != 3 {
		t.Errorf("steps[0] parallel group = %d substeps, want 3", len(resp.Steps[0].Parallel))
	}
	if resp.Steps[1].Vote == nil || resp.Steps[1].Vote.Strategy != "majority" {
		t.Errorf("steps[1] vote = %+v, want majority vote node", resp.Steps[1].Vote)
	}
}

func TestRunBundle_OptionsForwarded(t *testing.T) {
	var got bundleRunOpts
	fake := func(ctx context.Context, b *bundle.Bundle, inputs map[string]string, opts bundleRunOpts) (*envelope.Envelope, error) {
		got = opts
		return envelope.New().Success().Build(), nil
	}
	h := newBundleTestHandler(t, fake)

	body := `{"inputs":{"task":"x"},"options":{"opus_only":true,"flash_only":true}}`
	req := httptest.NewRequest(http.MethodPost, "/v1/bundles/ensemble", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !got.opusOnly || !got.flashOnly {
		t.Errorf("options = opusOnly %v, flashOnly %v; want both true", got.opusOnly, got.flashOnly)
	}
	if got.onStep == nil {
		t.Error("expected onStep callback to be wired even in non-streaming mode")
	}
}

// emitTwoSteps simulates an orchestrator run with two completed steps.
func emitTwoSteps(opts bundleRunOpts) {
	opts.onStep(orchestrator.StepEvent{Type: orchestrator.StepEventStarted, Index: 0, Name: "research", Tool: "gemini", Model: "g-pro"})
	opts.onStep(orchestrator.StepEvent{
		Type: orchestrator.StepEventCompleted, Index: 0, Name: "research", Tool: "gemini", Model: "g-pro",
		Status: "success", CostUSD: 0.5, InputTokens: 5, OutputTokens: 7, DurationMs: 100,
		Envelope: envelope.New().Success().WithResult("stdout", "research notes").Build(),
	})
	opts.onStep(orchestrator.StepEvent{Type: orchestrator.StepEventStarted, Index: 1, Name: "draft", Tool: "claude", Model: "opus"})
	opts.onStep(orchestrator.StepEvent{
		Type: orchestrator.StepEventCompleted, Index: 1, Name: "draft", Tool: "claude", Model: "opus",
		Status: "success", CostUSD: 1.0, InputTokens: 11, OutputTokens: 13, DurationMs: 200,
		Envelope: envelope.New().Success().WithResult("stdout", "FINAL ANSWER").Build(),
	})
}

func TestRunBundle_StepResultsAndOutput(t *testing.T) {
	fake := func(ctx context.Context, b *bundle.Bundle, inputs map[string]string, opts bundleRunOpts) (*envelope.Envelope, error) {
		emitTwoSteps(opts)
		return envelope.New().Success().WithResult("total_cost_usd", 1.5).Build(), nil
	}
	h := newBundleTestHandler(t, fake)

	req := httptest.NewRequest(http.MethodPost, "/v1/bundles/ensemble", strings.NewReader(`{"inputs":{"task":"x"}}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp BundleRunResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if len(resp.Steps) != 2 {
		t.Fatalf("steps = %d, want 2: %+v", len(resp.Steps), resp.Steps)
	}
	s0, s1 := resp.Steps[0], resp.Steps[1]
	if s0.Name != "research" || s0.Status != "success" || s0.Output != "research notes" || s0.CostUSD != 0.5 {
		t.Errorf("steps[0] = %+v", s0)
	}
	if s1.Name != "draft" || s1.Output != "FINAL ANSWER" || s1.InputTokens != 11 || s1.OutputTokens != 13 {
		t.Errorf("steps[1] = %+v", s1)
	}
	if resp.Output != "FINAL ANSWER" {
		t.Errorf("output = %q, want FINAL ANSWER (last successful step)", resp.Output)
	}
}

func TestRunBundle_Streaming(t *testing.T) {
	fake := func(ctx context.Context, b *bundle.Bundle, inputs map[string]string, opts bundleRunOpts) (*envelope.Envelope, error) {
		emitTwoSteps(opts)
		return envelope.New().Success().Build(), nil
	}
	h := newBundleTestHandler(t, fake)

	req := httptest.NewRequest(http.MethodPost, "/v1/bundles/ensemble", strings.NewReader(`{"inputs":{"task":"x"},"stream":true}`))
	req.Header.Set("X-Correlation-ID", "wm-777")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}
	if rec.Header().Get("X-Correlation-ID") != "wm-777" {
		t.Errorf("X-Correlation-ID header = %q, want wm-777", rec.Header().Get("X-Correlation-ID"))
	}

	body := rec.Body.String()
	for _, want := range []string{"event: step_started", "event: step_completed", "event: bundle_completed"} {
		if !strings.Contains(body, want) {
			t.Errorf("stream missing %q:\n%s", want, body)
		}
	}

	// Parse the bundle_completed payload.
	lines := strings.Split(body, "\n")
	var completedData string
	for i, line := range lines {
		if line == "event: bundle_completed" && i+1 < len(lines) {
			completedData = strings.TrimPrefix(lines[i+1], "data: ")
		}
	}
	if completedData == "" {
		t.Fatalf("no bundle_completed data found in stream:\n%s", body)
	}
	var resp BundleRunResponse
	if err := json.Unmarshal([]byte(completedData), &resp); err != nil {
		t.Fatalf("decode bundle_completed: %v", err)
	}
	if resp.Status != "success" || resp.Output != "FINAL ANSWER" || len(resp.Steps) != 2 {
		t.Errorf("bundle_completed = status %q, output %q, steps %d", resp.Status, resp.Output, len(resp.Steps))
	}
	if resp.CorrelationID != "wm-777" {
		t.Errorf("correlation_id = %q, want wm-777", resp.CorrelationID)
	}
}

func TestRunBundle_NonRegularFilesNotCollected(t *testing.T) {
	workDir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.md")
	if err := os.WriteFile(outside, []byte("SECRET"), 0o644); err != nil {
		t.Fatalf("write outside file: %v", err)
	}

	fake := func(ctx context.Context, b *bundle.Bundle, inputs map[string]string, opts bundleRunOpts) (*envelope.Envelope, error) {
		dir := inputs["output_dir"]
		if err := os.WriteFile(filepath.Join(dir, "REPORT.md"), []byte("ok"), 0o644); err != nil {
			t.Fatalf("write report: %v", err)
		}
		// A symlink pointing outside work_dir must not leak content.
		if err := os.Symlink(outside, filepath.Join(dir, "LEAK.md")); err != nil {
			t.Fatalf("symlink: %v", err)
		}
		// A FIFO must not be opened (opening would block the response).
		_ = syscall.Mkfifo(filepath.Join(dir, "PIPE.md"), 0o644)
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
	if len(resp.Artifacts) != 1 || resp.Artifacts[0].Path != "REPORT.md" {
		t.Fatalf("artifacts = %+v, want only REPORT.md", resp.Artifacts)
	}
	for _, a := range resp.Artifacts {
		if strings.Contains(a.Content, "SECRET") {
			t.Fatalf("symlink target content leaked into artifacts: %+v", a)
		}
	}
}

func TestRunBundle_WorkRootRestriction(t *testing.T) {
	allowed := t.TempDir()
	t.Setenv("RSERVE_WORK_ROOT", allowed)

	fake := func(ctx context.Context, b *bundle.Bundle, inputs map[string]string, opts bundleRunOpts) (*envelope.Envelope, error) {
		return envelope.New().Success().Build(), nil
	}
	h := newBundleTestHandler(t, fake)

	// Outside the root: rejected.
	req := httptest.NewRequest(http.MethodPost, "/v1/bundles/ensemble",
		strings.NewReader(`{"inputs":{"task":"x"},"work_dir":"/tmp/definitely-elsewhere"}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for work_dir outside root, got %d: %s", rec.Code, rec.Body.String())
	}

	// Inside the root: accepted.
	inside := filepath.Join(allowed, "job1")
	req = httptest.NewRequest(http.MethodPost, "/v1/bundles/ensemble",
		strings.NewReader(`{"inputs":{"task":"x"},"work_dir":"`+inside+`"}`))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for work_dir under root, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestTrimPartialRune(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"ascii untouched", "hello", "hello"},
		{"complete rune untouched", "café", "café"},
		{"split 2-byte rune", "ab\xc3", "ab"},          // é cut after first byte
		{"split 3-byte rune", "ab\xe2\x82", "ab"},      // € cut after two bytes
		{"split 4-byte rune", "ab\xf0\x9f\x98", "ab"},  // emoji cut after three bytes
		{"lone invalid byte trimmed", "ab\xff", "ab"},  // indistinguishable from a split start byte
		{"empty", "", ""},
	}
	for _, c := range cases {
		if got := trimPartialRune(c.in); got != c.want {
			t.Errorf("%s: trimPartialRune(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
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
