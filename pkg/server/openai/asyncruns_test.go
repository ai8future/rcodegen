package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
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
func asyncHandler(t *testing.T, reg *server.RunRegistry, factory server.ToolFactory, opts ...HandlerOption) *Handler {
	t.Helper()
	h := NewHandler(nil, map[string]server.ToolFactory{"opencode": factory}, reg,
		[]string{"opencode"}, nil, nil, opts...)
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
// Admission
// ---------------------------------------------------------------------------

// submitRefused posts a chat completion that admission should turn away, and
// asserts the whole refusal contract: retryable 503, Retry-After, and — the
// part that matters most — no run_id, because a caller that receives one has
// been told about work the server never took.
func submitRefused(t *testing.T, h *Handler, body string) ErrorResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Retry-After"); got != "1" {
		t.Errorf("Retry-After = %q, want 1", got)
	}
	if strings.Contains(rec.Body.String(), "run_id") {
		t.Errorf("a refusal carries a run_id: %s", rec.Body.String())
	}
	var errResp ErrorResponse
	if err := json.NewDecoder(rec.Body).Decode(&errResp); err != nil {
		t.Fatalf("decode refusal: %v", err)
	}
	if errResp.Error.Code != codeAsyncCapacity {
		t.Errorf("code = %q, want %s", errResp.Error.Code, codeAsyncCapacity)
	}
	if !errResp.Error.Retryable {
		t.Error("async_capacity reported as not retryable; the caller has nothing to change")
	}
	return errResp
}

// health reads /health, which is where admission is visible to an operator.
func health(t *testing.T, h *Handler) HealthResponse {
	t.Helper()
	rec := do(t, h, http.MethodGet, "/health")
	if rec.Code != http.StatusOK {
		t.Fatalf("health status = %d", rec.Code)
	}
	var hr HealthResponse
	if err := json.NewDecoder(rec.Body).Decode(&hr); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	return hr
}

