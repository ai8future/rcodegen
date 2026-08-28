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
	// errAsyncCapacity refuses a submission that would take live async work past
	// one of the store's admission bounds. It is the caller's signal to retry,
	// not to change the request.
	errAsyncCapacity = errors.New("the server is at its async capacity")
	// errAsyncClosing refuses a submission made once shutdown has begun. The
	// server keeps no durable run state, so there is nothing to accept it into.
	errAsyncClosing = errors.New("the server is shutting down and is not accepting async runs; " +
		"rserve holds no durable run state, so resubmit")

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
// transport's dialer with the plaintext address policy — and, with it, drops
// the transport's inherited proxy.
//
// Dropping the proxy is what makes the address policy mean anything. A cloned
// http.DefaultTransport carries Proxy: ProxyFromEnvironment, and when HTTP_PROXY
// names a proxy the transport asks the dialer to connect to the proxy rather
// than to the callback host. dialPlaintextCallback would then resolve and
// approve the proxy — while the absolute callback URL, the completion, and the
// caller's own headers went to it, for it to resolve and forward as it liked.
// That is the public-address and rebinding path the dialer exists to close, so
// plaintext delivery is direct or it does not happen: a failed direct attempt
// stays failed and pollable rather than falling back through the proxy.
//
// https keeps ordinary proxy behavior. Its guarantee is the certificate, which
// a proxy cannot forge, so a proxied https callback is still delivered to the
// host the caller named.
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
		transport.Proxy = nil
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
	// planBytes is the admission cost this run reserved at submit; see
	// asyncPlanBytes.
	planBytes int64
	// deliverOnce is defensive belt beside tryTerminalize's braces: terminal
	// ownership already decides who delivers, and this makes a second POST
	// impossible even if a future caller forgets that.
	deliverOnce sync.Once

	// Guarded by asyncRuns.mu.
	status       string
	createdAt    time.Time
	startedAt    time.Time
	finishedAt   time.Time
	queueWait    time.Duration
	cause        string // why the run was cancelled; see the cancel causes below
	admitted     bool   // still holding its admission reservation
	result       *AsyncCompletion
	lastAccessed time.Time
}

// Why a run was cancelled. The cause is set once, under the store's mutex,
// before the cancellation is signalled, so the worker that observes a cancelled
// context can say who ended the run rather than infer it from what else was
// true at the time.
const (
	causeNone           = ""
	causeCallerHTTP     = "caller_http"
	causeCallerGRPC     = "caller_grpc"
	causeServerShutdown = "server_shutdown"
)

// AsyncLimits bounds the async work one server will hold at once. Both bounds
// are needed: a count alone lets a handful of maximum-size requests hold far
// more memory than intended, and a byte budget alone lets an unbounded number
// of tiny requests hold an unbounded number of goroutines.
type AsyncLimits struct {
	// MaxLive is how many submitted async runs may be nonterminal at once.
	MaxLive int
	// MaxBytes is the total estimated retained request payload those runs may
	// hold; see asyncPlanBytes for what is counted.
	MaxBytes int64
}

// asyncMaxBytesDefault is the default retained-plan budget: enough for several
// requests at the 10MB body limit, small enough that a caller cannot walk the
// process into the OOM killer one accepted 202 at a time.
const asyncMaxBytesDefault = 64 << 20

// DefaultAsyncLimits derives the admission bounds for a server with
// maxConcurrent run slots. The live bound scales with the slot count so a
// server built to run more work can queue proportionally more of it, with a
// floor for the single-slot deployments the suite actually runs.
func DefaultAsyncLimits(maxConcurrent int) AsyncLimits {
	live := 4 * maxConcurrent
	if live < 8 {
		live = 8
	}
	return AsyncLimits{MaxLive: live, MaxBytes: asyncMaxBytesDefault}
}

