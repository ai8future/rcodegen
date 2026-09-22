// asynccancel_test.go covers the cross-protocol cancellation contract: an async
// run submitted over HTTP and cancelled over gRPC.
//
// It is an external test package because it needs both halves of the server —
// the gRPC service here and the async store in pkg/server/openai, which imports
// this package. That is the same asymmetry the AsyncCanceller interface exists
// for: the async store can depend on the registry, so the gRPC server takes the
// store as an interface and main wires the two together.
package server_test

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
	"rcodegen/pkg/server/openai"
	"rcodegen/pkg/server/pb"
	"rcodegen/pkg/tools/opencode"

	chassis "github.com/ai8future/chassis-go/v11"
)

// installSleepingOpenCode installs a fake opencode CLI that writes a marker and
// then blocks until it is killed, so a test can cancel a run that is provably
// under way. exec, so the process rserve kills is the sleep itself.
func installSleepingOpenCode(t *testing.T, marker string) {
	t.Helper()
	binDir := t.TempDir()
	script := "#!/bin/sh\nprintf 'x' > '" + marker + "'\nexec sleep 60\n"
	if err := os.WriteFile(filepath.Join(binDir, "opencode"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake opencode: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// asyncReceiver records the callbacks an async run delivers.
type asyncReceiver struct {
	server *httptest.Server

	mu       sync.Mutex
	payloads []map[string]any
	hits     chan struct{}
}

func newAsyncReceiver(t *testing.T) *asyncReceiver {
	t.Helper()
	rec := &asyncReceiver{hits: make(chan struct{}, 8)}
	rec.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		payload := map[string]any{}
		_ = json.Unmarshal(body, &payload)
		rec.mu.Lock()
		rec.payloads = append(rec.payloads, payload)
		rec.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		select {
		case rec.hits <- struct{}{}:
		default:
		}
	}))
	t.Cleanup(rec.server.Close)
	return rec
}

// await blocks until one callback has arrived and returns its status and error
// code — the two fields that say what the run's terminal outcome was.
func (a *asyncReceiver) await(t *testing.T) (status, code string) {
	t.Helper()
	deadline := time.After(30 * time.Second)
	for {
		a.mu.Lock()
		got := len(a.payloads)
		var first map[string]any
		if got > 0 {
			first = a.payloads[0]
		}
		a.mu.Unlock()
		if got > 0 {
			status, _ = first["status"].(string)
			if detail, ok := first["error"].(map[string]any); ok {
				code, _ = detail["code"].(string)
			}
			return status, code
		}
		select {
		case <-a.hits:
		case <-deadline:
			t.Fatal("no callback was delivered")
		}
	}
}

func (a *asyncReceiver) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.payloads)
}

// asyncPair builds an HTTP handler and a gRPC server over one registry. wired
// says whether the gRPC server is told that the async store owns its own IDs —
// the difference this release is about.
func asyncPair(t *testing.T, slots int, wired bool) (*openai.Handler, *server.Server, *server.RunRegistry) {
	t.Helper()
	reg := server.NewRunRegistry(slots)
	factories := map[string]server.ToolFactory{
		"opencode": func() runner.Tool { return opencode.New() },
	}
	h := openai.NewHandler(nil, factories, reg, []string{"opencode"}, nil, nil)
	srv := server.NewServer(nil, factories, reg, nil)
	if wired {
		srv.SetAsyncCanceller(h)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		h.Shutdown(ctx)
	})
	return h, srv, reg
}

// submitAsync posts a callback-mode chat completion and returns its run ID.
func submitAsync(t *testing.T, h *openai.Handler, callbackURL, corrID string) string {
	t.Helper()
	body := `{"model":"opencode","messages":[{"role":"user","content":"hello"}],` +
		`"callback_url":"` + callbackURL + `"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("X-Correlation-ID", corrID)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("submit status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		RunID string `json:"run_id"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode 202: %v", err)
	}
	if resp.RunID == "" {
		t.Fatal("202 carries no run_id")
	}
	return resp.RunID
}

// runStatus reads one run's lifecycle status.
func runStatus(t *testing.T, h *openai.Handler, id string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/runs/"+id, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status poll = %d, body = %s", rec.Code, rec.Body.String())
	}
	var summary struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&summary); err != nil {
		t.Fatalf("decode summary: %v", err)
	}
	return summary.Status
}

