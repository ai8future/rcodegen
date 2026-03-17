# rserve Multi-Turn Session Support Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add multi-turn conversation support to rserve by capturing session IDs from tool CLI output and passing them back on subsequent requests via `--resume`.

**Architecture:** The underlying CLIs (claude, codex, gemini) already manage their own conversation state and support `--resume <session_id>`. rserve already has `Config.SessionID` and all three tools' `BuildCommand` already use it. We add a server-side `SessionStore` that maps client-facing session IDs to tool session IDs with TTL-based expiry. The `StreamParser` is extended to capture session IDs from the init event. Both the gRPC proto and OpenAI HTTP API are updated to accept and return session IDs.

**Tech Stack:** Go, protobuf, gRPC, HTTP/JSON

---

## File Structure

| File | Action | Responsibility |
|------|--------|---------------|
| `pkg/server/session.go` | Create | `SessionStore` — thread-safe in-memory store mapping session IDs → tool session IDs with TTL |
| `pkg/server/session_test.go` | Create | Tests for `SessionStore` |
| `pkg/runner/stream.go` | Modify | Add `SessionID` field to `StreamParser`, capture from init events |
| `pkg/runner/stream_test.go` | Modify | Test session ID capture |
| `pkg/runner/runner.go` | Modify | Add `SessionID` to `RunResult`, propagate from parser |
| `proto/rserve.proto` | Modify | Add `session_id` to `RunTaskRequest` |
| `pkg/server/pb/rserve.pb.go` | Regenerate | Generated from proto |
| `pkg/server/pb/rserve_grpc.pb.go` | Regenerate | Generated from proto |
| `pkg/server/server.go` | Modify | Wire session store into `RunTask` — lookup on request, store after run, return in `InitEvent` |
| `pkg/server/openai/types.go` | Modify | Add `SessionID` to request/response types |
| `pkg/server/openai/handler.go` | Modify | Wire session store into HTTP handler — accept session_id, pass to config, return in response |
| `cmd/rserve/main.go` | Modify | Create `SessionStore` and pass to server + handler |

---

### Task 1: SessionStore

**Files:**
- Create: `pkg/server/session.go`
- Create: `pkg/server/session_test.go`

- [ ] **Step 1: Write the failing test for SessionStore**

```go
// pkg/server/session_test.go
package server

import (
	"testing"
	"time"
)

func TestSessionStore_StoreAndGet(t *testing.T) {
	store := NewSessionStore(10 * time.Minute)
	defer store.Stop()

	store.Store("sess-1", "claude", "tool-sess-abc")

	entry, ok := store.Get("sess-1")
	if !ok {
		t.Fatal("expected to find session")
	}
	if entry.ToolSessionID != "tool-sess-abc" {
		t.Errorf("expected tool session 'tool-sess-abc', got %q", entry.ToolSessionID)
	}
	if entry.Tool != "claude" {
		t.Errorf("expected tool 'claude', got %q", entry.Tool)
	}
}

func TestSessionStore_GetMissing(t *testing.T) {
	store := NewSessionStore(10 * time.Minute)
	defer store.Stop()

	_, ok := store.Get("nonexistent")
	if ok {
		t.Error("expected not found for nonexistent session")
	}
}

func TestSessionStore_Expiry(t *testing.T) {
	store := NewSessionStore(50 * time.Millisecond)
	defer store.Stop()
	store.Store("sess-1", "claude", "tool-sess-abc")

	// Should exist immediately
	if _, ok := store.Get("sess-1"); !ok {
		t.Fatal("expected session to exist before expiry")
	}

	time.Sleep(100 * time.Millisecond)

	// Should be gone after TTL
	if _, ok := store.Get("sess-1"); ok {
		t.Error("expected session to be expired")
	}
}

func TestSessionStore_Update(t *testing.T) {
	store := NewSessionStore(10 * time.Minute)
	defer store.Stop()

	store.Store("sess-1", "claude", "tool-sess-v1")
	store.Store("sess-1", "claude", "tool-sess-v2")

	entry, ok := store.Get("sess-1")
	if !ok {
		t.Fatal("expected to find session")
	}
	if entry.ToolSessionID != "tool-sess-v2" {
		t.Errorf("expected updated tool session, got %q", entry.ToolSessionID)
	}
}

func TestSessionStore_Delete(t *testing.T) {
	store := NewSessionStore(10 * time.Minute)
	defer store.Stop()
	store.Store("sess-1", "claude", "tool-sess-abc")

	store.Delete("sess-1")

	if _, ok := store.Get("sess-1"); ok {
		t.Error("expected session to be deleted")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/cliff/Desktop/_code/codegen_suite/rcodegen && go test ./pkg/server/ -run TestSessionStore -v`