// asyncRuns holds every async run this process knows about — in flight and
// retained — plus the lifecycle context they all descend from.
type asyncRuns struct {
	mu   sync.Mutex
	runs map[string]*asyncRun

	// Admission bounds and what is currently reserved against them. A run holds
	// its reservation from submit until its execution goroutine lets go of the
	// plan, which is a longer life than its terminal status: the memory is the
	// thing being bounded, not the lifecycle.
	limits    AsyncLimits
	liveCount int
	liveBytes int64
	// closing refuses submissions once shutdown has begun, so nothing joins the
	// set of runs being torn down.
	closing bool

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

func newAsyncRuns(limits AsyncLimits) *asyncRuns {
	ctx, cancel := context.WithCancel(context.Background())
	return &asyncRuns{
		runs:        make(map[string]*asyncRun),
		limits:      limits,
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

// submit reserves admission for a run and registers it as queued, or refuses.
//
// The reservation is taken before the run has an ID, which is the whole point:
// a refusal must reach the caller as a failed submission, not as a 202 for work
// the server never intended to hold. A refused submission leaves no run, no
// waiter, and no goroutine behind.
func (a *asyncRuns) submit(correlationID string, cb *callbackTarget, planBytes int64) (*asyncRun, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closing {
		return nil, errAsyncClosing
	}
	if a.liveCount >= a.limits.MaxLive {
		return nil, fmt.Errorf("%w: %d live async runs is the configured limit (RSERVE_ASYNC_MAX_LIVE); "+
			"poll or cancel one, or retry shortly", errAsyncCapacity, a.limits.MaxLive)
	}
	if a.liveBytes+planBytes > a.limits.MaxBytes {
		return nil, fmt.Errorf("%w: live async requests hold %d of %d retained bytes "+
			"(RSERVE_ASYNC_MAX_BYTES) and this one needs %d; retry shortly",
			errAsyncCapacity, a.liveBytes, a.limits.MaxBytes, planBytes)
	}

	ctx, cancel := context.WithCancel(a.ctx)
	now := a.now()
	run := &asyncRun{
		id:            server.NewRunID(),
		correlationID: correlationID,
		callback:      cb,
		ctx:           ctx,
		cancel:        cancel,
		planBytes:     planBytes,
		admitted:      true,
		status:        runStatusQueued,
		createdAt:     now,
		lastAccessed:  now,
	}
	a.liveCount++
	a.liveBytes += planBytes
	a.sweepLocked()
	a.runs[run.id] = run
	return run, nil
}

// releaseAdmission returns what a run reserved, once its execution goroutine no
// longer holds the plan. It is idempotent by the admitted flag, so the counters
// can never be double-credited into going negative.
func (a *asyncRuns) releaseAdmission(run *asyncRun) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !run.admitted {
		return
	}
	run.admitted = false
	a.liveCount--
	a.liveBytes -= run.planBytes
}

// asyncStats is what /health reports about admission: what is held now and what
// the ceiling is, so a caller seeing retryable 503s can tell a saturated server
// from a misconfigured one.
type asyncStats struct {
	live     int
	bytes    int64
	maxLive  int
	maxBytes int64
}

func (a *asyncRuns) stats() asyncStats {
	a.mu.Lock()
	defer a.mu.Unlock()
	return asyncStats{
		live:     a.liveCount,
		bytes:    a.liveBytes,
		maxLive:  a.limits.MaxLive,
		maxBytes: a.limits.MaxBytes,
	}
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

// tryTerminalize is the one place a run ends. It takes the run from queued or
// running to a terminal status, records the outcome, derives the retained copy
// from the same payload, and applies the retention bounds — all under one hold
// of the mutex — and reports whether this caller is the one that did it.
//
// Only the winner may deliver the callback, and only with the payload it won
// with. That is what keeps the three representations of a run in agreement: a
// worker finishing at the same moment shutdown sweeps the store used to be able
// to store success and deliver "server shut down", telling an orchestrator to
// redo work that had in fact been done. Whichever of the two arrives first now
// owns status, retained result, and callback together; the loser is told it
// lost and does nothing.
//
// The run just finished is the most recently used, so it is never the entry
// evicted to make room.
func (a *asyncRuns) tryTerminalize(run *asyncRun, status string, payload *AsyncCompletion) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if run.status != runStatusQueued && run.status != runStatusRunning {
		return false
	}
	run.status = status
	run.finishedAt = a.now()
	run.lastAccessed = run.finishedAt
	// Retention's own budget is applied here rather than by the caller, so the
	// retained copy is always derived from the payload that won.
	run.result = retainedCopy(payload)
	a.sweepLocked()
	a.evictLocked()
	return true
}

// cancelCause reports why a run was cancelled, or causeNone if nothing has
// asked for it to stop.
func (a *asyncRuns) cancelCause(run *asyncRun) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return run.cause
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

// requestCancel ends a queued or running run, recording who asked. known says
// whether the store has this ID at all; live says whether there was work to
// stop. A run that already finished is known but not live, so a caller
// cancelling twice sees the same answer both times, and no API can claim it
// killed work that had already ended.
//
// The cause is set under the mutex before the context is cancelled, so the
// worker that wakes to a cancelled context always finds the reason already
// recorded rather than racing to infer one.
func (a *asyncRuns) requestCancel(id, cause string) (known, live bool) {
	a.mu.Lock()
	a.sweepLocked()
	run, ok := a.runs[id]
	if !ok {
		a.mu.Unlock()
		return false, false
	}
	live = run.status == runStatusQueued || run.status == runStatusRunning
	if live && run.cause == causeNone {
		run.cause = cause
	}
	run.lastAccessed = a.now()
	a.mu.Unlock()

	if live {
		run.cancel()
	}
	return true, live
}

// beginShutdown closes the store to new submissions, marks every live run as
// ending with the server, and hands them back for terminalization. Causes are
// set here — before any context is cancelled — so a worker that observes the
// cancellation cannot mistake it for a caller's.
func (a *asyncRuns) beginShutdown() []*asyncRun {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.closing = true
	var out []*asyncRun
	for _, run := range a.runs {
		if run.status != runStatusQueued && run.status != runStatusRunning {
			continue
		}
		if run.cause == causeNone {
			run.cause = causeServerShutdown
		}
		out = append(out, run)
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
	run, err := h.async.submit(plan.correlationID, plan.callback, asyncPlanBytes(plan))
	if err != nil {
		writeAsyncRefusal(w, err)
		return
	}

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

// writeAsyncRefusal answers a submission the store would not admit. Unlike
// every other async answer it carries no run_id, because nothing was accepted:
// there is no run to poll, no run to cancel, and no callback coming. Retry-After
// gives an unattended caller — a Windmill step's retry policy — a number to wait
// rather than a guess.
func writeAsyncRefusal(w http.ResponseWriter, err error) {
	w.Header().Set("Retry-After", "1")
	writeJSON(w, http.StatusServiceUnavailable, NewErrorResponse(
		err.Error(), "server_error", asyncRefusalCode(err),
	))
}

// asyncRefusalCode maps a refused submission to its API error code. Both are
// retryable, and they differ in what the caller is waiting for: capacity to
// free up, or a server to come back.
func asyncRefusalCode(err error) ErrorCode {
	if errors.Is(err, errAsyncClosing) {
		return codeServerShutdown
	}
	return codeAsyncCapacity
}

// asyncPlanBytes estimates what one accepted submission keeps alive for the
// length of its run: the strings the plan holds, plus a fixed allowance for the
// run's own bookkeeping.
//
// It is deliberately an estimate. Exact Go heap accounting is not available
// here and would not be worth its cost if it were; what the byte bound has to
// do is stop a few maximum-size requests — a task near the 10MB body limit, a
// long callback URL, a header set — from being admitted as if they were free.
// The count bound handles the many-small-requests shape.
func asyncPlanBytes(plan *chatPlan) int64 {
	// The base covers the run entry, its context, the goroutine's stack, and the
	// bookkeeping around them: enough that a thousand empty runs still register
	// as memory rather than as nothing.
	total := int64(4 << 10)
	if plan == nil {
		return total
	}
	total += int64(len(plan.model) + len(plan.toolName) + len(plan.correlationID))
	for _, dir := range plan.workDirs {
		total += int64(len(dir))
	}
	if cfg := plan.cfg; cfg != nil {
		total += int64(len(cfg.Task) + len(cfg.Model) + len(cfg.Effort) + len(cfg.SessionID))
		for _, dir := range cfg.WorkDirs {
			total += int64(len(dir))
		}
	}
	if cb := plan.callback; cb != nil {
		total += int64(len(cb.url) + len(cb.target))
		for name, value := range cb.headers {
			total += int64(len(name) + len(value))
		}
	}
	return total
}

// executeAsync runs a submitted plan to its end, records the outcome, and
// delivers the callback. The callback POST happens after the run slot is
// released, so a slow receiver never holds capacity.
//
// The run is only this goroutine's to finish if it wins terminalization.
// Shutdown may have ended it first, in which case the receiver has already been
// told what happened and this outcome is discarded rather than sent as a second,
// contradictory answer. The teardown below — reaping the CLI child, removing the
// scratch clone — runs either way.
func (h *Handler) executeAsync(run *asyncRun, plan *chatPlan) {
	defer h.async.wg.Done()
	// The plan is unreachable once this returns, so the admission it reserved
	// goes back here and nowhere else.
	defer h.async.releaseAdmission(run)
	defer run.cancel()

	status, payload := h.runAsync(run, plan)
	// The callback gets the payload whole; retention gets whatever fits its
	// memory discipline, derived inside terminalization from this same payload.
	if h.async.tryTerminalize(run, status, payload) {
		h.deliverCallback(context.Background(), run, payload, h.async.backoff)
	}
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

	resp, result := h.completeNonStreaming(acq.Ctx, plan.tool, plan.cfg, completionMeta{
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
	if result.ExitCode != 0 || result.Error != nil {
		return runStatusFailure, asyncFailure(run, plan, executionErrorResponse(plan.cfg, result), artifacts)
	}
	return runStatusSuccess, asyncSuccess(run, resp)
}

// abortError names why a run ended early, from the cause recorded when the
// cancellation was requested. Both caller protocols produce the same
// run_cancelled outcome — an orchestrator should not have to care which API its
// operator reached for — and anything else is the server having ended the run,
// which is the retryable one.
func (h *Handler) abortError(run *asyncRun) ErrorResponse {
	switch h.async.cancelCause(run) {
	case causeCallerHTTP:
		return NewErrorResponse(
			"run cancelled by DELETE /v1/runs/"+run.id,
			"invalid_request_error", codeRunCancelled,
		)
	case causeCallerGRPC:
		return NewErrorResponse(
			"run cancelled by gRPC CancelRun for run "+run.id,
			"invalid_request_error", codeRunCancelled,
		)
	default:
		return NewErrorResponse(
			"server shut down before the run finished; rserve holds no durable run state, so resubmit",
			"server_error", codeServerShutdown,
		)
	}
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
	// Close the store, mark the live runs as ending with the server, then cancel
	// them. Nothing can be submitted into the set being torn down, and every
	// worker that wakes to a cancelled context finds the cause already recorded.
	live := h.async.beginShutdown()
	h.async.cancel() // every run's context ends here

	var wg sync.WaitGroup
	for _, run := range live {
		payload := asyncShutdownPayload(run)
		// A run that finished between the snapshot and here has already stored
		// its own outcome and delivered it. Shutdown does not get to overwrite
		// that with a failure the caller would redo the work for.
		if !h.async.tryTerminalize(run, runStatusFailure, payload) {
			continue
		}
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
		if known, _ := h.async.requestCancel(id, causeCallerHTTP); !known {
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

// CancelAsyncRun cancels an async run on behalf of another protocol — gRPC
// CancelRun, which shares this process's run IDs but not its run store. owned
// says whether this store knows the ID at all, so a caller can fall back to the
// registry for an ordinary synchronous run; cancelled says whether there was
// live work to stop, so an already-terminal run is never reported as killed.
//
// It exists on the handler rather than the store because the gRPC server must
// not import this package: it takes this as an interface, wired in main.
func (h *Handler) CancelAsyncRun(id string) (owned, cancelled bool) {
	return h.async.requestCancel(id, causeCallerGRPC)
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
