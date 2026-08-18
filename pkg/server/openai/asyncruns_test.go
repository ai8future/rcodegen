package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"rcodegen/pkg/runner"
	"rcodegen/pkg/server"
	"rcodegen/pkg/tools/opencode"

	chassis "github.com/ai8future/chassis-go/v11"
)

// ---------------------------------------------------------------------------
// Harness
// ---------------------------------------------------------------------------

// asyncHandler builds a handler whose callbacks retry without the production
// backoff, and whose detached runs are stopped when the test ends. Call it
// after installing a fake CLI, so the shutdown runs before PATH is restored.
func asyncHandler(t *testing.T, reg *server.RunRegistry, factory server.ToolFactory) *Handler {
	t.Helper()
	h := NewHandler(nil, map[string]server.ToolFactory{"opencode": factory}, reg,
		[]string{"opencode"}, nil, nil)
	h.async.backoff = []time.Duration{time.Millisecond, time.Millisecond}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		h.Shutdown(ctx)
	})
	return h
}

func openCodeFactory() server.ToolFactory {
	return func() runner.Tool { return opencode.New() }
}

// callbackReceiver stands in for the caller's callback endpoint — a Windmill
// resume URL in production.
type callbackReceiver struct {
	server *httptest.Server

	mu       sync.Mutex
	payloads []AsyncCompletion
	headers  []http.Header
	status   int

	hits chan struct{}
}

// newCallbackReceiver starts a receiver that answers every POST with status.
func newCallbackReceiver(t *testing.T, status int) *callbackReceiver {
	t.Helper()
	rec := &callbackReceiver{status: status, hits: make(chan struct{}, 64)}
	rec.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload AsyncCompletion
		_ = json.Unmarshal(body, &payload)

		rec.mu.Lock()
		rec.payloads = append(rec.payloads, payload)
		rec.headers = append(rec.headers, r.Header.Clone())
		code := rec.status
		rec.mu.Unlock()

		w.WriteHeader(code)
		select {
		case rec.hits <- struct{}{}:
		default:
		}
	}))
	t.Cleanup(rec.server.Close)
	return rec
}

// await blocks until the receiver has taken n POSTs, then returns the first.
func (c *callbackReceiver) await(t *testing.T, n int) AsyncCompletion {
	t.Helper()
	deadline := time.After(20 * time.Second)
	for {
		c.mu.Lock()
		got := len(c.payloads)
		var first AsyncCompletion
		if got > 0 {
			first = c.payloads[0]
		}
		c.mu.Unlock()
		if got >= n {
			return first
		}
		select {
		case <-c.hits:
		case <-deadline:
			t.Fatalf("callback delivered %d times, want %d", got, n)
		}
	}
}

func (c *callbackReceiver) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.payloads)
}

func (c *callbackReceiver) header(t *testing.T, i int, name string) string {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if i >= len(c.headers) {
		t.Fatalf("no callback %d recorded", i)
	}
	return c.headers[i].Get(name)
}

// submitAsync posts a chat completion and asserts the 202 submission contract.
func submitAsync(t *testing.T, h *Handler, body, corrID string) AsyncSubmitResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	if corrID != "" {
		req.Header.Set("X-Correlation-ID", corrID)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp AsyncSubmitResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode 202 body: %v", err)
	}
	if resp.RunID == "" {
		t.Fatal("202 carries no run_id")
	}
	if resp.Status != runStatusQueued {
		t.Errorf("202 status = %q, want %s", resp.Status, runStatusQueued)
	}
	return resp
}

// callBody builds a chat completion request body with a callback URL.
func callBody(url string, extra string) string {
	body := `{"model":"opencode","messages":[{"role":"user","content":"hello"}],` +
		`"callback_url":"` + url + `"`
	if extra != "" {
		body += "," + extra
	}
	return body + "}"
}