Expected: FAIL — `NewSessionStore` not defined

- [ ] **Step 3: Implement SessionStore**

```go
// pkg/server/session.go
package server

import (
	"sync"
	"time"
)

// SessionEntry holds a stored session mapping.
type SessionEntry struct {
	ToolSessionID string
	Tool          string
	CreatedAt     time.Time
	LastUsed      time.Time
}

// SessionStore provides thread-safe in-memory session storage with TTL.
type SessionStore struct {
	mu       sync.RWMutex
	sessions map[string]*SessionEntry
	ttl      time.Duration
	done     chan struct{}
}

// NewSessionStore creates a store that expires sessions after ttl.
// It starts a background goroutine that sweeps expired entries every ttl/2.
// Call Stop() to release the goroutine (important in tests).
func NewSessionStore(ttl time.Duration) *SessionStore {
	s := &SessionStore{
		sessions: make(map[string]*SessionEntry),
		ttl:      ttl,
		done:     make(chan struct{}),
	}
	go s.sweepLoop(ttl / 2)
	return s
}

// Stop shuts down the background sweep goroutine.
func (s *SessionStore) Stop() {
	select {
	case <-s.done:
		// already stopped
	default:
		close(s.done)
	}
}

// Store saves or updates a session mapping.
func (s *SessionStore) Store(sessionID, tool, toolSessionID string) {
	now := time.Now()
	s.mu.Lock()
	s.sessions[sessionID] = &SessionEntry{
		ToolSessionID: toolSessionID,
		Tool:          tool,
		CreatedAt:     now,
		LastUsed:      now,
	}
	s.mu.Unlock()
}

// Get retrieves a session entry, updating its last-used time.
// Returns false if not found or expired.
func (s *SessionStore) Get(sessionID string) (*SessionEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.sessions[sessionID]
	if !ok {
		return nil, false
	}
	if time.Since(entry.LastUsed) > s.ttl {
		delete(s.sessions, sessionID)
		return nil, false
	}
	entry.LastUsed = time.Now()
	return entry, true
}

// Delete removes a session.
func (s *SessionStore) Delete(sessionID string) {
	s.mu.Lock()
	delete(s.sessions, sessionID)
	s.mu.Unlock()
}

// sweepLoop periodically removes expired sessions until Stop() is called.
func (s *SessionStore) sweepLoop(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-s.done:
			return
		case <-ticker.C:
			s.mu.Lock()
			now := time.Now()
			for id, entry := range s.sessions {
				if now.Sub(entry.LastUsed) > s.ttl {
					delete(s.sessions, id)
				}
			}
			s.mu.Unlock()
		}
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /Users/cliff/Desktop/_code/codegen_suite/rcodegen && go test ./pkg/server/ -run TestSessionStore -v`
Expected: All PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/server/session.go pkg/server/session_test.go
git commit -m "feat(rserve): add SessionStore for multi-turn session management"
```

---

### Task 2: StreamParser captures session ID

**Files:**
- Modify: `pkg/runner/stream.go` — add `SessionID` field to `StreamParser`, capture in `handleSystem`
- Modify: `pkg/runner/stream_test.go` — test session ID capture
- Modify: `pkg/runner/runner.go` — add `SessionID` to `RunResult`, propagate from parser

- [ ] **Step 1: Write the failing test**

Add to `pkg/runner/stream_test.go`:

```go
func TestStreamParser_CapturesSessionID(t *testing.T) {
	var buf bytes.Buffer
	p := NewStreamParser(&buf)

	p.ProcessLine(`{"type":"system","subtype":"init","session_id":"sess-abc-123"}`)

	if p.SessionID != "sess-abc-123" {
		t.Errorf("expected SessionID='sess-abc-123', got %q", p.SessionID)
	}
}

