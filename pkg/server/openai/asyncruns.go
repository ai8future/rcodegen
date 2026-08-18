// asyncruns.go implements callback mode: a chat completion that outlives the
// connection that submitted it.
//
//	POST /v1/chat/completions with "callback_url" — 202 {run_id, status, correlation_id}
//	GET  /v1/runs/{run_id}                        — lifecycle status and timings
//	GET  /v1/runs/{run_id}/result                 — the retained completion JSON
//	GET  /v1/runs?correlation_id=                 — the runs a caller's job owns
//	DELETE /v1/runs/{run_id}                      — cancel a queued or running run
//
// A synchronous completion couples three timeouts and dies with its connection,
// which is what makes a Windmill step holding one for half an hour fragile. In
// callback mode the request returns as soon as the run is accepted, the run
// takes its lifecycle from the server rather than from the request, and the
// completion is POSTed to the caller's URL when it ends — a Windmill resume URL,
// typically, so the flow suspends instead of waiting.
//
// Retention is deliberately small and deliberately non-durable: results live in
// this process's memory, bounded by count and age, and a restart loses them. The
// fleet keeps durable state in Postgres; rserve keeps none.
package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"rcodegen/pkg/server"

	"github.com/ai8future/chassis-go/v11/logz"
)

const (
	// asyncResultCap and asyncResultTTL bound retention. Only finished runs are
	// evictable — a queued or running one is never dropped out from under its
	// caller.
	asyncResultCap = 100
	asyncResultTTL = time.Hour

	// asyncOutputCap caps the message content of a retained or delivered
	// completion, the same 64KB discipline bundle step output follows. Over the
	// cap the content is cut and marked, never silently dropped.
	asyncOutputCap = stepOutputCap

	// callbackAttemptTimeout bounds one delivery attempt.
	callbackAttemptTimeout = 10 * time.Second
)

// callbackBackoff is the wait before each retry, so a callback receiver that is
// restarting has three chances across roughly ten seconds. Its length plus one
// is the attempt count.
var callbackBackoff = []time.Duration{2 * time.Second, 8 * time.Second}

// ---------------------------------------------------------------------------
// Callback targets
// ---------------------------------------------------------------------------

var (
	// errCallbackURL marks a callback URL the server refuses to POST to.
	errCallbackURL = errors.New("invalid callback_url")
	// errCallbackHeaders marks callback headers that cannot be sent as given.
	errCallbackHeaders = errors.New("invalid callback_headers")
)

// callbackTarget is a validated callback destination. headers are the caller's,
// applied verbatim to the POST and never logged: a receiver's bearer token
// arrives this way, and so does the secret inside a Windmill resume URL, which
// is why only target — scheme and host — is ever written to a log line.
type callbackTarget struct {
	url     string
	headers map[string]string
	target  string
}

// newCallbackTarget validates a callback URL and its headers. https is accepted
// anywhere; plain http only for a loopback or RFC1918 host, where the network
// itself is the boundary.
func newCallbackTarget(raw string, headers map[string]string) (*callbackTarget, error) {
	u, err := url.Parse(raw)
	if err != nil {
		// A parse error names the URL it failed on, and the URL may carry a
		// resume secret, so the reason is described rather than quoted.
		return nil, fmt.Errorf("%w: not a valid URL", errCallbackURL)
	}
	host := u.Hostname()
	if host == "" {
		return nil, fmt.Errorf("%w: no host", errCallbackURL)
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
	case "http":
		if !isPrivateOrLoopbackHost(host) {
			return nil, fmt.Errorf("%w: plain http is accepted only for a loopback or RFC1918 host, "+
				"not %s — use https", errCallbackURL, host)
		}
	default:
		return nil, fmt.Errorf("%w: scheme %q is not supported; use https, or http for a "+
			"loopback or RFC1918 host", errCallbackURL, u.Scheme)
	}
	if err := checkCallbackHeaders(headers); err != nil {
		return nil, err
	}

	cb := &callbackTarget{url: u.String(), target: u.Scheme + "://" + u.Host}
	if len(headers) > 0 {
		cb.headers = make(map[string]string, len(headers))
		for name, value := range headers {
			cb.headers[name] = value
		}
	}
	return cb, nil
}

