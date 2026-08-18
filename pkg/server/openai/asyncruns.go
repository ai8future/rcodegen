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

	// asyncRetainedArtifactCap is how many bytes of artifact content a retained
	// result may hold, holding retention to the same discipline. Past it the
	// retained copy keeps names instead of contents; see retainedCopy.
	asyncRetainedArtifactCap = asyncOutputCap

	// callbackAttemptTimeout bounds one delivery attempt.
	callbackAttemptTimeout = 10 * time.Second

	// callbackResolveTimeout bounds the submit-time lookup of a plaintext
	// callback hostname. That lookup is feedback rather than enforcement — the
	// delivery dialer is what actually holds the line — so it fails fast rather
	// than making the caller wait out a slow resolver.
	callbackResolveTimeout = 2 * time.Second
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
	// errCallbackRedirect ends a delivery whose receiver answered with a
	// redirect. Following one would let a host that passed the plaintext policy
	// hand the payload to a host that never did, and no resume URL redirects.
	errCallbackRedirect = errors.New("callback receiver answered with a redirect, which is not followed")
	// errCallbackPublicAddress ends a plaintext delivery whose host resolved,
	// at the moment of connection, to an address outside the private network.
	errCallbackPublicAddress = errors.New("callback host resolves outside the private network; " +
		"refusing to send plain http to it")
)

// callbackLookupIP resolves a callback host to the addresses a delivery could
// reach. It is a variable so tests can put a name in front of a local receiver
// without touching the machine's resolver, and can make one host answer
// differently at validation time than at delivery time.
var callbackLookupIP = func(ctx context.Context, host string) ([]net.IP, error) {
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	ips := make([]net.IP, 0, len(addrs))
	for _, addr := range addrs {
		ips = append(ips, addr.IP)
	}
	return ips, nil
}

// callbackDialIP opens the connection to an address the plaintext policy has
// already vetted. Tests replace it to observe exactly which address the dialer
// chose — the assertion that a refused delivery never reached the network.
var callbackDialIP = func(ctx context.Context, network, addr string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, network, addr)
}

// callbackTarget is a validated callback destination. headers are the caller's,
// applied verbatim to the POST and never logged: a receiver's bearer token
// arrives this way, and so does the secret inside a Windmill resume URL, which
// is why only target — scheme and host — is ever written to a log line.
type callbackTarget struct {
	url     string
	headers map[string]string
	target  string
	// plaintext selects the delivery client: an http target is dialed under the
	// private-address policy, an https one over ordinary TLS.
	plaintext bool
}

// newCallbackTarget validates a callback URL and its headers. https is accepted
// anywhere; plain http only where the network itself is the boundary — a
// loopback or RFC1918 address, or a hostname that resolves to one.
//
// The hostname check here is the caller's feedback, not the control. DNS can
// answer differently by the time the run finishes, so the delivery dialer
// checks again against the address it is about to connect to; see
// dialPlaintextCallback.
func newCallbackTarget(ctx context.Context, raw string, headers map[string]string) (*callbackTarget, error) {
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
	scheme := strings.ToLower(u.Scheme)
	switch scheme {
	case "https":
	case "http":
		if err := checkPlaintextHost(ctx, host); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("%w: scheme %q is not supported; use https, or http for a loopback or "+
			"RFC1918 host, or a hostname that resolves to one", errCallbackURL, u.Scheme)
	}
	if err := checkCallbackHeaders(headers); err != nil {
		return nil, err
	}

	cb := &callbackTarget{
		url:       u.String(),
		target:    u.Scheme + "://" + u.Host,
		plaintext: scheme == "http",
	}
	if len(headers) > 0 {
		cb.headers = make(map[string]string, len(headers))
		for name, value := range headers {
			cb.headers[name] = value
		}
	}
	return cb, nil
}