func TestStreamParser_CapturesSessionID_ViaCallback(t *testing.T) {
	var buf bytes.Buffer
	var capturedSessionID string
	cb := func(event *StreamEvent) {
		if event.SessionID != "" {
			capturedSessionID = event.SessionID
		}
	}
	p := NewStreamParserWithCallback(&buf, nil, cb)

	p.ProcessLine(`{"type":"system","subtype":"init","session_id":"sess-xyz-789"}`)

	if p.SessionID != "sess-xyz-789" {
		t.Errorf("expected parser SessionID='sess-xyz-789', got %q", p.SessionID)
	}
	if capturedSessionID != "sess-xyz-789" {
		t.Errorf("expected callback to receive session_id='sess-xyz-789', got %q", capturedSessionID)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/cliff/Desktop/_code/codegen_suite/rcodegen && go test ./pkg/runner/ -run TestStreamParser_CapturesSessionID -v`
Expected: FAIL — `SessionID` field not found

- [ ] **Step 3: Add SessionID to StreamEvent and StreamParser**

In `pkg/runner/stream.go`, add `SessionID` to `StreamEvent`:

```go
// StreamEvent — add field after existing fields:
SessionID    string          `json:"session_id,omitempty"`
```

Add `SessionID` to `StreamParser` struct (after `TotalCostUSD`):

```go
SessionID    string          // Captured from init event
```

In `handleSystem`, capture session_id from init events. Replace the existing `handleSystem` method:

```go
func (p *StreamParser) handleSystem(event StreamEvent) {
	switch event.Subtype {
	case "init":
		if event.SessionID != "" {
			p.SessionID = event.SessionID
		}
		if !p.initialized {
			fmt.Fprintf(p.writer, "%s%s⚡ Claude initialized%s\n", Dim, Cyan, Reset)
			p.initialized = true
		}
	case "hook_response":
		// Skip hook responses
	default:
		// Skip other system events
	}
}
```

- [ ] **Step 4: Add SessionID to RunResult and propagate**

In `pkg/runner/runner.go`, add to `RunResult` struct:

```go
SessionID    string
```

In `executeWithStreamParserCtx` (line ~626, after `TotalCostUSD` capture), add:

```go
if parser.SessionID != "" {
	cfg.SessionID = parser.SessionID
}
```

In `RunWithContext` (line ~550, in the return), add:

```go
SessionID:    cfg.SessionID,
```

So the return block becomes:
```go
return &RunResult{
	ExitCode:     exitCode,
	TokenUsage:   cfg.TokenUsage,
	TotalCostUSD: cfg.TotalCostUSD,
	SessionID:    cfg.SessionID,
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd /Users/cliff/Desktop/_code/codegen_suite/rcodegen && go test ./pkg/runner/ -v`
Expected: All PASS (including new and existing tests)

- [ ] **Step 6: Commit**

```bash
git add pkg/runner/stream.go pkg/runner/stream_test.go pkg/runner/runner.go
git commit -m "feat(runner): capture session_id from stream init events"
```

---

### Task 3: Update proto and regenerate

**Files:**
- Modify: `proto/rserve.proto` — add `session_id` to `RunTaskRequest` and `ResultEvent`
- Regenerate: `pkg/server/pb/rserve.pb.go`
- Regenerate: `pkg/server/pb/rserve_grpc.pb.go`

- [ ] **Step 1: Add session_id to proto**

In `proto/rserve.proto`, add `session_id` field to `RunTaskRequest`:

```protobuf
message RunTaskRequest {
  string tool = 1;
  string task = 2;
  string model = 3;
  string max_budget = 4;
  repeated string work_dirs = 5;
  map<string, string> variables = 6;
  string session_id = 7;           // Resume existing session (multi-turn)
}
```

Add `session_id` to `ResultEvent`:

```protobuf
message ResultEvent {
  int32 exit_code = 1;
  string output = 2;
  TokenUsage usage = 3;
  double total_cost_usd = 4;
  string grade = 5;
  string session_id = 6;          // Session ID for multi-turn continuation
}
```

**Note:** `InitEvent` already has a `session_id` field (field 1). We intentionally return the session ID in `ResultEvent` instead because the session ID is only known after the CLI subprocess emits its init event and the run completes — by then, the gRPC `InitEvent` has already been sent. The `InitEvent.session_id` remains available for future use (e.g., mid-stream session ID reporting) but is not populated in this implementation.

- [ ] **Step 2: Regenerate Go code from proto**

Run: `cd /Users/cliff/Desktop/_code/codegen_suite/rcodegen && make proto`

This uses the Makefile target which runs protoc and moves generated files to `pkg/server/pb/`. If `make proto` fails, check `which protoc` and `ls $(go env GOPATH)/bin/protoc-gen-go*`.

- [ ] **Step 3: Verify it compiles**

Run: `cd /Users/cliff/Desktop/_code/codegen_suite/rcodegen && go build ./pkg/server/...`
Expected: No errors

- [ ] **Step 4: Commit**

```bash
git add proto/rserve.proto pkg/server/pb/rserve.pb.go pkg/server/pb/rserve_grpc.pb.go
git commit -m "feat(proto): add session_id to RunTaskRequest and ResultEvent"
```

---

### Task 4: Wire sessions into gRPC server

**Files:**
- Modify: `pkg/server/server.go` — accept session store, lookup/store sessions, return session_id
- Modify: `cmd/rserve/main.go` — create SessionStore and pass to Server

- [ ] **Step 1: Update Server struct to accept SessionStore**

In `pkg/server/server.go`, add `sessions` field to `Server`:

```go
type Server struct {
	pb.UnimplementedRServeServer
	settings      *settings.Settings
	toolFactories map[string]ToolFactory
	registry      *RunRegistry
	sessions      *SessionStore
}
```

Update `NewServer` signature and implementation:

```go
func NewServer(s *settings.Settings, toolFactories map[string]ToolFactory, registry *RunRegistry, sessions *SessionStore) *Server {
	return &Server{
		settings:      s,
		toolFactories: toolFactories,
		registry:      registry,
		sessions:      sessions,
	}
}
```

- [ ] **Step 2: Wire session lookup and storage into RunTask**

In `RunTask`, after building the config (after `cfg.Logger = logz.New("warn")` around line 88), add session lookup:

```go
// Resume existing session if session_id provided (validate tool matches)
if req.SessionId != "" && s.sessions != nil {
	if entry, ok := s.sessions.Get(req.SessionId); ok && entry.Tool == req.Tool {
		cfg.SessionID = entry.ToolSessionID
	}
}
```

After the run completes (after `result := r.RunWithContext(runCtx, cfg)` around line 144), store the session:

```go
// Store session ID for multi-turn reuse
sessionID := ""
if result.SessionID != "" && s.sessions != nil {
	sessionID = runID // Use runID as the client-facing session ID
	s.sessions.Store(sessionID, req.Tool, result.SessionID)
}
```

Update the `InitEvent` send (around line 63) to include session_id — actually, it's better to return it in the `ResultEvent`. Update the result event construction to include the session ID:

```go
resultEvent := &pb.ResultEvent{
	ExitCode:     int32(result.ExitCode),
	TotalCostUsd: result.TotalCostUSD,
	SessionId:    sessionID,
}
```

- [ ] **Step 3: Update main.go to create and pass SessionStore**

In `cmd/rserve/main.go`, after `runRegistry` creation (line 66), add:

```go
sessionStore := server.NewSessionStore(30 * time.Minute)
```

Update the `NewServer` call (line 67):

```go
srv := server.NewServer(s, toolFactories, runRegistry, sessionStore)
```

Also pass `sessionStore` to `NewHandler` (will be wired in Task 6).

- [ ] **Step 4: Verify it compiles**

Run: `cd /Users/cliff/Desktop/_code/codegen_suite/rcodegen && go build ./cmd/rserve/`
Expected: No errors

- [ ] **Step 5: Commit**

```bash
git add pkg/server/server.go cmd/rserve/main.go
git commit -m "feat(rserve): wire session store into gRPC RunTask handler"
```

---

### Task 5: Update OpenAI HTTP types

**Files:**
- Modify: `pkg/server/openai/types.go` — add `SessionID` to request and response types

- [ ] **Step 1: Add SessionID to ChatCompletionRequest**

```go
type ChatCompletionRequest struct {
	Model     string    `json:"model"`
	Messages  []Message `json:"messages"`
	Stream    bool      `json:"stream,omitempty"`
	WorkDirs  []string  `json:"work_dirs,omitempty"`
	SessionID string    `json:"session_id,omitempty"`
}
```

- [ ] **Step 2: Add SessionID to ChatCompletionResponse and ChatCompletionChunk**

```go
type ChatCompletionResponse struct {
	ID        string   `json:"id"`
	Object    string   `json:"object"`
	Created   int64    `json:"created"`
	Model     string   `json:"model"`
	Choices   []Choice `json:"choices"`
	Usage     *Usage   `json:"usage,omitempty"`
	SessionID string   `json:"session_id,omitempty"`
}
```

```go
type ChatCompletionChunk struct {
	ID        string         `json:"id"`
	Object    string         `json:"object"`
	Created   int64          `json:"created"`
	Model     string         `json:"model"`
	Choices   []StreamChoice `json:"choices"`
	Usage     *Usage         `json:"usage,omitempty"`
	SessionID string         `json:"session_id,omitempty"`
}
```

- [ ] **Step 3: Verify it compiles**

Run: `cd /Users/cliff/Desktop/_code/codegen_suite/rcodegen && go build ./pkg/server/openai/...`
Expected: No errors

- [ ] **Step 4: Commit**

```bash
git add pkg/server/openai/types.go
git commit -m "feat(openai): add session_id to HTTP request/response types"
```

---

### Task 6: Wire sessions into HTTP handler

**Files:**
- Modify: `pkg/server/openai/handler.go` — accept session store, use it in chat completions
- Modify: `cmd/rserve/main.go` — pass session store to handler

- [ ] **Step 1: Add sessionStore to Handler**

In `pkg/server/openai/handler.go`, add `sessions` field:

```go
type Handler struct {
	mux            *http.ServeMux
	settings       *settings.Settings
	toolFactories  map[string]server.ToolFactory
	registry       *server.RunRegistry
	availableTools []string
	fileStore      *FileStore
	sessions       *server.SessionStore
}
```

Update `NewHandler` to accept it:

```go
func NewHandler(s *settings.Settings, toolFactories map[string]server.ToolFactory, registry *server.RunRegistry, availableTools []string, fileStore *FileStore, sessions *server.SessionStore) *Handler {
	h := &Handler{
		mux:            http.NewServeMux(),
		settings:       s,
		toolFactories:  toolFactories,
		registry:       registry,
		availableTools: availableTools,
		fileStore:      fileStore,
		sessions:       sessions,
	}
	// ... routes unchanged
```

- [ ] **Step 2: Wire session lookup into handleChatCompletions**

After building the config (after `cfg.Stderr = &bytes.Buffer{}`, around line 147), add:

```go
// Resume existing session if session_id provided (validate tool matches)
if req.SessionID != "" && h.sessions != nil {
	if entry, ok := h.sessions.Get(req.SessionID); ok && entry.Tool == toolName {
		cfg.SessionID = entry.ToolSessionID
	}
}
```

- [ ] **Step 3: Wire session storage into handleStreaming**

First, update `handleStreaming` signature to accept `toolName` (the `toolName` variable is already in scope in `handleChatCompletions` from the `ParseModel` call at line 105). Change the signature from:

```go
func (h *Handler) handleStreaming(w http.ResponseWriter, ctx context.Context, runID, model string, tool runner.Tool, cfg *runner.Config, showToolUse bool, cancel context.CancelFunc)
```

to:

```go
func (h *Handler) handleStreaming(w http.ResponseWriter, ctx context.Context, runID, model, toolName string, tool runner.Tool, cfg *runner.Config, showToolUse bool, cancel context.CancelFunc)
```

Update the call site in `handleChatCompletions` (line ~164) to pass `toolName`:

```go
h.handleStreaming(w, runCtx, runID, oaiModel, toolName, tool, cfg, showToolUse, cancel)
```

After `result := rn.RunWithContext(ctx, cfg)` in `handleStreaming` (around line 228):

```go
// Store session for multi-turn (use toolName, NOT cfg.Model)
sessionID := ""
if result.SessionID != "" && h.sessions != nil {
	sessionID = runID
	h.sessions.Store(sessionID, toolName, result.SessionID)
}
```

Add `SessionID: sessionID` to the `finalChunk`:

```go
finalChunk := ChatCompletionChunk{
	ID:        "chatcmpl-" + runID,
	Object:    "chat.completion.chunk",
	Created:   nowUnix(),
	Model:     model,
	SessionID: sessionID,
	Choices: []StreamChoice{
		{
			Index:        0,
			Delta:        Delta{},
			FinishReason: finishStop(),
		},
	},
}
```

- [ ] **Step 4: Wire session storage into handleNonStreaming**

Update `handleNonStreaming` signature to accept `toolName`:

```go
func (h *Handler) handleNonStreaming(w http.ResponseWriter, ctx context.Context, tool runner.Tool, cfg *runner.Config, model, toolName, runID string)
```

Update the call site in `handleChatCompletions` (line ~166) to pass `toolName`:

```go
h.handleNonStreaming(w, runCtx, tool, cfg, oaiModel, toolName, runID)
```

After `result := rn.RunWithContext(ctx, cfg)` in `handleNonStreaming` (around line 276):

```go
// Store session for multi-turn (use toolName, NOT cfg.Model)
sessionID := ""
if result.SessionID != "" && h.sessions != nil {
	sessionID = runID
	h.sessions.Store(sessionID, toolName, result.SessionID)
}
```

Add `SessionID: sessionID` to the response:

```go
resp := ChatCompletionResponse{
	ID:        "chatcmpl-" + runID,
	Object:    "chat.completion",
	Created:   nowUnix(),
	Model:     model,
	SessionID: sessionID,
	// ... rest unchanged
}
```

- [ ] **Step 5: Update main.go handler construction**

In `cmd/rserve/main.go`, update the `NewHandler` call (line 103):

```go
httpHandler := openai.NewHandler(s, toolFactories, runRegistry, availableTools, fileStore, sessionStore)
```

- [ ] **Step 6: Fix handler_test.go for new signature**

All `NewHandler` calls in test files need the new `sessions` parameter. Add `nil` as the last argument to each `NewHandler(...)` call in `pkg/server/openai/handler_test.go`:

```go
NewHandler(nil, nil, server.NewRunRegistry(5), []string{"claude", "gemini"}, nil, nil)
```

Also fix `files_test.go:34` — the `newTestHandler` helper:

```go
func newTestHandler(t *testing.T) (*Handler, *FileStore) {
	t.Helper()
	fs := newTestFileStore(t)
	h := NewHandler(nil, nil, server.NewRunRegistry(5), nil, fs, nil)
	return h, fs
}
```

- [ ] **Step 7: Verify everything compiles and tests pass**

Run: `cd /Users/cliff/Desktop/_code/codegen_suite/rcodegen && go build ./... && go test ./pkg/server/... -v`
Expected: All PASS

- [ ] **Step 8: Commit**

```bash
git add pkg/server/openai/handler.go pkg/server/openai/handler_test.go pkg/server/openai/files_test.go cmd/rserve/main.go
git commit -m "feat(rserve): wire session store into OpenAI HTTP handler"
```

---

### Task 7: Full build and integration test

**Files:**
- None new — verify full compilation and existing tests pass

- [ ] **Step 1: Build all binaries**

Run: `cd /Users/cliff/Desktop/_code/codegen_suite/rcodegen && make clean && make`
Expected: All 6 binaries build successfully

- [ ] **Step 2: Run all tests**

Run: `cd /Users/cliff/Desktop/_code/codegen_suite/rcodegen && make test`
Expected: All tests pass

- [ ] **Step 3: Verify session_id appears in binary**

Run: `cd /Users/cliff/Desktop/_code/codegen_suite/rcodegen && ./bin/rserve -v`
Expected: Shows version

- [ ] **Step 4: Commit any remaining fixes**

If any test fixes were needed, commit them.

---

### Task 8: Version bump, changelog, final commit

**Files:**
- Modify: `VERSION`
- Modify: `CHANGELOG.md`

- [ ] **Step 1: Read current VERSION**

Read the VERSION file to determine the next version.

- [ ] **Step 2: Increment version and update CHANGELOG**

Bump the patch version. Add a CHANGELOG entry:

```
## X.Y.Z

- Add multi-turn session support to rserve gRPC and HTTP APIs
- New `session_id` field in RunTaskRequest, ResultEvent, and OpenAI-compatible endpoints
- Sessions automatically expire after 30 minutes of inactivity
- Underlying tool CLIs (claude, codex, gemini) are resumed via their native `--resume` flag
```

- [ ] **Step 3: Rebuild with new version**

Run: `cd /Users/cliff/Desktop/_code/codegen_suite/rcodegen && make clean && make`

- [ ] **Step 4: Final commit and push**

```bash
git add -A
git commit -m "feat(rserve): multi-turn session support with session_id"
git push
```