// isPrivateOrLoopbackHost reports whether http without TLS is defensible for
// this host: a loopback address, an RFC1918 (or IPv6 unique-local) address, or
// the localhost name.
func isPrivateOrLoopbackHost(host string) bool {
	h := strings.ToLower(host)
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	ip := net.ParseIP(h)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate()
}

// checkCallbackHeaders rejects header names and values that cannot go on the
// wire. A value is never quoted back: it is the caller's credential.
func checkCallbackHeaders(headers map[string]string) error {
	for name, value := range headers {
		if !isHeaderName(name) {
			return fmt.Errorf("%w: %q is not a valid header name", errCallbackHeaders, name)
		}
		if strings.ContainsAny(value, "\r\n\x00") {
			return fmt.Errorf("%w: the value of %s contains a line break or NUL", errCallbackHeaders, name)
		}
	}
	return nil
}

// isHeaderName reports whether s is a valid HTTP field name (RFC 7230 token).
func isHeaderName(s string) bool {
	if s == "" {
		return false
	}
	const others = "!#$%&'*+-.^_`|~"
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune(others, r):
		default:
			return false
		}
	}
	return true
}

// callbackErrorCode maps a callback validation failure to its API error code.
func callbackErrorCode(err error) ErrorCode {
	if errors.Is(err, errCallbackHeaders) {
		return codeInvalidCallbackHeaders
	}
	return codeInvalidCallbackURL
}

// ---------------------------------------------------------------------------
// The run store
// ---------------------------------------------------------------------------

// asyncRun is one submitted run: its identity and lifecycle context, fixed at
// submission, and its mutable progress, guarded by the store's mutex.
type asyncRun struct {
	// Immutable after submit — safe to read without the lock.
	id            string
	correlationID string
	callback      *callbackTarget
	ctx           context.Context
	cancel        context.CancelFunc
	// deliverOnce makes the callback exactly one POST: whichever finishes the
	// run — the run itself or a shutdown notice — is the one the receiver sees.
	deliverOnce sync.Once

	// Guarded by asyncRuns.mu.
	status       string
	createdAt    time.Time
	startedAt    time.Time
	finishedAt   time.Time
	queueWait    time.Duration
	byCaller     bool // cancelled through DELETE rather than by shutdown
	result       *AsyncCompletion
	lastAccessed time.Time
}

// asyncRuns holds every async run this process knows about — in flight and
// retained — plus the lifecycle context they all descend from.
type asyncRuns struct {
	mu   sync.Mutex
	runs map[string]*asyncRun

	// Retention bounds. Tests shrink them.
	cap int
	ttl time.Duration
	now func() time.Time

	// ctx is the parent of every run's context: an async run is detached from
	// the request that submitted it and ends only on cancel or shutdown.
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	client  *http.Client
	backoff []time.Duration
}

func newAsyncRuns() *asyncRuns {
	ctx, cancel := context.WithCancel(context.Background())
	return &asyncRuns{
		runs:    make(map[string]*asyncRun),
		cap:     asyncResultCap,
		ttl:     asyncResultTTL,
		now:     time.Now,
		ctx:     ctx,
		cancel:  cancel,
		client:  &http.Client{Timeout: callbackAttemptTimeout},
		backoff: callbackBackoff,
	}
}

// asyncLogger builds the logger for one run's own output. It is built where it
// is used, not held on the handler, because a handler can be constructed before
// the chassis runtime is initialized.
func asyncLogger() *slog.Logger {
	return logz.New("info")
}

// submit registers a queued run and hands back the entry the caller answers
// 202 with.
func (a *asyncRuns) submit(correlationID string, cb *callbackTarget) *asyncRun {
	ctx, cancel := context.WithCancel(a.ctx)
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	run := &asyncRun{
		id:            server.NewRunID(),
		correlationID: correlationID,
		callback:      cb,
		ctx:           ctx,
		cancel:        cancel,
		status:        runStatusQueued,
		createdAt:     now,
		lastAccessed:  now,
	}
	a.sweepLocked()
	a.runs[run.id] = run
	return run
}

// markRunning records that the run took a slot, and how long it waited for it.
func (a *asyncRuns) markRunning(run *asyncRun, queueWait time.Duration) {
	a.mu.Lock()
	defer a.mu.Unlock()
	run.status = runStatusRunning
	run.startedAt = a.now()
	run.queueWait = queueWait
	run.lastAccessed = run.startedAt
}