// awaitLiveAsync waits for the store's live count to fall to want, which is how
// a test observes that reservations were given back.
func awaitLiveAsync(t *testing.T, h *Handler, want int) HealthResponse {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		hr := health(t, h)
		if hr.AsyncLive == want {
			return hr
		}
		if time.Now().After(deadline) {
			t.Fatalf("async_live = %d, want %d", hr.AsyncLive, want)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// runCount returns how many runs the store is holding.
func runCount(t *testing.T, h *Handler) int {
	t.Helper()
	rec := do(t, h, http.MethodGet, "/v1/runs")
	var list RunList
	if err := json.NewDecoder(rec.Body).Decode(&list); err != nil {
		t.Fatalf("decode run list: %v", err)
	}
	return len(list.Data)
}

// Accepted async work outlives the connection that submitted it, so without an
// admission bound a caller can leave behind as many goroutines and retained
// plans as it likes. The bound is enforced before the 202, not after it.
func TestAsyncAdmission_RefusesPastTheLiveRunLimit(t *testing.T) {
	chassis.RequireMajor(11)
	installFakeOpenCode(t, "admitted output")
	receiver := newCallbackReceiver(t, http.StatusOK)
	reg := server.NewRunRegistry(1)
	const maxLive = 3
	h := asyncHandler(t, reg, openCodeFactory(),
		WithAsyncLimits(AsyncLimits{MaxLive: maxLive, MaxBytes: 1 << 20}))

	// Hold the only run slot so every admitted run stays queued and keeps its
	// reservation.
	heldID, _, heldCancel, err := reg.Acquire(context.Background(), "opencode", "held")
	if err != nil {
		t.Fatalf("acquire holding slot: %v", err)
	}

	for i := 0; i < maxLive; i++ {
		submitAsync(t, h, callBody(receiver.server.URL, ""), fmt.Sprintf("wm-admit-%d", i))
	}
	hr := health(t, h)
	if hr.AsyncLive != maxLive || hr.AsyncMaxLive != maxLive {
		t.Errorf("health async_live/async_max_live = %d/%d, want %d/%d",
			hr.AsyncLive, hr.AsyncMaxLive, maxLive, maxLive)
	}

	errResp := submitRefused(t, h, callBody(receiver.server.URL, ""))
	if !strings.Contains(errResp.Error.Message, "RSERVE_ASYNC_MAX_LIVE") {
		t.Errorf("refusal does not name the limit that refused it: %s", errResp.Error.Message)
	}

	// Nothing was created for the refused submission: no run to poll, and the
	// live count is unmoved.
	if got := runCount(t, h); got != maxLive {
		t.Errorf("store holds %d runs after a refusal, want %d", got, maxLive)
	}
	if got := health(t, h).AsyncLive; got != maxLive {
		t.Errorf("async_live = %d after a refusal, want %d", got, maxLive)
	}
	if got := reg.QueuedCount(); got > maxLive {
		t.Errorf("%d requests waiting for a slot, want at most the %d admitted", got, maxLive)
	}

	// Let the admitted work run: exactly the admitted runs deliver, so the
	// refused one started no goroutine either.
	heldCancel()
	reg.Release(heldID)
	receiver.await(t, maxLive)
	time.Sleep(50 * time.Millisecond)
	if got := receiver.count(); got != maxLive {
		t.Errorf("callbacks delivered = %d, want exactly the %d admitted", got, maxLive)
	}
}

// A count bound alone would let a few maximum-size requests hold far more
// memory than intended, so retained request payload is bounded too.
func TestAsyncAdmission_RefusesPastTheRetainedByteLimit(t *testing.T) {
	chassis.RequireMajor(11)
	installFakeOpenCode(t, "small output")
	receiver := newCallbackReceiver(t, http.StatusOK)
	reg := server.NewRunRegistry(1)
	h := asyncHandler(t, reg, openCodeFactory(),
		WithAsyncLimits(AsyncLimits{MaxLive: 64, MaxBytes: 8 << 10}))

	heldID, _, heldCancel, err := reg.Acquire(context.Background(), "opencode", "held")
	if err != nil {
		t.Fatalf("acquire holding slot: %v", err)
	}
	defer func() {
		heldCancel()
		reg.Release(heldID)
	}()

	// One task larger than the whole budget is refused on an idle server, which
	// is the reservation being made per request rather than only in aggregate.
	huge := `{"model":"opencode","messages":[{"role":"user","content":"` +
		strings.Repeat("t", 16<<10) + `"}],"callback_url":"` + receiver.server.URL + `"}`
	errResp := submitRefused(t, h, huge)
	if !strings.Contains(errResp.Error.Message, "RSERVE_ASYNC_MAX_BYTES") {
		t.Errorf("refusal does not name the limit that refused it: %s", errResp.Error.Message)
	}
	if got := health(t, h); got.AsyncLive != 0 || got.AsyncBytes != 0 {
		t.Errorf("a refused submission reserved %d runs / %d bytes", got.AsyncLive, got.AsyncBytes)
	}

	// A small one fits.
	submitAsync(t, h, callBody(receiver.server.URL, ""), "wm-bytes-small")
	hr := health(t, h)
	if hr.AsyncLive != 1 {
		t.Fatalf("async_live = %d after an admitted submission, want 1", hr.AsyncLive)
	}
	if hr.AsyncBytes <= 0 || hr.AsyncBytes > hr.AsyncMaxBytes {
		t.Errorf("async_bytes = %d, want a positive charge within the %d budget", hr.AsyncBytes, hr.AsyncMaxBytes)
	}

	// And the budget accumulates: what one live run holds is not available to
	// the next.
	nearly := `{"model":"opencode","messages":[{"role":"user","content":"` +
		strings.Repeat("t", 5<<10) + `"}],"callback_url":"` + receiver.server.URL + `"}`
	submitRefused(t, h, nearly)
	if got := health(t, h).AsyncLive; got != 1 {
		t.Errorf("async_live = %d, want the 1 admitted run", got)
	}
}

// A reservation is held for as long as the goroutine holds the plan and is
// given back exactly once — whether the run completed or was cancelled — or the
// server would refuse work it has the room for.
func TestAsyncAdmission_ReservationsAreReleasedOnceAndReused(t *testing.T) {
	chassis.RequireMajor(11)
	installFakeOpenCode(t, "released output")
	receiver := newCallbackReceiver(t, http.StatusOK)
	h := asyncHandler(t, server.NewRunRegistry(1), openCodeFactory(),
		WithAsyncLimits(AsyncLimits{MaxLive: 1, MaxBytes: 1 << 20}))

	// A completed run gives its reservation back.
	submitAsync(t, h, callBody(receiver.server.URL, ""), "wm-release-done")
	receiver.await(t, 1)
	hr := awaitLiveAsync(t, h, 0)
	if hr.AsyncBytes != 0 {
		t.Errorf("async_bytes = %d after the only run finished, want 0", hr.AsyncBytes)
	}

	// The freed capacity is usable, and one release did not credit two.
	second := submitAsync(t, h, callBody(receiver.server.URL, ""), "wm-release-again")
	receiver.await(t, 2)
	awaitLiveAsync(t, h, 0)
	if got := health(t, h).AsyncLive; got != 0 {
		t.Errorf("async_live = %d, want 0", got)
	}
	pollUntil(t, h, second.RunID, runStatusSuccess)

	// So is the capacity a cancelled run gives back.
	tool := &blockingDirectAPITool{Tool: opencode.New(), started: make(chan string, 1)}
	cancelH := asyncHandler(t, server.NewRunRegistry(1), func() runner.Tool { return tool },
		WithAsyncLimits(AsyncLimits{MaxLive: 1, MaxBytes: 1 << 20}))
	cancelled := submitAsync(t, cancelH, callBody(receiver.server.URL, ""), "wm-release-cancel")
	select {
	case <-tool.started:
	case <-time.After(15 * time.Second):
		t.Fatal("run never started")
	}
	if got := health(t, cancelH).AsyncLive; got != 1 {
		t.Fatalf("async_live = %d while a run is in flight, want 1", got)
	}
	if rec := do(t, cancelH, http.MethodDelete, "/v1/runs/"+cancelled.RunID); rec.Code != http.StatusNoContent {
		t.Fatalf("cancel status = %d", rec.Code)
	}
	pollUntil(t, cancelH, cancelled.RunID, runStatusFailure)
	if hr := awaitLiveAsync(t, cancelH, 0); hr.AsyncBytes != 0 {
		t.Errorf("async_bytes = %d after cancellation, want 0", hr.AsyncBytes)
	}
}

// Submissions, cancellations, and completions all move the same two counters
// from different goroutines. Neither may go negative or past its bound. Run
// with -race.
func TestAsyncAdmission_CountersHoldUnderConcurrentSubmitCancelFinish(t *testing.T) {
	chassis.RequireMajor(11)
	installFakeOpenCode(t, "concurrent admission")
	receiver := newCallbackReceiver(t, http.StatusOK)
	const maxLive = 6
	h := asyncHandler(t, server.NewRunRegistry(3), openCodeFactory(),
		WithAsyncLimits(AsyncLimits{MaxLive: maxLive, MaxBytes: 256 << 10}))

	stop := make(chan struct{})
	var watchers sync.WaitGroup
	watchers.Add(1)
	go func() {
		defer watchers.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			// Read the counters directly rather than through /health: this
			// runs off the test goroutine, where a helper's t.Fatalf would
			// not fail the test properly.
			st := h.async.stats()
			if st.live < 0 || st.live > maxLive {
				t.Errorf("live async runs = %d, outside [0, %d]", st.live, maxLive)
				return
			}
			if st.bytes < 0 || st.bytes > st.maxBytes {
				t.Errorf("live async bytes = %d, outside [0, %d]", st.bytes, st.maxBytes)
				return
			}
		}
	}()

	var submitters sync.WaitGroup
	var admitted sync.Map
	for i := 0; i < 24; i++ {
		submitters.Add(1)
		go func(i int) {
			defer submitters.Done()
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
				strings.NewReader(callBody(receiver.server.URL, ""))))
			switch rec.Code {
			case http.StatusAccepted:
				var resp AsyncSubmitResponse
				if err := json.NewDecoder(rec.Body).Decode(&resp); err == nil {
					admitted.Store(resp.RunID, struct{}{})
					// Cancelling some of them races their own completion; either
					// outcome is terminal and either releases exactly once.
					if i%3 == 0 {
						do(t, h, http.MethodDelete, "/v1/runs/"+resp.RunID)
					}
				}
			case http.StatusServiceUnavailable:
				// Refused: the bound doing its job.
			default:
				t.Errorf("submission status = %d, want 202 or 503", rec.Code)
			}
		}(i)
	}
	submitters.Wait()

	admitted.Range(func(key, _ any) bool {
		pollUntil(t, h, key.(string), runStatusSuccess, runStatusFailure)
		return true
	})
	close(stop)
	watchers.Wait()

	hr := awaitLiveAsync(t, h, 0)
	if hr.AsyncBytes != 0 {
		t.Errorf("async_bytes = %d once every run is terminal, want 0", hr.AsyncBytes)
	}
}