func awaitStatus(t *testing.T, h *openai.Handler, id string, want ...string) string {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		got := runStatus(t, h, id)
		for _, w := range want {
			if got == w {
				return got
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %s stuck at %q, want one of %v", id, got, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func awaitMarker(t *testing.T, marker string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the fake CLI never reached its marker")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A gRPC server that does not know the async store owns its IDs cancels the
// registry entry instead. That kills the CLI the run had reached but leaves the
// run's own context alive, so the worker publishes a tool-execution failure —
// after gRPC has already told its caller the run was cancelled.
//
// It is a live code path, not a historical one: a server built without
// SetAsyncCanceller behaves exactly this way, which is why main wires it.
func TestCancelRun_UnwiredGRPCAcknowledgesButTheAsyncRunReportsToolFailure(t *testing.T) {
	chassis.RequireMajor(11)
	marker := filepath.Join(t.TempDir(), "started")
	installSleepingOpenCode(t, marker)
	receiver := newAsyncReceiver(t)
	h, srv, _ := asyncPair(t, 1, false)

	runID := submitAsync(t, h, receiver.server.URL, "wm-grpc-unwired")
	awaitMarker(t, marker)
	awaitStatus(t, h, runID, "running")

	resp, err := srv.CancelRun(context.Background(), &pb.CancelRunRequest{RunId: runID})
	if err != nil {
		t.Fatalf("CancelRun: %v", err)
	}
	if !resp.Cancelled {
		t.Fatalf("CancelRun reported %+v; the registry entry should have been found", resp)
	}

	status, code := receiver.await(t)
	if status != "failure" || code != "tool_execution_failed" {
		t.Fatalf("callback = %q/%q, want failure/tool_execution_failed", status, code)
	}
}

// Wired, the async store answers for its own IDs: the cancellation ends the run
// rather than only the process it had reached, and every representation of the
// run agrees it was cancelled.
func TestCancelRun_GRPCCancelsARunningAsyncRun(t *testing.T) {
	chassis.RequireMajor(11)
	marker := filepath.Join(t.TempDir(), "started")
	installSleepingOpenCode(t, marker)
	receiver := newAsyncReceiver(t)
	h, srv, reg := asyncPair(t, 1, true)

	runID := submitAsync(t, h, receiver.server.URL, "wm-grpc-running")
	awaitMarker(t, marker)
	awaitStatus(t, h, runID, "running")

	resp, err := srv.CancelRun(context.Background(), &pb.CancelRunRequest{RunId: runID})
	if err != nil {
		t.Fatalf("CancelRun: %v", err)
	}
	if !resp.Cancelled {
		t.Fatalf("CancelRun reported %+v, want the run cancelled", resp)
	}

	status, code := receiver.await(t)
	if status != "failure" || code != "run_cancelled" {
		t.Errorf("callback = %q/%q, want failure/run_cancelled", status, code)
	}
	if got := awaitStatus(t, h, runID, "failure"); got != "failure" {
		t.Errorf("retained status = %q, want failure", got)
	}

	// The slot goes back and the run is not left holding the registry.
	deadline := time.Now().Add(15 * time.Second)
	for reg.ActiveCount() != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := reg.ActiveCount(); got != 0 {
		t.Errorf("active runs = %d after cancellation, want 0", got)
	}
}

// A queued async run has no registry entry at all — it was published before it
// held a slot — so before this release gRPC reported it as not found. The store
// owns it from submission, so cancelling it works and the CLI never starts.
func TestCancelRun_GRPCCancelsAQueuedAsyncRun(t *testing.T) {
	chassis.RequireMajor(11)
	marker := filepath.Join(t.TempDir(), "started")
	installSleepingOpenCode(t, marker)
	receiver := newAsyncReceiver(t)
	h, srv, reg := asyncPair(t, 1, true)

	// Hold the only slot so the submitted run cannot start.
	heldID, _, heldCancel, err := reg.Acquire(context.Background(), "opencode", "held")
	if err != nil {
		t.Fatalf("acquire holding slot: %v", err)
	}
	defer func() {
		heldCancel()
		reg.Release(heldID)
	}()

	runID := submitAsync(t, h, receiver.server.URL, "wm-grpc-queued")
	awaitStatus(t, h, runID, "queued")

	resp, err := srv.CancelRun(context.Background(), &pb.CancelRunRequest{RunId: runID})
	if err != nil {
		t.Fatalf("CancelRun: %v", err)
	}
	if !resp.Cancelled {
		t.Fatalf("CancelRun on a queued async run reported %+v, want it cancelled", resp)
	}

	status, code := receiver.await(t)
	if status != "failure" || code != "run_cancelled" {
		t.Errorf("callback = %q/%q, want failure/run_cancelled", status, code)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("the CLI started for a run that was cancelled while queued")
	}
}

// A terminal run is idempotent over HTTP, and gRPC must not claim it killed
// work that had already ended.
func TestCancelRun_GRPCReportsATerminalAsyncRunAsAlreadyFinished(t *testing.T) {
	chassis.RequireMajor(11)
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "opencode"),
		[]byte("#!/bin/sh\nprintf 'done'\n"), 0o755); err != nil {
		t.Fatalf("write fake opencode: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	receiver := newAsyncReceiver(t)
	h, srv, _ := asyncPair(t, 1, true)

	runID := submitAsync(t, h, receiver.server.URL, "wm-grpc-terminal")
	if status, _ := receiver.await(t); status != "success" {
		t.Fatalf("callback status = %q, want success", status)
	}
	awaitStatus(t, h, runID, "success")

	resp, err := srv.CancelRun(context.Background(), &pb.CancelRunRequest{RunId: runID})
	if err != nil {
		t.Fatalf("CancelRun: %v", err)
	}
	if resp.Cancelled {
		t.Error("gRPC claimed it cancelled a run that had already finished")
	}
	if !strings.Contains(resp.Message, "already finished") {
		t.Errorf("message = %q, want it to say the run was already terminal", resp.Message)
	}

	// And no second callback went out.
	time.Sleep(50 * time.Millisecond)
	if got := receiver.count(); got != 1 {
		t.Errorf("callbacks = %d, want the one the run delivered", got)
	}
}

// HTTP DELETE and gRPC racing the same run: exactly one cause wins, one
// terminal transition happens, and one callback is sent.
func TestCancelRun_HTTPAndGRPCRaceToOneOutcome(t *testing.T) {
	chassis.RequireMajor(11)
	marker := filepath.Join(t.TempDir(), "started")
	installSleepingOpenCode(t, marker)
	receiver := newAsyncReceiver(t)
	h, srv, _ := asyncPair(t, 1, true)

	runID := submitAsync(t, h, receiver.server.URL, "wm-grpc-race")
	awaitMarker(t, marker)
	awaitStatus(t, h, runID, "running")

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/v1/runs/"+runID, nil))
		if rec.Code != http.StatusNoContent && rec.Code != http.StatusNotFound {
			t.Errorf("DELETE = %d, want 204 or 404", rec.Code)
		}
	}()
	go func() {
		defer wg.Done()
		if _, err := srv.CancelRun(context.Background(), &pb.CancelRunRequest{RunId: runID}); err != nil {
			t.Errorf("CancelRun: %v", err)
		}
	}()
	wg.Wait()

	status, code := receiver.await(t)
	if status != "failure" || code != "run_cancelled" {
		t.Errorf("callback = %q/%q, want failure/run_cancelled whichever API won", status, code)
	}
	time.Sleep(100 * time.Millisecond)
	if got := receiver.count(); got != 1 {
		t.Errorf("callbacks = %d, want exactly 1 for one run", got)
	}
}

// Ordinary synchronous gRPC runs are still the registry's, and routing async
// IDs through the store did not change what happens to them.
func TestCancelRun_RegistryRunsAreUnchanged(t *testing.T) {
	chassis.RequireMajor(11)
	_, srv, reg := asyncPair(t, 1, true)

	runID, runCtx, cancel, err := reg.Acquire(context.Background(), "opencode", "sync work")
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer func() {
		cancel()
		reg.Release(runID)
	}()

	resp, err := srv.CancelRun(context.Background(), &pb.CancelRunRequest{RunId: runID})
	if err != nil {
		t.Fatalf("CancelRun: %v", err)
	}
	if !resp.Cancelled {
		t.Fatalf("CancelRun on a registry run reported %+v, want it cancelled", resp)
	}
	select {
	case <-runCtx.Done():
	case <-time.After(5 * time.Second):
		t.Error("the registry run's context was not cancelled")
	}

	unknown, err := srv.CancelRun(context.Background(), &pb.CancelRunRequest{RunId: "deadbeefdeadbeef"})
	if err != nil {
		t.Fatalf("CancelRun: %v", err)
	}
	if unknown.Cancelled {
		t.Error("an unknown run reported as cancelled")
	}
	if !strings.Contains(unknown.Message, "not found") {
		t.Errorf("message = %q, want not found", unknown.Message)
	}
}

// A run cancelled through gRPC says so in its error message, so an operator
// reading a callback can tell which API ended the run.
func TestCancelRun_GRPCCancellationNamesItself(t *testing.T) {
	chassis.RequireMajor(11)
	marker := filepath.Join(t.TempDir(), "started")
	installSleepingOpenCode(t, marker)
	receiver := newAsyncReceiver(t)
	h, srv, _ := asyncPair(t, 1, true)

	runID := submitAsync(t, h, receiver.server.URL, "wm-grpc-named")
	awaitMarker(t, marker)
	awaitStatus(t, h, runID, "running")
	if _, err := srv.CancelRun(context.Background(), &pb.CancelRunRequest{RunId: runID}); err != nil {
		t.Fatalf("CancelRun: %v", err)
	}
	receiver.await(t)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/runs/"+runID+"/result", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("result status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var retained struct {
		Error *struct {
			Message   string `json:"message"`
			Retryable bool   `json:"retryable"`
		} `json:"error"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&retained); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if retained.Error == nil {
		t.Fatal("a cancelled run retained no error")
	}
	if !strings.Contains(retained.Error.Message, "gRPC CancelRun") {
		t.Errorf("message = %q, want it to name gRPC CancelRun", retained.Error.Message)
	}
	if retained.Error.Retryable {
		t.Error("a cancelled run reported as retryable")
	}
	fmt.Fprint(io.Discard, runID)
}