// do issues a request against the handler and returns the recorder.
func do(t *testing.T, h *Handler, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

// pollUntil waits for a run to reach one of the wanted statuses.
func pollUntil(t *testing.T, h *Handler, runID string, want ...string) RunSummary {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		rec := do(t, h, http.MethodGet, "/v1/runs/"+runID)
		if rec.Code != http.StatusOK {
			t.Fatalf("poll status = %d, body = %s", rec.Code, rec.Body.String())
		}
		var summary RunSummary
		if err := json.NewDecoder(rec.Body).Decode(&summary); err != nil {
			t.Fatalf("decode summary: %v", err)
		}
		for _, w := range want {
			if summary.Status == w {
				return summary
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %s stuck at %q, want one of %v", runID, summary.Status, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// awaitQueued waits until the registry reports a request waiting for a slot,
// so a test can free one knowing the async run is really behind it.
func awaitQueued(t *testing.T, reg *server.RunRegistry) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for reg.QueuedCount() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("no request ever queued for a run slot")
		}
		time.Sleep(time.Millisecond)
	}
}

// gatedDirectAPITool takes the direct-API path and stays there until the test
// releases it or its context ends, so a run can be observed mid-flight.
type gatedDirectAPITool struct {
	runner.Tool
	started chan string
	release chan struct{}
}

func (t *gatedDirectAPITool) ShouldUseDirectAPI(*runner.Config) bool { return true }

func (t *gatedDirectAPITool) RunDirectAPI(ctx context.Context, cfg *runner.Config, workDir, task string) int {
	t.started <- workDir
	select {
	case <-t.release:
		fmt.Fprint(cfg.Output, "gated output")
		return 0
	case <-ctx.Done():
		return 130
	}
}

// ---------------------------------------------------------------------------
// Submission contract
// ---------------------------------------------------------------------------

// The 202 must come back while the run is still waiting for a slot: releasing
// the connection is the whole point of callback mode.
func TestAsyncSubmit_Returns202AndReleasesTheConnectionWhileQueued(t *testing.T) {
	chassis.RequireMajor(11)
	installFakeOpenCode(t, "queued then run")
	receiver := newCallbackReceiver(t, http.StatusOK)
	reg := server.NewRunRegistry(1)
	h := asyncHandler(t, reg, openCodeFactory())

	// Occupy the only slot so the submitted run cannot start.
	heldID, _, heldCancel, err := reg.Acquire(context.Background(), "opencode", "held")
	if err != nil {
		t.Fatalf("acquire holding slot: %v", err)
	}

	done := make(chan AsyncSubmitResponse, 1)
	go func() { done <- submitAsync(t, h, callBody(receiver.server.URL, ""), "wm-async-1") }()

	var submitted AsyncSubmitResponse
	select {
	case submitted = <-done:
	case <-time.After(10 * time.Second):
		heldCancel()
		reg.Release(heldID)
		t.Fatal("submission blocked on a run slot instead of returning 202")
	}
	if submitted.CorrelationID != "wm-async-1" {
		t.Errorf("202 correlation_id = %q, want wm-async-1", submitted.CorrelationID)
	}

	summary := pollUntil(t, h, submitted.RunID, runStatusQueued)
	if summary.StartedAt != 0 {
		t.Errorf("queued run reports started_at = %d", summary.StartedAt)
	}

	// Hold the slot a beat past the point where the run is provably waiting for
	// it, so the wait the run reports is longer than the millisecond the field
	// is measured in.
	awaitQueued(t, reg)
	time.Sleep(25 * time.Millisecond)
	heldCancel()
	reg.Release(heldID)

	payload := receiver.await(t, 1)
	if payload.Status != runStatusSuccess {
		t.Fatalf("callback status = %q, want success (error %+v)", payload.Status, payload.Error)
	}
	final := pollUntil(t, h, submitted.RunID, runStatusSuccess)
	if final.QueueWaitMs <= 0 {
		t.Errorf("queue_wait_ms = %d, want the wait it actually served", final.QueueWaitMs)
	}
}

func TestAsyncSubmit_CallbackCarriesTheCompletionOnSuccess(t *testing.T) {
	chassis.RequireMajor(11)
	installFakeOpenCode(t, "async completion output")
	receiver := newCallbackReceiver(t, http.StatusOK)
	h := asyncHandler(t, server.NewRunRegistry(1), openCodeFactory())

	submitted := submitAsync(t, h, callBody(receiver.server.URL, ""), "wm-async-ok")
	payload := receiver.await(t, 1)

	if payload.RunID != submitted.RunID {
		t.Errorf("callback run_id = %q, want %q", payload.RunID, submitted.RunID)
	}
	if payload.Status != runStatusSuccess {
		t.Errorf("callback status = %q, want success", payload.Status)
	}
	if payload.Error != nil {
		t.Errorf("successful callback carries an error: %+v", payload.Error)
	}
	if payload.ID != "chatcmpl-"+submitted.RunID {
		t.Errorf("callback id = %q, want chatcmpl-%s", payload.ID, submitted.RunID)
	}
	if payload.Object != "chat.completion" {
		t.Errorf("callback object = %q, want chat.completion", payload.Object)
	}
	if payload.CorrelationID != "wm-async-ok" {
		t.Errorf("callback correlation_id = %q, want wm-async-ok", payload.CorrelationID)
	}
	if len(payload.Choices) != 1 || payload.Choices[0].Message == nil {
		t.Fatalf("callback choices = %+v", payload.Choices)
	}
	if got := payload.Choices[0].Message.Content; got != "async completion output" {
		t.Errorf("callback content = %q, want the CLI's output", got)
	}

	// The same payload is retained for polling.
	rec := do(t, h, http.MethodGet, "/v1/runs/"+submitted.RunID+"/result")
	if rec.Code != http.StatusOK {
		t.Fatalf("result status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var retained AsyncCompletion
	if err := json.NewDecoder(rec.Body).Decode(&retained); err != nil {
		t.Fatalf("decode retained result: %v", err)
	}
	if retained.Choices[0].Message.Content != payload.Choices[0].Message.Content {
		t.Error("the retained result differs from what the callback received")
	}
}

func TestAsyncSubmit_CallbackHeadersRideThePost(t *testing.T) {
	chassis.RequireMajor(11)
	installFakeOpenCode(t, "authed callback")
	receiver := newCallbackReceiver(t, http.StatusOK)
	h := asyncHandler(t, server.NewRunRegistry(1), openCodeFactory())

	body := callBody(receiver.server.URL, `"callback_headers":{"Authorization":"Bearer receiver-token","X-Flow":"digest"}`)
	submitAsync(t, h, body, "")
	receiver.await(t, 1)

	if got := receiver.header(t, 0, "Authorization"); got != "Bearer receiver-token" {
		t.Errorf("Authorization header = %q, want the caller's value", got)
	}
	if got := receiver.header(t, 0, "X-Flow"); got != "digest" {
		t.Errorf("X-Flow header = %q, want digest", got)
	}
	if got := receiver.header(t, 0, "Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
}

// A receiver that never accepts the callback costs the run nothing: the
// attempts are bounded and the result stays pollable.
func TestAsyncSubmit_ReceiverDownIsRetriedThenLeftForPolling(t *testing.T) {
	chassis.RequireMajor(11)
	installFakeOpenCode(t, "undeliverable output")
	receiver := newCallbackReceiver(t, http.StatusInternalServerError)
	h := asyncHandler(t, server.NewRunRegistry(1), openCodeFactory())

	submitted := submitAsync(t, h, callBody(receiver.server.URL, ""), "")
	receiver.await(t, len(h.async.backoff)+1)

	// Give any fourth attempt time to arrive before asserting there was none.
	time.Sleep(50 * time.Millisecond)
	if got := receiver.count(); got != 3 {
		t.Errorf("callback attempts = %d, want 3", got)
	}

	rec := do(t, h, http.MethodGet, "/v1/runs/"+submitted.RunID+"/result")
	if rec.Code != http.StatusOK {
		t.Fatalf("result status = %d after undelivered callback, body = %s", rec.Code, rec.Body.String())
	}
	var retained AsyncCompletion
	if err := json.NewDecoder(rec.Body).Decode(&retained); err != nil {
		t.Fatalf("decode retained result: %v", err)
	}
	if retained.Choices[0].Message.Content != "undeliverable output" {
		t.Errorf("retained content = %q", retained.Choices[0].Message.Content)
	}
}

// ---------------------------------------------------------------------------
// Request validation
// ---------------------------------------------------------------------------

func TestAsyncSubmit_RejectsStreamAndCallbackTogether(t *testing.T) {
	chassis.RequireMajor(11)
	installFakeOpenCode(t, "never runs")
	h := asyncHandler(t, server.NewRunRegistry(1), openCodeFactory())

	body := callBody("https://windmill.example.invalid/resume", `"stream":true`)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var errResp ErrorResponse
	if err := json.NewDecoder(rec.Body).Decode(&errResp); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if errResp.Error.Code != codeCallbackStreamConflict {
		t.Errorf("code = %q, want %s", errResp.Error.Code, codeCallbackStreamConflict)
	}
	if errResp.Error.Retryable {
		t.Error("callback_stream_conflict reported as retryable")
	}
}

func TestAsyncSubmit_RejectsUnusableCallbackURLs(t *testing.T) {
	chassis.RequireMajor(11)
	installFakeOpenCode(t, "never runs")
	h := asyncHandler(t, server.NewRunRegistry(1), openCodeFactory())

	cases := []struct {
		name string
		url  string
	}{
		{"plain http to a public host", "http://example.com/resume"},
		{"no scheme", "example.com/resume"},
		{"no host", "https:///resume"},
		{"unsupported scheme", "ftp://example.com/resume"},
		{"not a url", "://nope"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
				strings.NewReader(callBody(c.url, "")))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			var errResp ErrorResponse
			if err := json.NewDecoder(rec.Body).Decode(&errResp); err != nil {
				t.Fatalf("decode error response: %v", err)
			}
			if errResp.Error.Code != codeInvalidCallbackURL {
				t.Errorf("code = %q, want %s", errResp.Error.Code, codeInvalidCallbackURL)
			}
			if errResp.Error.Retryable {
				t.Error("invalid_callback_url reported as retryable")
			}
		})
	}

	// Nothing was submitted, so no run exists to poll for.
	rec := do(t, h, http.MethodGet, "/v1/runs")
	var list RunList
	if err := json.NewDecoder(rec.Body).Decode(&list); err != nil {
		t.Fatalf("decode run list: %v", err)
	}
	if len(list.Data) != 0 {
		t.Errorf("rejected submissions created %d runs", len(list.Data))
	}
}

func TestAsyncSubmit_AcceptsHTTPSAndPrivateHTTPTargets(t *testing.T) {
	for _, raw := range []string{
		"https://windmill.example.com/api/w/aows/jobs_u/resume/1/2/sig",
		"http://127.0.0.1:9000/resume",
		"http://localhost:9000/resume",
		"http://10.1.2.3/resume",
		"http://192.168.1.10:8080/resume",
		"http://172.16.4.4/resume",
	} {
		if _, err := newCallbackTarget(raw, nil); err != nil {
			t.Errorf("newCallbackTarget(%q) = %v, want accepted", raw, err)
		}
	}
	for _, raw := range []string{
		"http://8.8.8.8/resume",
		"http://172.32.0.1/resume", // just outside RFC1918's 172.16/12
	} {
		if _, err := newCallbackTarget(raw, nil); err == nil {
			t.Errorf("newCallbackTarget(%q) accepted a public plaintext target", raw)
		}
	}
}

func TestAsyncSubmit_RejectsUnsendableCallbackHeaders(t *testing.T) {
	chassis.RequireMajor(11)
	installFakeOpenCode(t, "never runs")
	h := asyncHandler(t, server.NewRunRegistry(1), openCodeFactory())

	body := callBody("https://windmill.example.invalid/resume", `"callback_headers":{"X-Bad":"tok\nSUPERSECRET"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var errResp ErrorResponse
	if err := json.NewDecoder(rec.Body).Decode(&errResp); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if errResp.Error.Code != codeInvalidCallbackHeaders {
		t.Errorf("code = %q, want %s", errResp.Error.Code, codeInvalidCallbackHeaders)
	}
	// The offending value is the caller's secret and must not come back in the
	// message that rejects it.
	if strings.Contains(errResp.Error.Message, "SUPERSECRET") {
		t.Errorf("rejection quotes the header value: %s", errResp.Error.Message)
	}
}

// A callback submission is still a chat completion: the checks that make a
// synchronous request 400 must make this one 400 too, on this connection,
// before any run_id exists.
func TestAsyncSubmit_ValidatesTheRequestBeforeAccepting(t *testing.T) {
	chassis.RequireMajor(11)
	installFakeOpenCode(t, "never runs")
	h := asyncHandler(t, server.NewRunRegistry(1), openCodeFactory())

	missing := filepath.Join(t.TempDir(), "does-not-exist")
	cases := []struct {
		name string
		body string
		code ErrorCode
	}{
		{
			name: "unknown tool",
			body: `{"model":"nosuchtool","messages":[{"role":"user","content":"hi"}],` +
				`"callback_url":"https://windmill.example.invalid/resume"}`,
			code: codeUnknownTool,
		},
		{
			name: "no user message",
			body: `{"model":"opencode","messages":[],"callback_url":"https://windmill.example.invalid/resume"}`,
			code: codeEmptyTask,
		},
		{
			name: "unusable work_dir",
			body: `{"model":"opencode","messages":[{"role":"user","content":"hi"}],` +
				`"work_dirs":["` + missing + `"],"clone_work_dirs":true,` +
				`"callback_url":"https://windmill.example.invalid/resume"}`,
			code: codeInvalidWorkDir,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(c.body))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			var errResp ErrorResponse
			if err := json.NewDecoder(rec.Body).Decode(&errResp); err != nil {
				t.Fatalf("decode error response: %v", err)
			}
			if errResp.Error.Code != c.code {
				t.Errorf("code = %q, want %s", errResp.Error.Code, c.code)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Polling, lookup, cancellation
// ---------------------------------------------------------------------------

func TestAsyncRuns_PollsThroughQueuedRunningSuccess(t *testing.T) {
	chassis.RequireMajor(11)
	receiver := newCallbackReceiver(t, http.StatusOK)
	tool := &gatedDirectAPITool{
		Tool:    opencode.New(),
		started: make(chan string, 1),
		release: make(chan struct{}),
	}
	reg := server.NewRunRegistry(1)
	h := asyncHandler(t, reg, func() runner.Tool { return tool })

	held, _, heldCancel, err := reg.Acquire(context.Background(), "opencode", "held")
	if err != nil {
		t.Fatalf("acquire holding slot: %v", err)
	}

	submitted := submitAsync(t, h, callBody(receiver.server.URL, ""), "wm-lifecycle")
	queued := pollUntil(t, h, submitted.RunID, runStatusQueued)
	if queued.CreatedAt == 0 {
		t.Error("queued run has no created_at")
	}

	// A run with no result yet is reported as such, not as a successful empty one.
	rec := do(t, h, http.MethodGet, "/v1/runs/"+submitted.RunID+"/result")
	if rec.Code != http.StatusNotFound {
		t.Errorf("result before completion = %d, want 404", rec.Code)
	}

	heldCancel()
	reg.Release(held)

	select {
	case <-tool.started:
	case <-time.After(15 * time.Second):
		t.Fatal("run never started after the slot freed")
	}
	running := pollUntil(t, h, submitted.RunID, runStatusRunning)
	if running.StartedAt == 0 {
		t.Error("running run has no started_at")
	}
	if running.FinishedAt != 0 {
		t.Error("running run reports finished_at")
	}

	close(tool.release)
	payload := receiver.await(t, 1)
	if payload.Status != runStatusSuccess {
		t.Fatalf("callback status = %q, want success", payload.Status)
	}
	final := pollUntil(t, h, submitted.RunID, runStatusSuccess)
	if final.FinishedAt == 0 {
		t.Error("finished run has no finished_at")
	}
	if final.StartedAt < final.CreatedAt || final.FinishedAt < final.StartedAt {
		t.Errorf("timings out of order: %+v", final)
	}
}

func TestAsyncRuns_LookupByCorrelationID(t *testing.T) {
	chassis.RequireMajor(11)
	installFakeOpenCode(t, "correlated async")
	receiver := newCallbackReceiver(t, http.StatusOK)
	h := asyncHandler(t, server.NewRunRegistry(2), openCodeFactory())

	mine := submitAsync(t, h, callBody(receiver.server.URL, ""), "wm-job-mine")
	other := submitAsync(t, h, callBody(receiver.server.URL, ""), "wm-job-other")
	receiver.await(t, 2)

	rec := do(t, h, http.MethodGet, "/v1/runs?correlation_id=wm-job-mine")
	if rec.Code != http.StatusOK {
		t.Fatalf("lookup status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var list RunList
	if err := json.NewDecoder(rec.Body).Decode(&list); err != nil {
		t.Fatalf("decode run list: %v", err)
	}
	if list.Object != "list" {
		t.Errorf("object = %q, want list", list.Object)
	}
	if len(list.Data) != 1 {
		t.Fatalf("lookup returned %d runs, want 1: %+v", len(list.Data), list.Data)
	}
	if list.Data[0].RunID != mine.RunID {
		t.Errorf("lookup returned %s, want %s", list.Data[0].RunID, mine.RunID)
	}
	if list.Data[0].CorrelationID != "wm-job-mine" {
		t.Errorf("correlation_id = %q", list.Data[0].CorrelationID)
	}

	// Without a filter both runs are listed.
	all := do(t, h, http.MethodGet, "/v1/runs")
	var everything RunList
	if err := json.NewDecoder(all.Body).Decode(&everything); err != nil {
		t.Fatalf("decode full run list: %v", err)
	}
	if len(everything.Data) != 2 {
		t.Errorf("unfiltered list returned %d runs, want 2 (%s and %s)",
			len(everything.Data), mine.RunID, other.RunID)
	}
}

// DELETE stands in for the disconnect that used to cancel a synchronous run:
// the CLI is stopped, the scratch clone is removed, the slot is freed, and the
// callback reports the cancellation.
func TestAsyncRuns_DeleteCancelsARunningRun(t *testing.T) {
	chassis.RequireMajor(11)
	receiver := newCallbackReceiver(t, http.StatusOK)
	tool := &blockingDirectAPITool{Tool: opencode.New(), started: make(chan string, 1)}
	reg := server.NewRunRegistry(1)
	h := asyncHandler(t, reg, func() runner.Tool { return tool })

	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "main.go"), []byte("package main"), 0o644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}
	body := callBody(receiver.server.URL, `"work_dirs":["`+src+`"],"clone_work_dirs":true`)
	submitted := submitAsync(t, h, body, "wm-cancel")

	var clonePath string
	select {
	case clonePath = <-tool.started:
	case <-time.After(15 * time.Second):
		t.Fatal("run never started")
	}
	if !strings.Contains(clonePath, "rserve-clone-") {
		t.Fatalf("run was pointed at %s, not a scratch clone", clonePath)
	}

	rec := do(t, h, http.MethodDelete, "/v1/runs/"+submitted.RunID)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, body = %s", rec.Code, rec.Body.String())
	}

	payload := receiver.await(t, 1)
	if payload.Status != runStatusFailure {
		t.Fatalf("callback status = %q, want failure", payload.Status)
	}
	if payload.Error == nil || payload.Error.Code != codeRunCancelled {
		t.Fatalf("callback error = %+v, want %s", payload.Error, codeRunCancelled)
	}
	if payload.Error.Retryable {
		t.Error("a cancelled run reported as retryable")
	}
	if payload.RunID != submitted.RunID {
		t.Errorf("callback run_id = %q, want %q", payload.RunID, submitted.RunID)
	}

	pollUntil(t, h, submitted.RunID, runStatusFailure)
	if _, err := os.Stat(clonePath); !os.IsNotExist(err) {
		t.Errorf("scratch clone survived the cancelled run at %s (err = %v)", clonePath, err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for reg.ActiveCount() != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := reg.ActiveCount(); got != 0 {
		t.Errorf("active runs = %d after cancellation, want 0", got)
	}
}

func TestAsyncRuns_DeleteCancelsAQueuedRun(t *testing.T) {
	chassis.RequireMajor(11)
	installFakeOpenCode(t, "never gets a slot")
	receiver := newCallbackReceiver(t, http.StatusOK)
	reg := server.NewRunRegistry(1)
	h := asyncHandler(t, reg, openCodeFactory())

	held, _, heldCancel, err := reg.Acquire(context.Background(), "opencode", "held")
	if err != nil {
		t.Fatalf("acquire holding slot: %v", err)
	}
	defer func() {
		heldCancel()
		reg.Release(held)
	}()

	submitted := submitAsync(t, h, callBody(receiver.server.URL, ""), "")
	pollUntil(t, h, submitted.RunID, runStatusQueued)

	rec := do(t, h, http.MethodDelete, "/v1/runs/"+submitted.RunID)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, body = %s", rec.Code, rec.Body.String())
	}

	payload := receiver.await(t, 1)
	if payload.Status != runStatusFailure || payload.Error == nil || payload.Error.Code != codeRunCancelled {
		t.Fatalf("callback = %+v, want a run_cancelled failure", payload)
	}
	pollUntil(t, h, submitted.RunID, runStatusFailure)
}

func TestAsyncRuns_UnknownRunIsNotFound(t *testing.T) {
	chassis.RequireMajor(11)
	h := asyncHandler(t, server.NewRunRegistry(1), openCodeFactory())

	for _, tc := range []struct {
		method, path string
	}{
		{http.MethodGet, "/v1/runs/deadbeef"},
		{http.MethodGet, "/v1/runs/deadbeef/result"},
		{http.MethodDelete, "/v1/runs/deadbeef"},
	} {
		rec := do(t, h, tc.method, tc.path)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404", tc.method, tc.path, rec.Code)
		}
		var errResp ErrorResponse
		if err := json.NewDecoder(rec.Body).Decode(&errResp); err != nil {
			t.Fatalf("decode error response: %v", err)
		}
		if errResp.Error.Code != codeNotFound {
			t.Errorf("%s %s code = %q, want %s", tc.method, tc.path, errResp.Error.Code, codeNotFound)
		}
	}

	if rec := do(t, h, http.MethodPost, "/v1/runs/deadbeef"); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /v1/runs/{id} = %d, want 405", rec.Code)
	}
	if rec := do(t, h, http.MethodPost, "/v1/runs"); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /v1/runs = %d, want 405", rec.Code)
	}
}

// The run endpoints are as authenticated as the rest of /v1.
func TestAsyncRuns_RequireBearerTokenWhenConfigured(t *testing.T) {
	chassis.RequireMajor(11)
	t.Setenv("RSERVE_TOKEN", "s3cret")
	h := asyncHandler(t, server.NewRunRegistry(1), openCodeFactory())

	for _, path := range []string{"/v1/runs", "/v1/runs/deadbeef", "/v1/runs/deadbeef/result"} {
		rec := do(t, h, http.MethodGet, path)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("GET %s without a token = %d, want 401", path, rec.Code)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/runs", nil)
	req.Header.Set("Authorization", "Bearer s3cret")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("GET /v1/runs with the token = %d, want 200", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// Retention
// ---------------------------------------------------------------------------

// finishTestRun registers and finishes a run directly, for retention tests that
// should not depend on executing anything.
func finishTestRun(a *asyncRuns, correlationID string) *asyncRun {
	run := a.submit(correlationID, nil)
	a.finish(run, runStatusSuccess, &AsyncCompletion{RunID: run.id, Status: runStatusSuccess})
	return run
}

func TestAsyncRetention_EvictsLeastRecentlyUsedOverTheCap(t *testing.T) {
	a := newAsyncRuns()
	a.cap = 2

	first := finishTestRun(a, "")
	second := finishTestRun(a, "")

	// Touching the first makes the second the least recently used.
	if _, ok := a.summary(first.id); !ok {
		t.Fatal("first run evicted early")
	}
	third := finishTestRun(a, "")

	if _, ok := a.summary(second.id); ok {
		t.Error("the least recently used result survived the cap")
	}
	for _, run := range []*asyncRun{first, third} {
		if _, ok := a.summary(run.id); !ok {
			t.Errorf("run %s evicted while within the cap", run.id)
		}
	}
}

func TestAsyncRetention_ExpiresFinishedRunsAtTheTTL(t *testing.T) {
	a := newAsyncRuns()
	a.ttl = time.Hour
	now := time.Now()
	a.now = func() time.Time { return now }

	run := finishTestRun(a, "")
	if _, ok := a.summary(run.id); !ok {
		t.Fatal("run missing immediately after finishing")
	}

	now = now.Add(59 * time.Minute)
	if _, ok := a.summary(run.id); !ok {
		t.Error("run expired before its TTL")
	}
	now = now.Add(2 * time.Minute)
	if _, ok := a.summary(run.id); ok {
		t.Error("run outlived its TTL")
	}
}

// Retention bounds apply to results. A run still queued or running is not a
// result and must never be evicted out from under its caller.
func TestAsyncRetention_KeepsUnfinishedRunsRegardlessOfBounds(t *testing.T) {
	a := newAsyncRuns()
	a.cap = 1
	a.ttl = time.Nanosecond

	live := a.submit("", nil)
	for i := 0; i < 5; i++ {
		finishTestRun(a, "")
	}
	time.Sleep(2 * time.Nanosecond)

	if _, ok := a.summary(live.id); !ok {
		t.Fatal("an unfinished run was evicted")
	}
	retained := 0
	for _, s := range a.list("") {
		if s.Status == runStatusSuccess {
			retained++
		}
	}
	if retained > a.cap {
		t.Errorf("retained %d results, cap is %d", retained, a.cap)
	}
}

// An evicted run answers exactly like one that never existed — including for
// DELETE, which is how a caller finds out its result is gone.
func TestAsyncRetention_EvictedRunIs404OnEveryEndpoint(t *testing.T) {
	chassis.RequireMajor(11)
	installFakeOpenCode(t, "evicted output")
	receiver := newCallbackReceiver(t, http.StatusOK)
	h := asyncHandler(t, server.NewRunRegistry(1), openCodeFactory())
	h.async.cap = 1

	first := submitAsync(t, h, callBody(receiver.server.URL, ""), "")
	receiver.await(t, 1)
	second := submitAsync(t, h, callBody(receiver.server.URL, ""), "")
	receiver.await(t, 2)

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/v1/runs/" + first.RunID},
		{http.MethodGet, "/v1/runs/" + first.RunID + "/result"},
		{http.MethodDelete, "/v1/runs/" + first.RunID},
	} {
		if rec := do(t, h, tc.method, tc.path); rec.Code != http.StatusNotFound {
			t.Errorf("%s %s after eviction = %d, want 404", tc.method, tc.path, rec.Code)
		}
	}
	if rec := do(t, h, http.MethodGet, "/v1/runs/"+second.RunID+"/result"); rec.Code != http.StatusOK {
		t.Errorf("the newest result was evicted: %d", rec.Code)
	}
}

func TestAsyncRetention_MarksTruncatedOutput(t *testing.T) {
	chassis.RequireMajor(11)
	binDir := t.TempDir()
	script := filepath.Join(binDir, "opencode")
	// 70000 bytes: past the 64KB retention cap.
	if err := os.WriteFile(script, []byte("#!/bin/sh\nhead -c 70000 /dev/zero | tr '\\0' 'x'\n"), 0o755); err != nil {
		t.Fatalf("write fake opencode: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	receiver := newCallbackReceiver(t, http.StatusOK)
	h := asyncHandler(t, server.NewRunRegistry(1), openCodeFactory())

	submitted := submitAsync(t, h, callBody(receiver.server.URL, ""), "")
	payload := receiver.await(t, 1)

	if !payload.OutputTruncated {
		t.Error("an oversize completion was delivered without the truncation marker")
	}
	if got := len(payload.Choices[0].Message.Content); got > asyncOutputCap {
		t.Errorf("delivered content = %d bytes, cap is %d", got, asyncOutputCap)
	}
	if got := len(payload.Choices[0].Message.Content); got == 0 {
		t.Error("an oversize completion was dropped rather than truncated")
	}

	rec := do(t, h, http.MethodGet, "/v1/runs/"+submitted.RunID+"/result")
	var retained AsyncCompletion
	if err := json.NewDecoder(rec.Body).Decode(&retained); err != nil {
		t.Fatalf("decode retained result: %v", err)
	}
	if !retained.OutputTruncated {
		t.Error("the retained result lost the truncation marker")
	}
}

// ---------------------------------------------------------------------------
// Shutdown
// ---------------------------------------------------------------------------

// A run in flight when the server stops gets one last callback saying so —
// marked retryable, because nothing about the request was wrong.
func TestAsyncShutdown_TellsInFlightRunsTheServerIsGoing(t *testing.T) {
	chassis.RequireMajor(11)
	receiver := newCallbackReceiver(t, http.StatusOK)
	tool := &blockingDirectAPITool{Tool: opencode.New(), started: make(chan string, 1)}
	h := asyncHandler(t, server.NewRunRegistry(1), func() runner.Tool { return tool })

	submitted := submitAsync(t, h, callBody(receiver.server.URL, ""), "wm-shutdown")
	select {
	case <-tool.started:
	case <-time.After(15 * time.Second):
		t.Fatal("run never started")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	h.Shutdown(ctx)

	payload := receiver.await(t, 1)
	if payload.RunID != submitted.RunID {
		t.Errorf("callback run_id = %q, want %q", payload.RunID, submitted.RunID)
	}
	if payload.Status != runStatusFailure {
		t.Fatalf("callback status = %q, want failure", payload.Status)
	}
	if payload.Error == nil || payload.Error.Code != codeServerShutdown {
		t.Fatalf("callback error = %+v, want %s", payload.Error, codeServerShutdown)
	}
	if !payload.Error.Retryable {
		t.Error("server_shutdown reported as not retryable")
	}

	// One notice per run: the shutdown notice and the run's own outcome must
	// not both reach the receiver.
	time.Sleep(50 * time.Millisecond)
	if got := receiver.count(); got != 1 {
		t.Errorf("callback delivered %d times during shutdown, want 1", got)
	}
}

func TestAsyncShutdown_ReturnsWithinItsBudgetWhenTheReceiverHangs(t *testing.T) {
	chassis.RequireMajor(11)
	blocked := make(chan struct{})
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-blocked
	}))
	defer receiver.Close()
	defer close(blocked)

	tool := &blockingDirectAPITool{Tool: opencode.New(), started: make(chan string, 1)}
	h := asyncHandler(t, server.NewRunRegistry(1), func() runner.Tool { return tool })

	submitAsync(t, h, callBody(receiver.URL, ""), "")
	select {
	case <-tool.started:
	case <-time.After(15 * time.Second):
		t.Fatal("run never started")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	h.Shutdown(ctx)
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("shutdown took %s waiting on an unresponsive receiver", elapsed)
	}
}

// ---------------------------------------------------------------------------
// Concurrency
// ---------------------------------------------------------------------------

// Submissions, polls, lookups, and cancellations all touch the same store from
// different goroutines. Run with -race.
func TestAsyncRuns_ConcurrentSubmitsAndPolls(t *testing.T) {
	chassis.RequireMajor(11)
	installFakeOpenCode(t, "concurrent output")
	receiver := newCallbackReceiver(t, http.StatusOK)
	h := asyncHandler(t, server.NewRunRegistry(4), openCodeFactory())

	const runs = 12
	ids := make(chan string, runs)
	var submitters sync.WaitGroup
	for i := 0; i < runs; i++ {
		submitters.Add(1)
		go func(i int) {
			defer submitters.Done()
			corrID := fmt.Sprintf("wm-race-%d", i%3)
			ids <- submitAsync(t, h, callBody(receiver.server.URL, ""), corrID).RunID
		}(i)
	}

	stop := make(chan struct{})
	var readers sync.WaitGroup
	for i := 0; i < 4; i++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				h.ServeHTTP(httptest.NewRecorder(),
					httptest.NewRequest(http.MethodGet, "/v1/runs?correlation_id=wm-race-1", nil))
				for _, id := range h.async.list("") {
					h.ServeHTTP(httptest.NewRecorder(),
						httptest.NewRequest(http.MethodGet, "/v1/runs/"+id.RunID, nil))
					h.ServeHTTP(httptest.NewRecorder(),
						httptest.NewRequest(http.MethodGet, "/v1/runs/"+id.RunID+"/result", nil))
				}
			}
		}()
	}

	submitters.Wait()
	close(ids)

	var submitted []string
	for id := range ids {
		submitted = append(submitted, id)
	}
	if len(submitted) != runs {
		t.Fatalf("submitted %d runs, want %d", len(submitted), runs)
	}

	// Cancelling a few mid-flight races the runs' own completion; either
	// outcome is terminal and either is fine.
	for _, id := range submitted[:3] {
		do(t, h, http.MethodDelete, "/v1/runs/"+id)
	}

	for _, id := range submitted {
		pollUntil(t, h, id, runStatusSuccess, runStatusFailure)
	}
	close(stop)
	readers.Wait()

	if got := receiver.count(); got < runs {
		t.Errorf("callbacks delivered = %d, want at least one per run (%d)", got, runs)
	}
}