// Shutdown delivers one notice per admitted run and no more: the live bound is
// also the bound on the callback goroutines the exit path can create.
func TestAsyncAdmission_ShutdownDeliversNoMoreThanTheAdmittedRuns(t *testing.T) {
	chassis.RequireMajor(11)
	installFakeOpenCode(t, "never gets a slot")
	receiver := newCallbackReceiver(t, http.StatusOK)
	reg := server.NewRunRegistry(1)
	const maxLive = 3
	h := asyncHandler(t, reg, openCodeFactory(),
		WithAsyncLimits(AsyncLimits{MaxLive: maxLive, MaxBytes: 1 << 20}))

	heldID, _, heldCancel, err := reg.Acquire(context.Background(), "opencode", "held")
	if err != nil {
		t.Fatalf("acquire holding slot: %v", err)
	}
	defer func() {
		heldCancel()
		reg.Release(heldID)
	}()

	for i := 0; i < maxLive; i++ {
		submitAsync(t, h, callBody(receiver.server.URL, ""), fmt.Sprintf("wm-shut-%d", i))
	}
	submitRefused(t, h, callBody(receiver.server.URL, ""))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	h.Shutdown(ctx)

	receiver.await(t, maxLive)
	time.Sleep(50 * time.Millisecond)
	if got := receiver.count(); got != maxLive {
		t.Errorf("shutdown delivered %d callbacks, want the %d admitted runs", got, maxLive)
	}

	// The store is closed: nothing joins the set being torn down.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(callBody(receiver.server.URL, ""))))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("submission after shutdown = %d, want 503; body = %s", rec.Code, rec.Body.String())
	}
	var errResp ErrorResponse
	if err := json.NewDecoder(rec.Body).Decode(&errResp); err != nil {
		t.Fatalf("decode refusal: %v", err)
	}
	if errResp.Error.Code != codeServerShutdown {
		t.Errorf("code = %q, want %s", errResp.Error.Code, codeServerShutdown)
	}
	if !errResp.Error.Retryable {
		t.Error("server_shutdown reported as not retryable")
	}
}

func TestDefaultAsyncLimits(t *testing.T) {
	// The live bound scales with the slot count, with a floor for the
	// single-slot deployments the suite actually runs.
	for _, tc := range []struct{ slots, want int }{{1, 8}, {2, 8}, {3, 12}, {8, 32}} {
		if got := DefaultAsyncLimits(tc.slots).MaxLive; got != tc.want {
			t.Errorf("DefaultAsyncLimits(%d).MaxLive = %d, want %d", tc.slots, got, tc.want)
		}
	}
	if got := DefaultAsyncLimits(3).MaxBytes; got != asyncMaxBytesDefault {
		t.Errorf("MaxBytes = %d, want %d", got, asyncMaxBytesDefault)
	}
}

// The estimate is what the byte bound is made of, so it has to count the parts
// of a request that are actually large.
func TestAsyncPlanBytes_CountsTheRetainedStrings(t *testing.T) {
	base := asyncPlanBytes(&chatPlan{cfg: runner.NewConfig()})
	if base <= 0 {
		t.Fatalf("an empty plan estimates %d bytes, want a positive allowance", base)
	}

	cfg := runner.NewConfig()
	cfg.Task = strings.Repeat("t", 4096)
	withTask := asyncPlanBytes(&chatPlan{cfg: cfg})
	if withTask-base < 4096 {
		t.Errorf("a 4096-byte task added %d bytes to the estimate", withTask-base)
	}

	withCallback := asyncPlanBytes(&chatPlan{
		cfg:      cfg,
		callback: &callbackTarget{url: strings.Repeat("u", 512), headers: map[string]string{"Authorization": strings.Repeat("k", 256)}},
	})
	if withCallback <= withTask {
		t.Error("callback URL and headers are not counted")
	}
	if got := asyncPlanBytes(nil); got <= 0 {
		t.Errorf("asyncPlanBytes(nil) = %d, want the base allowance", got)
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
	// example.com is resolved by the plaintext check, so the answer comes from
	// here rather than from whatever DNS the test machine happens to have.
	stubCallbackDNS(t, func(string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("93.184.216.34")}, nil
	})
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
	// None of these reach the resolver — they are IP literals or the localhost
	// name — so the stub is here to prove that, by failing loudly if one does.
	stubCallbackDNS(t, func(host string) ([]net.IP, error) {
		t.Errorf("resolved %q; IP literals and localhost answer for themselves", host)
		return nil, errors.New("unexpected lookup")
	})
	for _, raw := range []string{
		"https://windmill.example.com/api/w/aows/jobs_u/resume/1/2/sig",
		"http://127.0.0.1:9000/resume",
		"http://localhost:9000/resume",
		"http://10.1.2.3/resume",
		"http://192.168.1.10:8080/resume",
		"http://172.16.4.4/resume",
	} {
		if _, err := newCallbackTarget(context.Background(), raw, nil); err != nil {
			t.Errorf("newCallbackTarget(%q) = %v, want accepted", raw, err)
		}
	}
	for _, raw := range []string{
		"http://8.8.8.8/resume",
		"http://172.32.0.1/resume",      // just outside RFC1918's 172.16/12
		"http://169.254.169.254/resume", // the cloud metadata endpoint
		"http://0.0.0.0/resume",         // not a receiver at all
	} {
		if _, err := newCallbackTarget(context.Background(), raw, nil); err == nil {
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
// Plaintext callback host policy
// ---------------------------------------------------------------------------

// stubCallbackDNS answers callback host lookups from resolve instead of the
// machine's resolver, so a test can stand a name in front of a local receiver.
//
// Call it before asyncHandler: cleanups run last-registered-first, so the
// handler's shutdown — which drains any delivery still in flight — has to be
// registered after this one to finish before the hook is put back.
func stubCallbackDNS(t *testing.T, resolve func(host string) ([]net.IP, error)) {
	t.Helper()
	restore := callbackLookupIP
	callbackLookupIP = func(_ context.Context, host string) ([]net.IP, error) {
		return resolve(host)
	}
	t.Cleanup(func() { callbackLookupIP = restore })
}

// recordCallbackDials wraps the delivery dialer and returns an accessor for the
// addresses it was asked to connect to — which is how a test proves that a
// refused delivery never reached the network at all.
func recordCallbackDials(t *testing.T) func() []string {
	t.Helper()
	var mu sync.Mutex
	var dialed []string
	restore := callbackDialIP
	callbackDialIP = func(ctx context.Context, network, addr string) (net.Conn, error) {
		mu.Lock()
		dialed = append(dialed, addr)
		mu.Unlock()
		return restore(ctx, network, addr)
	}
	t.Cleanup(func() { callbackDialIP = restore })
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), dialed...)
	}
}