// finish records a terminal outcome and its payload, then applies the retention
// bounds. The run just finished is the most recently used, so it is never the
// entry evicted to make room.
func (a *asyncRuns) finish(run *asyncRun, status string, payload *AsyncCompletion) {
	a.mu.Lock()
	defer a.mu.Unlock()
	run.status = status
	run.finishedAt = a.now()
	run.lastAccessed = run.finishedAt
	run.result = payload
	a.sweepLocked()
	a.evictLocked()
}

// cancelledByCaller reports whether DELETE, rather than shutdown, ended the run.
func (a *asyncRuns) cancelledByCaller(run *asyncRun) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return run.byCaller
}

// summary returns the run's status view, and whether it is still known.
func (a *asyncRuns) summary(id string) (RunSummary, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sweepLocked()
	run, ok := a.runs[id]
	if !ok {
		return RunSummary{}, false
	}
	run.lastAccessed = a.now()
	return summaryOf(run), true
}

// result returns the retained completion for a finished run. The second value
// distinguishes "not finished yet" from "gone", which are different answers to
// the caller.
func (a *asyncRuns) result(id string) (*AsyncCompletion, string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sweepLocked()
	run, ok := a.runs[id]
	if !ok {
		return nil, "", false
	}
	run.lastAccessed = a.now()
	return run.result, run.status, true
}

// list returns run summaries, newest first, filtered by correlation ID when one
// is given.
func (a *asyncRuns) list(correlationID string) []RunSummary {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sweepLocked()
	out := make([]RunSummary, 0, len(a.runs))
	for _, run := range a.runs {
		if correlationID != "" && run.correlationID != correlationID {
			continue
		}
		out = append(out, summaryOf(run))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt != out[j].CreatedAt {
			return out[i].CreatedAt > out[j].CreatedAt
		}
		return out[i].RunID < out[j].RunID
	})
	return out
}

// requestCancel ends a queued or running run. A run that already finished is
// reported as known and left alone, so a caller cancelling twice sees the same
// answer both times.
func (a *asyncRuns) requestCancel(id string) bool {
	a.mu.Lock()
	a.sweepLocked()
	run, ok := a.runs[id]
	if !ok {
		a.mu.Unlock()
		return false
	}
	live := run.status == runStatusQueued || run.status == runStatusRunning
	if live {
		run.byCaller = true
	}
	run.lastAccessed = a.now()
	a.mu.Unlock()

	if live {
		run.cancel()
	}
	return true
}

// liveRuns snapshots the runs that have not reached a terminal state.
func (a *asyncRuns) liveRuns() []*asyncRun {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []*asyncRun
	for _, run := range a.runs {
		if run.status == runStatusQueued || run.status == runStatusRunning {
			out = append(out, run)
		}
	}
	return out
}

// sweepLocked drops finished runs older than the TTL.
func (a *asyncRuns) sweepLocked() {
	if a.ttl <= 0 {
		return
	}
	cutoff := a.now().Add(-a.ttl)
	for id, run := range a.runs {
		if run.finishedAt.IsZero() {
			continue // still queued or running: not a result, not evictable
		}
		if run.finishedAt.Before(cutoff) {
			delete(a.runs, id)
		}
	}
}

// evictLocked drops the least recently used finished runs until retention fits
// within the cap.
func (a *asyncRuns) evictLocked() {
	for {
		var oldest *asyncRun
		retained := 0
		for _, run := range a.runs {
			if run.finishedAt.IsZero() {
				continue
			}
			retained++
			if oldest == nil || run.lastAccessed.Before(oldest.lastAccessed) {
				oldest = run
			}
		}
		if retained <= a.cap || oldest == nil {
			return
		}
		delete(a.runs, oldest.id)
	}
}

// summaryOf builds the status view of a run. Callers hold the lock.
func summaryOf(run *asyncRun) RunSummary {
	s := RunSummary{
		RunID:         run.id,
		Status:        run.status,
		CorrelationID: run.correlationID,
		CreatedAt:     run.createdAt.Unix(),
		QueueWaitMs:   run.queueWait.Milliseconds(),
	}
	if !run.startedAt.IsZero() {
		s.StartedAt = run.startedAt.Unix()
	}
	if !run.finishedAt.IsZero() {
		s.FinishedAt = run.finishedAt.Unix()
	}
	return s
}

// ---------------------------------------------------------------------------
// Submission and execution
// ---------------------------------------------------------------------------