// checkPlaintextHost decides, at submit time, whether plain http may be sent to
// this host. An IP literal answers for itself and localhost is taken on faith;
// any other name is resolved under a short budget, and every address it answers
// with has to be one the private network already protects. A name that resolves
// to nothing, or fails to resolve at all, is refused for the same reason a
// public one is: the server cannot show that the payload would stay inside.
//
// This exists so a mistyped or public callback URL comes back as a 400 on the
// submitting connection rather than as a warning in a log an hour later. What
// makes it safe is dialPlaintextCallback, not this.
func checkPlaintextHost(ctx context.Context, host string) error {
	if isLocalhostName(host) {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil {
		if isPrivateOrLoopbackIP(ip) {
			return nil
		}
		return plaintextHostError(host, "is not a loopback or RFC1918 address")
	}

	lookupCtx, cancel := context.WithTimeout(ctx, callbackResolveTimeout)
	defer cancel()
	ips, err := callbackLookupIP(lookupCtx, host)
	if err != nil {
		return plaintextHostError(host, "did not resolve")
	}
	if len(ips) == 0 {
		return plaintextHostError(host, "resolved to no addresses")
	}
	for _, ip := range ips {
		if !isPrivateOrLoopbackIP(ip) {
			return plaintextHostError(host, "resolves to an address outside the private network")
		}
	}
	return nil
}

// plaintextHostError phrases every plaintext rejection the same way, so the
// caller always learns which hosts would have been accepted.
func plaintextHostError(host, why string) error {
	return fmt.Errorf("%w: plain http is accepted only for a loopback or RFC1918 host, or a hostname "+
		"that resolves to one — %s %s; use https", errCallbackURL, host, why)
}

// isLocalhostName reports whether a host is the reserved localhost name or one
// of its subdomains, which RFC 6761 says never leaves the machine.
func isLocalhostName(host string) bool {
	h := strings.ToLower(host)
	return h == "localhost" || strings.HasSuffix(h, ".localhost")
}

// isPrivateOrLoopbackIP reports whether http without TLS is defensible for an
// address: loopback, or RFC1918 / IPv6 unique-local.
//
// Link-local is deliberately excluded even though it is unroutable off the
// segment — 169.254.169.254 is the cloud instance-metadata endpoint, which is
// exactly the address an attacker-chosen callback URL would want to reach. So
// are the unspecified and multicast ranges, which are not a receiver at all.
func isPrivateOrLoopbackIP(ip net.IP) bool {
	if ip == nil || ip.IsUnspecified() || ip.IsMulticast() ||
		ip.IsLinkLocalUnicast() || ip.IsInterfaceLocalMulticast() {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate()
}

// dialPlaintextCallback is the dialer of the plaintext delivery client, and the
// place the http policy is actually enforced.
//
// It resolves the callback host itself, at the moment of connection, refuses
// unless every address the host answers with is one plain http may reach, and
// then connects to a vetted address directly rather than handing the name back
// to the transport to resolve a second time. The name still rides the request,
// so the receiver sees the Host header it expects.
//
// Resolving here rather than trusting the submit-time check is the whole point:
// a host that answered 10.0.4.224 when the run was submitted and answers a
// public address half an hour later, when the callback is delivered, never gets
// a connection. That window — validate now, connect later — is exactly what DNS
// rebinding exploits, and an async run holds it open for the length of the run.
func dialPlaintextCallback(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, errors.New("callback address is not host:port")
	}
	// The resolver's own error is dropped rather than wrapped: it quotes the
	// name it failed on, and a resume URL's host is the least secret part of it
	// but still not worth putting in a log line that already names the target.
	ips, err := callbackLookupIP(ctx, host)
	if err != nil {
		return nil, errors.New("callback host did not resolve")
	}
	if len(ips) == 0 {
		return nil, errors.New("callback host resolved to no addresses")
	}
	// Every answer has to pass, not just the one that would be dialed: a host
	// answering with a private and a public address is the rebinding shape
	// itself, and which one a dial picks is not the server's decision to make.
	for _, ip := range ips {
		if !isPrivateOrLoopbackIP(ip) {
			return nil, errCallbackPublicAddress
		}
	}
	var lastErr error
	for _, ip := range ips {
		conn, err := callbackDialIP(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// newCallbackClient builds a delivery client. dial, when non-nil, replaces the
// transport's dialer with the plaintext address policy.
//
// Neither client follows redirects. A receiver that answers a callback with a
// 302 is either broken or hostile, and following it would hand the payload —
// completion, artifacts, and the caller's own headers — to a host that passed
// none of the checks the original target did. A Windmill resume URL never
// redirects, so nothing legitimate is lost.
func newCallbackClient(dial func(context.Context, string, string) (net.Conn, error)) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if dial != nil {
		transport.DialContext = dial
	}
	return &http.Client{
		Timeout:   callbackAttemptTimeout,
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errCallbackRedirect
		},
	}
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

	// client delivers to https targets; plainClient delivers to http ones under
	// the private-address dialer. Every delivery goes through one of them —
	// a finished run's callback and a shutdown notice alike — so the policy has
	// no path around it.
	client      *http.Client
	plainClient *http.Client
	backoff     []time.Duration
}

func newAsyncRuns() *asyncRuns {
	ctx, cancel := context.WithCancel(context.Background())
	return &asyncRuns{
		runs:        make(map[string]*asyncRun),
		cap:         asyncResultCap,
		ttl:         asyncResultTTL,
		now:         time.Now,
		ctx:         ctx,
		cancel:      cancel,
		client:      newCallbackClient(nil),
		plainClient: newCallbackClient(dialPlaintextCallback),
		backoff:     callbackBackoff,
	}
}

// clientFor picks the delivery client for a target: an http target gets the one
// whose dialer re-checks the address at connection time, https the ordinary one
// with default certificate verification.
func (a *asyncRuns) clientFor(cb *callbackTarget) *http.Client {
	if cb.plaintext {
		return a.plainClient
	}
	return a.client
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
	// The callback gets the payload whole; retention gets whatever fits its
	// memory discipline. They are the same object whenever nothing was evicted.
	h.async.finish(run, status, retainedCopy(payload))
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
		return runStatusFailure, asyncFailure(run, plan, h.abortError(run), nil)
	}
	defer acq.Cancel()
	defer h.registry.Release(acq.RunID)
	h.async.markRunning(run, acq.QueueWait)

	var clone *workDirClone
	var artifacts *artifactCollector
	if plan.cloneRequested {
		cloneLogger := asyncLogger()
		clone, err = cloneWorkDirs(acq.Ctx, run.id, plan.workDirs, cloneLogger)
		if err != nil {
			// No clone was made, so there is nothing to collect: this is the
			// one failure that reports no artifacts rather than partial ones.
			if run.ctx.Err() != nil {
				return runStatusFailure, asyncFailure(run, plan, h.abortError(run), nil)
			}
			return runStatusFailure, asyncFailure(run, plan, NewErrorResponse(
				err.Error(), "server_error", codeCloneFailed,
			), nil)
		}
		defer clone.cleanup(cloneLogger)
		plan.cfg.WorkDirs = clone.dirs
		if plan.returnArtifacts {
			artifacts = newArtifactCollector(clone, cloneLogger)
			defer artifacts.close()
		}
	}

	resp := h.completeNonStreaming(acq.Ctx, plan.tool, plan.cfg, completionMeta{
		runID:          run.id,
		model:          plan.model,
		toolName:       plan.toolName,
		correlationID:  plan.correlationID,
		clonedWorkDirs: clone.count(),
		artifacts:      artifacts,
	})

	// A cancelled run returns whatever the CLI managed to emit before it was
	// killed. That is a fragment, not an answer, so it is reported as the
	// failure it is — with the files it wrote before the kill, which are the
	// most useful thing a cancelled run leaves behind. Collection already ran
	// above and is memoized, so this costs no second walk.
	if run.ctx.Err() != nil {
		return runStatusFailure, asyncFailure(run, plan, h.abortError(run), artifacts)
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
// synchronous caller would have received, plus whatever the run wrote before it
// died: a failed run's partial output is often the only diagnostic there is.
func asyncFailure(run *asyncRun, plan *chatPlan, errResp ErrorResponse, artifacts *artifactCollector) *AsyncCompletion {
	detail := errResp.Error
	payload := &AsyncCompletion{
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
	payload.Artifacts, payload.ArtifactsSkipped = artifacts.collect()
	return payload
}

// retainedCopy is the version of a completion this process keeps in memory.
//
// Retention follows the same 64KB discipline as step output, and artifacts can
// be two megabytes; a callback has no such limit, because it is delivered once
// and then forgotten. So the two diverge on purpose: over the budget, the
// retained copy keeps the artifacts' names in artifacts_skipped and drops their
// contents, while the POST the caller receives carries them in full. A caller
// that needs the bytes takes them off its callback — polling is the fallback for
// the result, not a second copy of the payload.
func retainedCopy(payload *AsyncCompletion) *AsyncCompletion {
	if payload == nil || len(payload.Artifacts) == 0 {
		return payload
	}
	total := 0
	for _, a := range payload.Artifacts {
		total += len(a.Content)
	}
	if total <= asyncRetainedArtifactCap {
		return payload
	}

	retained := *payload
	skipped := make([]ArtifactSkipped, 0, len(payload.ArtifactsSkipped)+len(payload.Artifacts))
	skipped = append(skipped, payload.ArtifactsSkipped...)
	for _, a := range payload.Artifacts {
		skipped = append(skipped, ArtifactSkipped{Path: a.Path, Reason: artifactSkipEvicted})
	}
	retained.Artifacts = nil
	retained.ArtifactsSkipped = skipped
	return &retained
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

	resp, err := a.clientFor(cb).Do(req)
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