// receiverHost rewrites a receiver's URL to be reached through name instead of
// the loopback literal httptest bound to, keeping the port.
func receiverHost(t *testing.T, rawURL, name string) (target string, port string) {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse receiver URL: %v", err)
	}
	port = u.Port()
	u.Host = net.JoinHostPort(name, port)
	u.Path = "/resume"
	return u.String(), port
}

// The blocker this policy exists for: a Windmill resume URL at
// http://windmill.10.0.4.224.nip.io/... — a name, not a literal, that resolves
// inside the private network. It has to be accepted at submit and delivered.
func TestAsyncCallback_DeliversToAPrivateResolvingHostname(t *testing.T) {
	chassis.RequireMajor(11)
	installFakeOpenCode(t, "nip.io callback output")
	receiver := newCallbackReceiver(t, http.StatusOK)

	const name = "windmill.10.0.4.224.nip.io"
	target, port := receiverHost(t, receiver.server.URL, name)
	loopback := net.ParseIP("127.0.0.1")
	stubCallbackDNS(t, func(host string) ([]net.IP, error) {
		if host != name {
			return nil, fmt.Errorf("unexpected host %q", host)
		}
		return []net.IP{loopback}, nil
	})
	dials := recordCallbackDials(t)
	h := asyncHandler(t, server.NewRunRegistry(1), openCodeFactory())

	submitted := submitAsync(t, h, callBody(target, ""), "wm-nipio")
	payload := receiver.await(t, 1)

	if payload.RunID != submitted.RunID {
		t.Errorf("callback run_id = %q, want %q", payload.RunID, submitted.RunID)
	}
	if payload.Status != runStatusSuccess {
		t.Errorf("callback status = %q, want success", payload.Status)
	}
	if len(payload.Choices) != 1 || payload.Choices[0].Message == nil {
		t.Fatalf("callback choices = %+v", payload.Choices)
	}
	if got := payload.Choices[0].Message.Content; got != "nip.io callback output" {
		t.Errorf("callback content = %q, want the CLI's output", got)
	}

	// The dialer pinned the vetted address rather than handing the name back to
	// the transport, so the connection went to the IP the policy approved.
	want := net.JoinHostPort("127.0.0.1", port)
	if got := dials(); len(got) != 1 || got[0] != want {
		t.Errorf("dialed %v, want exactly [%s]", got, want)
	}
}