// submitAsyncRun answers 202 and detaches the run from this request. Everything
// the run needs was validated before we got here, so the only thing that can
// still go wrong is the run itself — and that is reported to the callback, not
// on this connection.
func (h *Handler) submitAsyncRun(w http.ResponseWriter, plan *chatPlan) {
	run := h.async.submit(plan.correlationID, plan.callback)

	// Answer first, then start work: the caller learns the run_id it will need
	// for polling and cancellation before any callback can reference it.
	writeJSON(w, http.StatusAccepted, AsyncSubmitResponse{
		RunID:         run.id,
		Status:        runStatusQueued,
		CorrelationID: plan.correlationID,
	})

	h.async.wg.Add(1)
	go h.executeAsync(run, plan)
}

// executeAsync runs a submitted plan to its end, records the outcome, and
// delivers the callback. The callback POST happens after the run slot is
// released, so a slow receiver never holds capacity.
func (h *Handler) executeAsync(run *asyncRun, plan *chatPlan) {
	defer h.async.wg.Done()
	defer run.cancel()

	status, payload := h.runAsync(run, plan)
	h.async.finish(run, status, payload)
	h.deliverCallback(context.Background(), run, payload, h.async.backoff)
}

// runAsync holds a run slot for exactly as long as the run needs it and returns
// the terminal outcome.
func (h *Handler) runAsync(run *asyncRun, plan *chatPlan) (string, *AsyncCompletion) {
	acq, err := h.registry.AcquireWith(run.ctx, plan.toolName, plan.cfg.Task, server.AcquireOptions{
		CorrelationID: plan.correlationID,
		RunID:         run.id,
	})
	if err != nil {
		// The lifecycle context is cancelled only by DELETE or shutdown, so a
		// failed acquire here is one of those, not a busy server.
		return runStatusFailure, asyncFailure(run, plan, h.abortError(run))
	}
	defer acq.Cancel()
	defer h.registry.Release(acq.RunID)
	h.async.markRunning(run, acq.QueueWait)

	var clone *workDirClone
	if plan.cloneRequested {
		cloneLogger := asyncLogger()
		clone, err = cloneWorkDirs(acq.Ctx, run.id, plan.workDirs, cloneLogger)
		if err != nil {
			if run.ctx.Err() != nil {
				return runStatusFailure, asyncFailure(run, plan, h.abortError(run))
			}
			return runStatusFailure, asyncFailure(run, plan, NewErrorResponse(
				err.Error(), "server_error", codeCloneFailed,
			))
		}
		defer clone.cleanup(cloneLogger)
		plan.cfg.WorkDirs = clone.dirs
	}

	resp := h.completeNonStreaming(acq.Ctx, plan.tool, plan.cfg, completionMeta{
		runID:          run.id,
		model:          plan.model,
		toolName:       plan.toolName,
		correlationID:  plan.correlationID,
		clonedWorkDirs: clone.count(),
	})

	// A cancelled run returns whatever the CLI managed to emit before it was
	// killed. That is a fragment, not an answer, so it is reported as the
	// failure it is.
	if run.ctx.Err() != nil {
		return runStatusFailure, asyncFailure(run, plan, h.abortError(run))
	}
	return runStatusSuccess, asyncSuccess(run, resp)
}

// abortError names why a run ended early: the caller asked, or the server did.
func (h *Handler) abortError(run *asyncRun) ErrorResponse {
	if h.async.cancelledByCaller(run) {
		return NewErrorResponse(
			"run cancelled by DELETE /v1/runs/"+run.id,
			"invalid_request_error", codeRunCancelled,
		)
	}
	return NewErrorResponse(
		"server shut down before the run finished; rserve holds no durable run state, so resubmit",
		"server_error", codeServerShutdown,
	)
}

// asyncSuccess wraps a completion as the run's terminal payload, capping the
// message content at the retention limit.
func asyncSuccess(run *asyncRun, resp ChatCompletionResponse) *AsyncCompletion {
	truncated := capCompletionOutput(&resp)
	return &AsyncCompletion{
		ChatCompletionResponse: resp,
		RunID:                  run.id,
		Status:                 runStatusSuccess,
		OutputTruncated:        truncated,
	}
}