// The control this fix is really about: a host that passes validation and then
// answers with a public address before the callback is delivered. The run is
// long enough to make that window real, so it is closed at the dialer.
func TestAsyncCallback_RebindingToAPublicAddressIsRefusedAtDialTime(t *testing.T) {
	chassis.RequireMajor(11)
	installFakeOpenCode(t, "rebinding callback output")
	receiver := newCallbackReceiver(t, http.StatusOK)

	const name = "rebind.internal.example"
	target, _ := receiverHost(t, receiver.server.URL, name)
	var lookups atomic.Int32
	stubCallbackDNS(t, func(host string) ([]net.IP, error) {
		if host != name {
			return nil, fmt.Errorf("unexpected host %q", host)
		}
		// The first answer is the one submit-time validation sees; every answer
		// after it — the delivery attempts — has rebound to a public address.
		if lookups.Add(1) == 1 {
			return []net.IP{net.ParseIP("127.0.0.1")}, nil
		}
		return []net.IP{net.ParseIP("203.0.113.10")}, nil
	})
	dials := recordCallbackDials(t)
	h := asyncHandler(t, server.NewRunRegistry(1), openCodeFactory())

	submitted := submitAsync(t, h, callBody(target, ""), "wm-rebind")
	pollUntil(t, h, submitted.RunID, runStatusSuccess)

	// One lookup validated the submission and one more per delivery attempt was
	// refused; waiting for them all is what makes the assertions below settled
	// rather than merely early.
	attempts := len(h.async.backoff) + 1
	deadline := time.Now().Add(20 * time.Second)
	for int(lookups.Load()) < attempts+1 {
		if time.Now().After(deadline) {
			t.Fatalf("delivery made %d lookups, want %d", lookups.Load(), attempts+1)
		}
		time.Sleep(time.Millisecond)
	}

	if got := dials(); len(got) != 0 {
		t.Errorf("a refused delivery still dialed %v", got)
	}
	if got := receiver.count(); got != 0 {
		t.Errorf("receiver took %d callbacks; the rebound delivery reached the network", got)
	}

	// The run itself succeeded and its result stays pollable: the refusal is a
	// delivery failure, not a run failure.
	rec := do(t, h, http.MethodGet, "/v1/runs/"+submitted.RunID+"/result")
	if rec.Code != http.StatusOK {
		t.Fatalf("result status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var retained AsyncCompletion
	if err := json.NewDecoder(rec.Body).Decode(&retained); err != nil {
		t.Fatalf("decode retained result: %v", err)
	}
	if retained.Status != runStatusSuccess {
		t.Errorf("retained status = %q, want success", retained.Status)
	}
}

func TestAsyncSubmit_RejectsHostnamesThatDoNotResolvePrivately(t *testing.T) {
	chassis.RequireMajor(11)
	installFakeOpenCode(t, "never runs")

	cases := []struct {
		name    string
		host    string
		resolve func() ([]net.IP, error)
	}{
		{
			name:    "resolves to a public address",
			host:    "public.example",
			resolve: func() ([]net.IP, error) { return []net.IP{net.ParseIP("203.0.113.10")}, nil },
		},
		{
			name: "resolves to a private and a public address",
			host: "split.example",
			resolve: func() ([]net.IP, error) {
				return []net.IP{net.ParseIP("10.0.4.224"), net.ParseIP("203.0.113.10")}, nil
			},
		},
		{
			name:    "does not resolve",
			host:    "nxdomain.example",
			resolve: func() ([]net.IP, error) { return nil, errors.New("no such host") },
		},
		{
			name:    "resolves to nothing",
			host:    "empty.example",
			resolve: func() ([]net.IP, error) { return nil, nil },
		},
		{
			name:    "resolves to the metadata endpoint",
			host:    "metadata.example",
			resolve: func() ([]net.IP, error) { return []net.IP{net.ParseIP("169.254.169.254")}, nil },
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stubCallbackDNS(t, func(string) ([]net.IP, error) { return c.resolve() })
			h := asyncHandler(t, server.NewRunRegistry(1), openCodeFactory())

			body := callBody("http://"+c.host+"/resume", "")
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
			if errResp.Error.Code != codeInvalidCallbackURL {
				t.Errorf("code = %q, want %s", errResp.Error.Code, codeInvalidCallbackURL)
			}
			// The message has to name the shape that would have worked, or the
			// caller reads it as "hostnames are never allowed".
			if !strings.Contains(errResp.Error.Message, "resolves to one") {
				t.Errorf("message does not offer private-resolving hostnames: %s", errResp.Error.Message)
			}
		})
	}
}

// A private receiver that answers a callback with a redirect to a public one
// would launder the payload past the policy. The client refuses to follow it.
func TestAsyncCallback_DoesNotFollowRedirects(t *testing.T) {
	chassis.RequireMajor(11)
	installFakeOpenCode(t, "redirected callback output")

	elsewhere := newCallbackReceiver(t, http.StatusOK)
	var redirects atomic.Int32
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirects.Add(1)
		http.Redirect(w, r, elsewhere.server.URL+"/resume", http.StatusFound)
	}))
	t.Cleanup(redirector.Close)

	h := asyncHandler(t, server.NewRunRegistry(1), openCodeFactory())
	submitted := submitAsync(t, h, callBody(redirector.URL+"/resume", ""), "wm-redirect")
	pollUntil(t, h, submitted.RunID, runStatusSuccess)

	// Delivery is over once every attempt has been refused — or, if the redirect
	// were followed, as soon as the target takes the payload. Either ends the
	// wait, so the leak is what gets reported rather than a timeout.
	attempts := len(h.async.backoff) + 1
	deadline := time.Now().Add(20 * time.Second)
	for int(redirects.Load()) < attempts && elsewhere.count() == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("redirecting receiver saw %d attempts, want %d", redirects.Load(), attempts)
		}
		time.Sleep(time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)

	if got := elsewhere.count(); got != 0 {
		t.Errorf("the redirect target took %d callbacks; the payload was handed on", got)
	}
	// A 302 is a failed attempt, retried and then given up on — never followed.
	if got := int(redirects.Load()); got != attempts {
		t.Errorf("redirecting receiver saw %d attempts, want %d", got, attempts)
	}
}

// ---------------------------------------------------------------------------
// Ambient proxies
// ---------------------------------------------------------------------------

// recordingProxy stands in for the HTTP proxy an operator points HTTP_PROXY at.
// It answers forwarded requests itself and keeps what it was handed, so a test
// can tell a direct delivery from one that went through the proxy — and can say
// exactly what a proxy would have learned if it had.
type recordingProxy struct {
	server *httptest.Server

	mu      sync.Mutex
	targets []string
	bodies  []string
	headers []http.Header
}

func newRecordingProxy(t *testing.T) *recordingProxy {
	t.Helper()
	p := &recordingProxy{}
	p.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		p.mu.Lock()
		// A proxied request carries the absolute URL of the real target, which
		// is the disclosure that matters: for a Windmill resume URL that string
		// is itself the credential.
		p.targets = append(p.targets, r.RequestURI)
		p.bodies = append(p.bodies, string(body))
		p.headers = append(p.headers, r.Header.Clone())
		p.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(p.server.Close)
	return p
}

func (p *recordingProxy) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.targets)
}

// saw reports what the proxy was handed on its first forwarded request.
func (p *recordingProxy) saw(t *testing.T) (target, body string, headers http.Header) {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.targets) == 0 {
		t.Fatal("the proxy forwarded nothing")
	}
	return p.targets[0], p.bodies[0], p.headers[0]
}

// useProxyEnvironment points every ambient proxy variable at p and clears the
// bypass list, the shape a deployment behind a corporate or LAN proxy has.
func useProxyEnvironment(t *testing.T, p *recordingProxy) {
	t.Helper()
	for _, name := range []string{"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy"} {
		t.Setenv(name, p.server.URL)
	}
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "")
}