// asyncFailure builds the terminal payload for a run that produced no
// completion. It carries the same error envelope — retryable included — a
// synchronous caller would have received.
func asyncFailure(run *asyncRun, plan *chatPlan, errResp ErrorResponse) *AsyncCompletion {
	detail := errResp.Error
	return &AsyncCompletion{
		ChatCompletionResponse: ChatCompletionResponse{
			ID:            "chatcmpl-" + run.id,
			Object:        "chat.completion",
			Created:       nowUnix(),
			Model:         plan.model,
			Choices:       []Choice{},
			CorrelationID: run.correlationID,
		},
		RunID:  run.id,
		Status: runStatusFailure,
		Error:  &detail,
	}
}

// capCompletionOutput trims message content to the retention cap, reporting
// whether anything was cut.
func capCompletionOutput(resp *ChatCompletionResponse) bool {
	truncated := false
	for i, choice := range resp.Choices {
		if choice.Message == nil || len(choice.Message.Content) <= asyncOutputCap {
			continue
		}
		trimmed := *choice.Message
		trimmed.Content = trimPartialRune(choice.Message.Content[:asyncOutputCap])
		resp.Choices[i].Message = &trimmed
		truncated = true
	}
	return truncated
}

// ---------------------------------------------------------------------------
// Callback delivery
// ---------------------------------------------------------------------------

// deliverCallback POSTs a run's terminal payload, at most once per run. A
// receiver that never accepts it costs the run nothing: the result stays
// retained for polling either way.
func (h *Handler) deliverCallback(ctx context.Context, run *asyncRun, payload *AsyncCompletion, backoff []time.Duration) {
	if run.callback == nil || payload == nil {
		return
	}
	run.deliverOnce.Do(func() {
		h.async.post(ctx, run, payload, backoff)
	})
}

// post attempts delivery len(backoff)+1 times, waiting the given interval
// between attempts, then gives up with a warning.
func (a *asyncRuns) post(ctx context.Context, run *asyncRun, payload *AsyncCompletion, backoff []time.Duration) {
	cb := run.callback
	logger := asyncLogger()
	body, err := json.Marshal(payload)
	if err != nil {
		logger.Warn("async callback payload could not be encoded",
			"run_id", run.id, "target", cb.target, "error", err)
		return
	}

	attempts := len(backoff) + 1
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		status, err := a.postOnce(ctx, cb, body)
		if err == nil {
			logger.Info("async callback delivered",
				"run_id", run.id, "target", cb.target, "status", status, "attempt", attempt)
			return
		}
		lastErr = err
		if attempt == attempts {
			break
		}
		select {
		case <-time.After(backoff[attempt-1]):
		case <-ctx.Done():
			logger.Warn("async callback abandoned; the delivery budget expired",
				"run_id", run.id, "target", cb.target, "attempts", attempt, "error", err)
			return
		}
	}
	logger.Warn("async callback undelivered; the result stays available for polling",
		"run_id", run.id, "target", cb.target, "attempts", attempts, "error", lastErr)
}

// postOnce makes one delivery attempt. Transport errors are unwrapped before
// they are returned: the wrapper names the URL it failed on, and that URL can
// be a bearer secret in path form.
func (a *asyncRuns) postOnce(ctx context.Context, cb *callbackTarget, body []byte) (int, error) {
	reqCtx, cancel := context.WithTimeout(ctx, callbackAttemptTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, cb.url, bytes.NewReader(body))
	if err != nil {
		return 0, errors.New("callback request could not be built")
	}
	req.Header.Set("Content-Type", "application/json")
	for name, value := range cb.headers {
		req.Header.Set(name, value)
	}

	resp, err := a.client.Do(req)
	if err != nil {
		var uerr *url.Error
		if errors.As(err, &uerr) {
			err = uerr.Err
		}
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return resp.StatusCode, fmt.Errorf("callback receiver returned HTTP %d", resp.StatusCode)
	}
	return resp.StatusCode, nil
}