// The delivery-time private-address check is only worth anything if it inspects
// the callback host. Under HTTP_PROXY the transport asks the dialer for the
// proxy instead, so the check would vet the proxy and hand it the payload. The
// plaintext client therefore refuses to inherit ambient proxies at all.
func TestAsyncCallback_PlaintextDeliveryIgnoresTheAmbientProxy(t *testing.T) {
	chassis.RequireMajor(11)
	installFakeOpenCode(t, "direct delivery output")
	proxy := newRecordingProxy(t)
	useProxyEnvironment(t, proxy)
	receiver := newCallbackReceiver(t, http.StatusOK)

	const name = "windmill.10.0.4.224.nip.io"
	target, port := receiverHost(t, receiver.server.URL, name)
	stubCallbackDNS(t, func(host string) ([]net.IP, error) {
		// The proxy is reached by its loopback literal, and the control leg
		// below dials it through the same policy, so literals answer for
		// themselves here the way the real resolver answers for them.
		if ip := net.ParseIP(host); ip != nil {
			return []net.IP{ip}, nil
		}
		if host != name {
			return nil, fmt.Errorf("unexpected host %q", host)
		}
		return []net.IP{net.ParseIP("127.0.0.1")}, nil
	})
	dials := recordCallbackDials(t)
	h := asyncHandler(t, server.NewRunRegistry(1), openCodeFactory())

	submitted := submitAsync(t, h, callBody(target, `"callback_headers":{"Authorization":"Bearer resume-secret"}`), "wm-proxy")
	payload := receiver.await(t, 1)

	if payload.RunID != submitted.RunID {
		t.Errorf("callback run_id = %q, want %q", payload.RunID, submitted.RunID)
	}
	if got := receiver.count(); got != 1 {
		t.Errorf("receiver took %d callbacks, want exactly 1", got)
	}
	if got := proxy.count(); got != 0 {
		t.Errorf("the proxy forwarded %d callbacks; the payload went through it", got)
	}
	// The connection went to the address callbackLookupIP approved during this
	// attempt, not to the proxy the environment named.
	want := net.JoinHostPort("127.0.0.1", port)
	if got := dials(); len(got) != 1 || got[0] != want {
		t.Errorf("dialed %v, want exactly [%s]", got, want)
	}

	// What the fix is worth: a transport that does inherit the ambient proxy
	// hands that proxy the absolute callback URL, the completion, and the
	// caller's own Authorization header — while the private-address dialer,
	// asked for the proxy's address rather than the receiver's, approves it.
	leaky := http.DefaultTransport.(*http.Transport).Clone()
	leaky.DialContext = dialPlaintextCallback
	leaky.Proxy = func(*http.Request) (*url.URL, error) { return url.Parse(os.Getenv("HTTP_PROXY")) }
	req, err := http.NewRequest(http.MethodPost, target, strings.NewReader(`{"run_id":"leak"}`))
	if err != nil {
		t.Fatalf("build control request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer resume-secret")
	resp, err := (&http.Client{Transport: leaky, Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("control delivery through the ambient proxy: %v", err)
	}
	resp.Body.Close()

	if got := proxy.count(); got != 1 {
		t.Fatalf("the control delivery reached the proxy %d times, want 1; "+
			"the proxy is not live, so this test proves nothing", got)
	}
	gotTarget, gotBody, gotHeaders := proxy.saw(t)
	if gotTarget != target {
		t.Errorf("proxy was handed %q, want the callback URL %q", gotTarget, target)
	}
	if !strings.Contains(gotBody, "leak") {
		t.Errorf("proxy body = %q, want the delivered payload", gotBody)
	}
	if got := gotHeaders.Get("Authorization"); got != "Bearer resume-secret" {
		t.Errorf("proxy Authorization = %q, want the caller's header", got)
	}
	// And the receiver still saw only the one real delivery.
	if got := receiver.count(); got != 1 {
		t.Errorf("receiver took %d callbacks after the control, want 1", got)
	}
}

// The rebinding refusal has to hold under the same environment: a proxy must
// not become the way a host that rebound to a public address gets reached.
func TestAsyncCallback_RebindingIsRefusedWithAProxyConfigured(t *testing.T) {
	chassis.RequireMajor(11)
	installFakeOpenCode(t, "rebinding under proxy")
	proxy := newRecordingProxy(t)
	useProxyEnvironment(t, proxy)
	receiver := newCallbackReceiver(t, http.StatusOK)

	const name = "rebind-proxy.internal.example"
	target, _ := receiverHost(t, receiver.server.URL, name)
	var lookups atomic.Int32
	stubCallbackDNS(t, func(host string) ([]net.IP, error) {
		if host != name {
			return nil, fmt.Errorf("unexpected host %q", host)
		}
		if lookups.Add(1) == 1 {
			return []net.IP{net.ParseIP("127.0.0.1")}, nil
		}
		return []net.IP{net.ParseIP("203.0.113.10")}, nil
	})
	dials := recordCallbackDials(t)
	h := asyncHandler(t, server.NewRunRegistry(1), openCodeFactory())

	submitted := submitAsync(t, h, callBody(target, ""), "wm-rebind-proxy")
	pollUntil(t, h, submitted.RunID, runStatusSuccess)

	attempts := len(h.async.backoff) + 1
	deadline := time.Now().Add(20 * time.Second)
	for int(lookups.Load()) < attempts+1 {
		if time.Now().After(deadline) {
			t.Fatalf("delivery made %d lookups, want %d", lookups.Load(), attempts+1)
		}
		time.Sleep(time.Millisecond)
	}

	if got := proxy.count(); got != 0 {
		t.Errorf("the proxy forwarded %d rebound callbacks", got)
	}
	if got := dials(); len(got) != 0 {
		t.Errorf("a refused delivery still dialed %v", got)
	}
	if got := receiver.count(); got != 0 {
		t.Errorf("receiver took %d callbacks; the rebound delivery reached the network", got)
	}
}

// The two clients differ on purpose: plaintext is direct-only because its
// safety comes from the address it dials, https keeps ordinary proxy behavior
// because its safety comes from the certificate.
func TestNewCallbackClient_PlaintextTransportCarriesNoProxy(t *testing.T) {
	plain := newCallbackClient(dialPlaintextCallback).Transport.(*http.Transport)
	if plain.Proxy != nil {
		t.Error("the plaintext delivery transport inherits a proxy")
	}
	if plain.DialContext == nil {
		t.Error("the plaintext delivery transport lost its address policy")
	}
	secure := newCallbackClient(nil).Transport.(*http.Transport)
	if secure.Proxy == nil {
		t.Error("the https delivery transport dropped ordinary proxy support")
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

// testAsyncRuns builds a store with admission limits generous enough that
// retention, not admission, is what a retention test is measuring.
func testAsyncRuns() *asyncRuns {
	return newAsyncRuns(AsyncLimits{MaxLive: 64, MaxBytes: 1 << 20})
}

// submitTestRun registers a run directly, for tests that should not depend on
// executing anything.
func submitTestRun(t *testing.T, a *asyncRuns, correlationID string) *asyncRun {
	t.Helper()
	run, err := a.submit(correlationID, nil, 0)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	return run
}

// finishTestRun registers and finishes a run directly, releasing its admission
// the way the execution goroutine would.
func finishTestRun(t *testing.T, a *asyncRuns, correlationID string) *asyncRun {
	t.Helper()
	run := submitTestRun(t, a, correlationID)
	a.tryTerminalize(run, runStatusSuccess, &AsyncCompletion{RunID: run.id, Status: runStatusSuccess})
	a.releaseAdmission(run)
	return run
}

func TestAsyncRetention_EvictsLeastRecentlyUsedOverTheCap(t *testing.T) {
	a := testAsyncRuns()
	a.cap = 2

	first := finishTestRun(t, a, "")
	second := finishTestRun(t, a, "")

	// Touching the first makes the second the least recently used.
	if _, ok := a.summary(first.id); !ok {
		t.Fatal("first run evicted early")
	}
	third := finishTestRun(t, a, "")

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
	a := testAsyncRuns()
	a.ttl = time.Hour
	now := time.Now()
	a.now = func() time.Time { return now }

	run := finishTestRun(t, a, "")
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
	a := testAsyncRuns()
	a.cap = 1
	a.ttl = time.Nanosecond

	live := submitTestRun(t, a, "")
	for i := 0; i < 5; i++ {
		finishTestRun(t, a, "")
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
// Terminal ownership
// ---------------------------------------------------------------------------

// Terminalization is the whole state machine, so a run can end exactly once and
// only the caller that ended it may speak for it.
func TestAsyncTerminal_OnlyOneCallerWinsTheTransition(t *testing.T) {
	a := testAsyncRuns()
	run := submitTestRun(t, a, "")

	const racers = 8
	var wins atomic.Int32
	var winner atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			payload := &AsyncCompletion{RunID: run.id, Status: runStatusSuccess, OutputTruncated: i%2 == 0}
			payload.Model = fmt.Sprintf("racer-%d", i)
			if a.tryTerminalize(run, runStatusSuccess, payload) {
				wins.Add(1)
				winner.Store(int32(i))
			}
		}(i)
	}
	close(start)
	wg.Wait()

	if got := wins.Load(); got != 1 {
		t.Fatalf("%d callers won terminal ownership, want exactly 1", got)
	}
	result, status, ok := a.result(run.id)
	if !ok || result == nil {
		t.Fatal("the run kept no result")
	}
	if status != runStatusSuccess {
		t.Errorf("status = %q, want success", status)
	}
	// The retained result is the winner's payload, not a later caller's.
	if want := fmt.Sprintf("racer-%d", winner.Load()); result.Model != want {
		t.Errorf("retained result = %q, want the winner's %q", result.Model, want)
	}

	// And a terminal run stays terminal: a straggler cannot reopen it.
	if a.tryTerminalize(run, runStatusFailure, &AsyncCompletion{RunID: run.id, Status: runStatusFailure}) {
		t.Error("a terminal run was terminalized a second time")
	}
	if _, status, _ := a.result(run.id); status != runStatusSuccess {
		t.Errorf("status = %q after a losing transition, want success", status)
	}
}

// The shape the shutdown race used to produce: shutdown wins, then the worker
// finishes. The worker's outcome must not overwrite what the receiver was
// already told.
func TestAsyncTerminal_ShutdownWinnerIsNotOverwrittenByTheWorker(t *testing.T) {
	a := testAsyncRuns()
	run := submitTestRun(t, a, "")

	shutdownDetail := NewErrorResponse("server shut down", "server_error", codeServerShutdown).Error
	shutdown := &AsyncCompletion{RunID: run.id, Status: runStatusFailure, Error: &shutdownDetail}
	if !a.tryTerminalize(run, runStatusFailure, shutdown) {
		t.Fatal("shutdown did not win an untouched run")
	}

	success := &AsyncCompletion{RunID: run.id, Status: runStatusSuccess}
	if a.tryTerminalize(run, runStatusSuccess, success) {
		t.Error("a worker completion overwrote the shutdown outcome")
	}
	cancelDetail := NewErrorResponse("cancelled", "invalid_request_error", codeRunCancelled).Error
	if a.tryTerminalize(run, runStatusFailure, &AsyncCompletion{RunID: run.id, Status: runStatusFailure, Error: &cancelDetail}) {
		t.Error("a cancellation overwrote the shutdown outcome")
	}

	result, status, _ := a.result(run.id)
	if status != runStatusFailure || result == nil || result.Error == nil {
		t.Fatalf("retained status = %q, result = %+v", status, result)
	}
	if result.Error.Code != codeServerShutdown {
		t.Errorf("retained error = %q, want %s", result.Error.Code, codeServerShutdown)
	}
}

// Whatever wins, the receiver's copy and the retained copy describe one
// outcome. This is the invariant the split finish/deliver decision could break:
// a run could store success and deliver a shutdown failure, telling an
// orchestrator to redo work that had in fact been done.
func TestAsyncTerminal_CallbackAndRetainedResultAgreeUnderTheShutdownRace(t *testing.T) {
	chassis.RequireMajor(11)
	for i := 0; i < 3; i++ {
		t.Run(fmt.Sprintf("round-%d", i), func(t *testing.T) {
			receiver := newCallbackReceiver(t, http.StatusOK)
			tool := &gatedDirectAPITool{
				Tool:    opencode.New(),
				started: make(chan string, 1),
				release: make(chan struct{}),
			}
			h := asyncHandler(t, server.NewRunRegistry(1), func() runner.Tool { return tool })

			submitted := submitAsync(t, h, callBody(receiver.server.URL, ""), "wm-terminal-race")
			select {
			case <-tool.started:
			case <-time.After(15 * time.Second):
				t.Fatal("run never started")
			}

			// The run finishing and the server going away, at the same moment.
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			var wg sync.WaitGroup
			wg.Add(2)
			go func() { defer wg.Done(); close(tool.release) }()
			go func() { defer wg.Done(); h.Shutdown(ctx) }()
			wg.Wait()

			delivered := receiver.await(t, 1)
			rec := do(t, h, http.MethodGet, "/v1/runs/"+submitted.RunID+"/result")
			if rec.Code != http.StatusOK {
				t.Fatalf("result status = %d, body = %s", rec.Code, rec.Body.String())
			}
			var retained AsyncCompletion
			if err := json.NewDecoder(rec.Body).Decode(&retained); err != nil {
				t.Fatalf("decode retained result: %v", err)
			}

			if delivered.Status != retained.Status {
				t.Errorf("callback says %q, the retained result says %q",
					delivered.Status, retained.Status)
			}
			if deliveredCode(delivered) != deliveredCode(retained) {
				t.Errorf("callback error = %q, retained error = %q",
					deliveredCode(delivered), deliveredCode(retained))
			}
			// One outcome means one delivery.
			time.Sleep(50 * time.Millisecond)
			if got := receiver.count(); got != 1 {
				t.Errorf("callbacks delivered = %d, want exactly 1", got)
			}
		})
	}
}

// deliveredCode names a payload's error code, or "" for a success.
func deliveredCode(payload AsyncCompletion) ErrorCode {
	if payload.Error == nil {
		return ""
	}
	return payload.Error.Code
}

// A run that finished just before shutdown began has already told its receiver
// what happened. Shutdown must not follow it with a retryable failure for work
// that succeeded.
func TestAsyncShutdown_LeavesAnAlreadyFinishedRunAlone(t *testing.T) {
	chassis.RequireMajor(11)
	installFakeOpenCode(t, "finished before shutdown")
	receiver := newCallbackReceiver(t, http.StatusOK)
	h := asyncHandler(t, server.NewRunRegistry(1), openCodeFactory())

	submitted := submitAsync(t, h, callBody(receiver.server.URL, ""), "wm-finished-first")
	if payload := receiver.await(t, 1); payload.Status != runStatusSuccess {
		t.Fatalf("callback status = %q, want success", payload.Status)
	}
	pollUntil(t, h, submitted.RunID, runStatusSuccess)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	h.Shutdown(ctx)

	time.Sleep(50 * time.Millisecond)
	if got := receiver.count(); got != 1 {
		t.Errorf("callbacks = %d; shutdown notified a run that had already finished", got)
	}
	rec := do(t, h, http.MethodGet, "/v1/runs/"+submitted.RunID+"/result")
	var retained AsyncCompletion
	if err := json.NewDecoder(rec.Body).Decode(&retained); err != nil {
		t.Fatalf("decode retained result: %v", err)
	}
	if retained.Status != runStatusSuccess || retained.Error != nil {
		t.Errorf("retained result = %q / %+v, want the success it stored", retained.Status, retained.Error)
	}
}

// End to end: shutdown ends a run that is still working, and the worker's own
// completion afterwards changes nothing the caller can see.
func TestAsyncShutdown_WorkerCompletionAfterTheNoticeChangesNothing(t *testing.T) {
	chassis.RequireMajor(11)
	receiver := newCallbackReceiver(t, http.StatusOK)
	tool := &gatedDirectAPITool{
		Tool:    opencode.New(),
		started: make(chan string, 1),
		release: make(chan struct{}),
	}
	h := asyncHandler(t, server.NewRunRegistry(1), func() runner.Tool { return tool })

	submitted := submitAsync(t, h, callBody(receiver.server.URL, ""), "wm-shutdown-first")
	select {
	case <-tool.started:
	case <-time.After(15 * time.Second):
		t.Fatal("run never started")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		h.Shutdown(ctx)
	}()

	payload := receiver.await(t, 1)
	if payload.Error == nil || payload.Error.Code != codeServerShutdown {
		t.Fatalf("callback error = %+v, want %s", payload.Error, codeServerShutdown)
	}

	// Now let the worker run to its own end. It lost the transition, so it has
	// nothing to say.
	close(tool.release)
	<-shutdownDone
	time.Sleep(100 * time.Millisecond)

	if got := receiver.count(); got != 1 {
		t.Errorf("callbacks = %d, want 1", got)
	}
	rec := do(t, h, http.MethodGet, "/v1/runs/"+submitted.RunID+"/result")
	var retained AsyncCompletion
	if err := json.NewDecoder(rec.Body).Decode(&retained); err != nil {
		t.Fatalf("decode retained result: %v", err)
	}
	if retained.Status != runStatusFailure || retained.Error == nil || retained.Error.Code != codeServerShutdown {
		t.Errorf("retained result = %q / %+v, want the shutdown failure that was delivered",
			retained.Status, retained.Error)
	}
}

// The cause is recorded before the cancellation is signalled, so a worker never
// has to guess who ended its run — and the two caller protocols are reported
// distinctly while producing the same outcome.
func TestAsyncCancel_CausesAreExplicit(t *testing.T) {
	a := testAsyncRuns()

	viaHTTP := submitTestRun(t, a, "")
	if known, live := a.requestCancel(viaHTTP.id, causeCallerHTTP); !known || !live {
		t.Fatalf("requestCancel(queued) = %v/%v, want known and live", known, live)
	}
	if got := a.cancelCause(viaHTTP); got != causeCallerHTTP {
		t.Errorf("cause = %q, want %s", got, causeCallerHTTP)
	}
	if err := viaHTTP.ctx.Err(); err == nil {
		t.Error("the run's context was not cancelled")
	}

	viaGRPC := submitTestRun(t, a, "")
	if _, live := a.requestCancel(viaGRPC.id, causeCallerGRPC); !live {
		t.Error("a queued run was not live for gRPC cancellation")
	}
	if got := a.cancelCause(viaGRPC); got != causeCallerGRPC {
		t.Errorf("cause = %q, want %s", got, causeCallerGRPC)
	}

	// The first cause wins: a second cancellation does not relabel the run.
	a.requestCancel(viaGRPC.id, causeCallerHTTP)
	if got := a.cancelCause(viaGRPC); got != causeCallerGRPC {
		t.Errorf("cause = %q after a second cancellation, want the first %s", got, causeCallerGRPC)
	}

	// A terminal run is known but not live, so no API can claim it killed work.
	done := finishTestRun(t, a, "")
	known, live := a.requestCancel(done.id, causeCallerGRPC)
	if !known || live {
		t.Errorf("requestCancel(terminal) = %v/%v, want known but not live", known, live)
	}
	if got := a.cancelCause(done); got != causeNone {
		t.Errorf("a terminal run took cause %q", got)
	}

	// An unknown ID is neither.
	if known, live := a.requestCancel("deadbeef", causeCallerHTTP); known || live {
		t.Errorf("requestCancel(unknown) = %v/%v, want neither", known, live)
	}

	// Shutdown labels what is still live, and closes the store.
	pending := submitTestRun(t, a, "")
	live2 := a.beginShutdown()
	if len(live2) != 3 {
		t.Errorf("shutdown found %d live runs, want the 3 cancelled-but-unfinished ones", len(live2))
	}
	if got := a.cancelCause(pending); got != causeServerShutdown {
		t.Errorf("cause = %q, want %s", got, causeServerShutdown)
	}
	if _, err := a.submit("", nil, 0); !errors.Is(err, errAsyncClosing) {
		t.Errorf("submit after shutdown = %v, want errAsyncClosing", err)
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