// Shutdown ends every in-flight async run and makes one best-effort attempt to
// tell each waiting callback that its run died with the server. It is bounded
// by ctx: a receiver that does not answer promptly delays the exit by no more
// than the caller's budget.
//
// Call it after the HTTP listener has stopped accepting, so no submission
// arrives while the runs it would join are being torn down.
//
// Nothing here is durable. A caller whose run was in flight gets a failure
// callback if its receiver is up, and nothing at all if it is not — which is
// why a Windmill flow's suspend timeout, not rserve, is the guard.
func (h *Handler) Shutdown(ctx context.Context) {
	live := h.async.liveRuns()
	h.async.cancel() // every run's context ends here

	var wg sync.WaitGroup
	for _, run := range live {
		payload := asyncShutdownPayload(run)
		wg.Add(1)
		go func(run *asyncRun, payload *AsyncCompletion) {
			defer wg.Done()
			// One attempt, no backoff: shutdown is not the time to retry.
			h.deliverCallback(ctx, run, payload, nil)
		}(run, payload)
	}
	waitBounded(&wg, ctx)

	// Give the run goroutines their chance to unwind — reap the CLI child,
	// remove the scratch clone — within the same budget.
	waitBounded(&h.async.wg, ctx)
}

// asyncShutdownPayload is the failure a run reports when the server, not the
// run, is what ended.
func asyncShutdownPayload(run *asyncRun) *AsyncCompletion {
	detail := NewErrorResponse(
		"server shut down before the run finished; rserve holds no durable run state, so resubmit",
		"server_error", codeServerShutdown,
	).Error
	return &AsyncCompletion{
		ChatCompletionResponse: ChatCompletionResponse{
			ID:            "chatcmpl-" + run.id,
			Object:        "chat.completion",
			Created:       nowUnix(),
			Choices:       []Choice{},
			CorrelationID: run.correlationID,
		},
		RunID:  run.id,
		Status: runStatusFailure,
		Error:  &detail,
	}
}

// waitBounded waits for wg, or for ctx to run out, whichever comes first.
func waitBounded(wg *sync.WaitGroup, ctx context.Context) {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// ---------------------------------------------------------------------------
// Run endpoints
// ---------------------------------------------------------------------------

// handleRuns serves GET /v1/runs, optionally filtered by ?correlation_id=.
func (h *Handler) handleRuns(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, NewErrorResponse(
			"method not allowed", "invalid_request_error", codeMethodNotAllowed,
		))
		return
	}
	corrID := sanitizeCorrelationID(r.URL.Query().Get("correlation_id"))
	writeJSON(w, http.StatusOK, RunList{Object: "list", Data: h.async.list(corrID)})
}

// handleRunByID routes /v1/runs/{run_id} and /v1/runs/{run_id}/result.
func (h *Handler) handleRunByID(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v1/runs/")
	id, sub, _ := strings.Cut(path, "/")
	if id == "" || (sub != "" && sub != "result") {
		writeJSON(w, http.StatusNotFound, NewErrorResponse(
			"unknown run path", "invalid_request_error", codeNotFound,
		))
		return
	}

	if sub == "result" {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, NewErrorResponse(
				"method not allowed", "invalid_request_error", codeMethodNotAllowed,
			))
			return
		}
		h.writeRunResult(w, id)
		return
	}

	switch r.Method {
	case http.MethodGet:
		summary, ok := h.async.summary(id)
		if !ok {
			writeJSON(w, http.StatusNotFound, NewErrorResponse(
				runGoneMessage(id), "invalid_request_error", codeNotFound,
			))
			return
		}
		writeJSON(w, http.StatusOK, summary)
	case http.MethodDelete:
		if !h.async.requestCancel(id) {
			writeJSON(w, http.StatusNotFound, NewErrorResponse(
				runGoneMessage(id), "invalid_request_error", codeNotFound,
			))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, NewErrorResponse(
			"method not allowed", "invalid_request_error", codeMethodNotAllowed,
		))
	}
}

// writeRunResult serves GET /v1/runs/{run_id}/result.
func (h *Handler) writeRunResult(w http.ResponseWriter, id string) {
	payload, status, ok := h.async.result(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, NewErrorResponse(
			runGoneMessage(id), "invalid_request_error", codeNotFound,
		))
		return
	}
	if payload == nil {
		writeJSON(w, http.StatusNotFound, NewErrorResponse(
			fmt.Sprintf("run %s is %s and has no result yet; poll GET /v1/runs/%s for status", id, status, id),
			"invalid_request_error", codeNotFound,
		))
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

// runGoneMessage says the same thing for an ID that never existed and one whose
// result has been evicted, because from here they are the same condition.
func runGoneMessage(id string) string {
	return fmt.Sprintf("run %s is unknown: it was never submitted, or its result has been evicted "+
		"(retention is in-memory, %d results or %s, and does not survive a restart)",
		id, asyncResultCap, asyncResultTTL)
}
