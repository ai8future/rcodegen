# OpenAI-Compatible HTTP Endpoint Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add an OpenAI-compatible `/v1/chat/completions` HTTP API to rserve so any OpenAI-speaking client can use rcodegen's tools.

**Architecture:** New `pkg/server/openai/` package with stdlib `net/http` handlers. Shares the existing `RunRegistry` and `ToolFactory` map from `pkg/server/`. HTTP server runs on gRPC port+1 alongside the existing gRPC server in the same process via `lifecycle.Run`.

**Tech Stack:** Go stdlib `net/http`, `encoding/json`, `os/exec` (for LookPath CLI detection)

---

### Task 1: OpenAI Types

**Files:**
- Create: `pkg/server/openai/types.go`

**Step 1: Create the types file**

Create `pkg/server/openai/types.go` with all OpenAI-compatible request and response structs:

```go
// Package openai implements an OpenAI-compatible HTTP API for rserve.
package openai

import "time"

// --- Request types ---

// ChatCompletionRequest is the OpenAI /v1/chat/completions request body.
type ChatCompletionRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Stream   bool      `json:"stream,omitempty"`
}

// Message is a single message in the messages array.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// --- Response types ---

// ChatCompletionResponse is the non-streaming response.
type ChatCompletionResponse struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   *Usage   `json:"usage,omitempty"`
}

// Choice is a single completion choice.
type Choice struct {
	Index        int      `json:"index"`
	Message      *Message `json:"message,omitempty"`
	FinishReason *string  `json:"finish_reason"`
}

// Usage contains token usage information.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// --- Streaming types ---

// ChatCompletionChunk is a single SSE chunk in streaming mode.
type ChatCompletionChunk struct {
	ID      string          `json:"id"`
	Object  string          `json:"object"`
	Created int64           `json:"created"`
	Model   string          `json:"model"`
	Choices []StreamChoice  `json:"choices"`
	Usage   *Usage          `json:"usage,omitempty"`
}

// StreamChoice is a choice within a streaming chunk.
type StreamChoice struct {
	Index        int    `json:"index"`
	Delta        Delta  `json:"delta"`
	FinishReason *string `json:"finish_reason"`
}

// Delta is the incremental content in a streaming chunk.
type Delta struct {
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
}

// --- Models types ---

// ModelList is the response for GET /v1/models.
type ModelList struct {
	Object string      `json:"object"`
	Data   []ModelInfo `json:"data"`
}

// ModelInfo describes a single available model.
type ModelInfo struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

// --- Health types ---

// HealthResponse is the response for GET /health.
type HealthResponse struct {
	Status        string `json:"status"`
	Version       string `json:"version"`
	ActiveRuns    int    `json:"active_runs"`
	MaxConcurrent int    `json:"max_concurrent"`
}

// --- Error types ---

// ErrorResponse wraps an API error in OpenAI format.
type ErrorResponse struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail is the error body.
type ErrorDetail struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code"`
}

// NewErrorResponse creates an ErrorResponse.
func NewErrorResponse(msg, errType, code string) ErrorResponse {
	return ErrorResponse{Error: ErrorDetail{Message: msg, Type: errType, Code: code}}
}

// finishStop is a convenience for the "stop" finish reason.
func finishStop() *string {
	s := "stop"
	return &s
}

// startedAt returns the current Unix timestamp.
func nowUnix() int64 {
	return time.Now().Unix()
}
```

**Step 2: Verify it compiles**

Run: `cd /Users/cliff/Desktop/_code/codegen_suite/rcodegen && go build ./pkg/server/openai/`
Expected: no errors

**Step 3: Commit**

```bash
git add pkg/server/openai/types.go
git commit -m "feat(openai): add OpenAI-compatible request/response types"
```

---

### Task 2: Model Parsing and CLI Detection

**Files:**
- Create: `pkg/server/openai/models.go`
- Create: `pkg/server/openai/models_test.go`

**Step 1: Write the failing tests**

Create `pkg/server/openai/models_test.go`:

```go
package openai

import "testing"

func TestParseModel(t *testing.T) {
	tests := []struct {
		input     string
		wantTool  string
		wantModel string
	}{
		{"claude", "claude", ""},
		{"claude:opus-4", "claude", "opus-4"},
		{"codex:o3-pro", "codex", "o3-pro"},
		{"gemini", "gemini", ""},
		{"gemini:2.5-flash", "gemini", "2.5-flash"},
		{"claude:sonnet-4:thinking", "claude", "sonnet-4:thinking"},
	}
	for _, tt := range tests {
		tool, model := ParseModel(tt.input)
		if tool != tt.wantTool || model != tt.wantModel {
			t.Errorf("ParseModel(%q) = (%q, %q), want (%q, %q)",
				tt.input, tool, model, tt.wantTool, tt.wantModel)
		}
	}
}

func TestParseModelInvalid(t *testing.T) {
	tool, model := ParseModel("")
	if tool != "" || model != "" {
		t.Errorf("ParseModel(\"\") = (%q, %q), want (\"\", \"\")", tool, model)
	}
}

func TestExtractTaskPrompt(t *testing.T) {
	tests := []struct {
		name     string
		messages []Message
		want     string
	}{
		{
			name: "user only",
			messages: []Message{
				{Role: "user", Content: "fix the bug"},
			},
			want: "fix the bug",
		},
		{
			name: "system + user",
			messages: []Message{
				{Role: "system", Content: "You are a Go expert."},
				{Role: "user", Content: "fix the bug"},
			},
			want: "You are a Go expert.\n\nfix the bug",
		},
		{
			name: "multi-turn takes last user",
			messages: []Message{
				{Role: "system", Content: "Be concise."},
				{Role: "user", Content: "hello"},
				{Role: "assistant", Content: "hi"},
				{Role: "user", Content: "fix the bug"},
			},
			want: "Be concise.\n\nfix the bug",
		},
		{
			name: "multiple system messages concatenated",
			messages: []Message{
				{Role: "system", Content: "Rule 1."},
				{Role: "system", Content: "Rule 2."},
				{Role: "user", Content: "do it"},
			},
			want: "Rule 1.\nRule 2.\n\ndo it",
		},
		{
			name: "no user message",
			messages: []Message{
				{Role: "system", Content: "hello"},
			},
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractTaskPrompt(tt.messages)
			if got != tt.want {
				t.Errorf("ExtractTaskPrompt() = %q, want %q", got, tt.want)
			}
		})
	}
}
```

**Step 2: Run tests to verify they fail**

Run: `cd /Users/cliff/Desktop/_code/codegen_suite/rcodegen && go test ./pkg/server/openai/ -v -run 'TestParseModel|TestExtractTaskPrompt'`
Expected: FAIL (functions don't exist yet)

**Step 3: Write the implementation**

Create `pkg/server/openai/models.go`:

```go
package openai

import (
	"os/exec"
	"strings"

	"rcodegen/pkg/runner"
	"rcodegen/pkg/server"
)

// ParseModel splits a model string like "claude:opus-4" into (tool, model).
// Only the first colon is split on — "claude:sonnet-4:thinking" → ("claude", "sonnet-4:thinking").
// Returns ("", "") for empty input.
func ParseModel(s string) (tool, model string) {
	if s == "" {
		return "", ""
	}
	if idx := strings.IndexByte(s, ':'); idx >= 0 {
		return s[:idx], s[idx+1:]
	}
	return s, ""
}

// ExtractTaskPrompt collapses an OpenAI messages array into a single task prompt.
// System messages are concatenated as a preamble, the last user message is the task.
// Returns "" if no user message is found.
func ExtractTaskPrompt(messages []Message) string {
	var systemParts []string
	var lastUser string
	foundUser := false

	for _, m := range messages {
		switch m.Role {
		case "system":
			systemParts = append(systemParts, m.Content)
		case "user":
			lastUser = m.Content
			foundUser = true
		}
	}

	if !foundUser {
		return ""
	}

	if len(systemParts) > 0 {
		return strings.Join(systemParts, "\n") + "\n\n" + lastUser
	}
	return lastUser
}

// DetectAvailableTools probes which tool CLIs are on $PATH.
// Returns a subset of toolFactories keys whose BinaryName is found.
func DetectAvailableTools(toolFactories map[string]server.ToolFactory) []string {
	var available []string
	for name, factory := range toolFactories {
		tool := factory()
		if _, err := exec.LookPath(tool.BinaryName()); err == nil {
			available = append(available, name)
		}
	}
	return available
}

// BuildModelList creates the /v1/models response from detected tools.
func BuildModelList(available []string) ModelList {
	ts := nowUnix()
	data := make([]ModelInfo, len(available))
	for i, name := range available {
		data[i] = ModelInfo{
			ID:      name,
			Object:  "model",
			Created: ts,
			OwnedBy: "rcodegen",
		}
	}
	return ModelList{Object: "list", Data: data}
}

// ToolVersion returns the server version string.
func ToolVersion() string {
	return runner.GetVersion()
}
```

**Step 4: Run tests to verify they pass**

Run: `cd /Users/cliff/Desktop/_code/codegen_suite/rcodegen && go test ./pkg/server/openai/ -v -run 'TestParseModel|TestExtractTaskPrompt'`
Expected: PASS

**Step 5: Commit**

```bash
git add pkg/server/openai/models.go pkg/server/openai/models_test.go
git commit -m "feat(openai): add model parsing, message extraction, CLI detection"
```

---

### Task 3: SSE Writer

**Files:**
- Create: `pkg/server/openai/sse.go`
- Create: `pkg/server/openai/sse_test.go`

**Step 1: Write the failing test**

Create `pkg/server/openai/sse_test.go`:

```go
package openai

import (
	"bytes"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSSEWriter_WriteChunk(t *testing.T) {
	w := httptest.NewRecorder()
	sse := NewSSEWriter(w)

	chunk := ChatCompletionChunk{
		ID:      "run-123",
		Object:  "chat.completion.chunk",
		Created: 1000,
		Model:   "claude",
		Choices: []StreamChoice{{
			Index: 0,
			Delta: Delta{Content: "hello"},
		}},
	}

	if err := sse.WriteChunk(chunk); err != nil {
		t.Fatalf("WriteChunk: %v", err)
	}

	body := w.Body.String()
	if !strings.HasPrefix(body, "data: ") {
		t.Errorf("expected 'data: ' prefix, got %q", body[:20])
	}
	if !strings.Contains(body, `"hello"`) {
		t.Errorf("expected content in body, got %q", body)
	}
	if !strings.HasSuffix(body, "\n\n") {
		t.Errorf("expected trailing \\n\\n, got %q", body[len(body)-4:])
	}
}

func TestSSEWriter_WriteDone(t *testing.T) {
	w := httptest.NewRecorder()
	sse := NewSSEWriter(w)

	sse.WriteDone()

	if got := w.Body.String(); got != "data: [DONE]\n\n" {
		t.Errorf("WriteDone() = %q, want %q", got, "data: [DONE]\n\n")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `cd /Users/cliff/Desktop/_code/codegen_suite/rcodegen && go test ./pkg/server/openai/ -v -run TestSSEWriter`
Expected: FAIL

**Step 3: Write the implementation**

Create `pkg/server/openai/sse.go`:

```go
package openai

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// SSEWriter writes Server-Sent Events to an http.ResponseWriter.
type SSEWriter struct {
	w       http.ResponseWriter
	flusher http.Flusher
}

// NewSSEWriter creates an SSE writer. Sets the required headers.
func NewSSEWriter(w http.ResponseWriter) *SSEWriter {
	sw := &SSEWriter{w: w}
	if f, ok := w.(http.Flusher); ok {
		sw.flusher = f
	}
	return sw
}

// SetHeaders sets SSE response headers. Call before any writes.
func (s *SSEWriter) SetHeaders() {
	s.w.Header().Set("Content-Type", "text/event-stream")
	s.w.Header().Set("Cache-Control", "no-cache")
	s.w.Header().Set("Connection", "keep-alive")
	s.w.Header().Set("X-Accel-Buffering", "no")
}

// WriteChunk encodes a chunk as a JSON SSE data line and flushes.
func (s *SSEWriter) WriteChunk(chunk ChatCompletionChunk) error {
	data, err := json.Marshal(chunk)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(s.w, "data: %s\n\n", data); err != nil {
		return err
	}
	if s.flusher != nil {
		s.flusher.Flush()
	}
	return nil
}

// WriteDone writes the final [DONE] sentinel and flushes.
func (s *SSEWriter) WriteDone() {
	fmt.Fprint(s.w, "data: [DONE]\n\n")
	if s.flusher != nil {
		s.flusher.Flush()
	}
}
```

**Step 4: Run tests to verify they pass**

Run: `cd /Users/cliff/Desktop/_code/codegen_suite/rcodegen && go test ./pkg/server/openai/ -v -run TestSSEWriter`
Expected: PASS

**Step 5: Commit**

```bash
git add pkg/server/openai/sse.go pkg/server/openai/sse_test.go
git commit -m "feat(openai): add SSE writer for streaming responses"
```

---

### Task 4: HTTP Handler — `/v1/models` and `/health`

**Files:**
- Create: `pkg/server/openai/handler.go`
- Create: `pkg/server/openai/handler_test.go`

**Step 1: Write the failing tests**

Create `pkg/server/openai/handler_test.go`:

```go
package openai

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"rcodegen/pkg/server"
)

func TestHandleModels(t *testing.T) {
	h := NewHandler(nil, nil, nil, []string{"claude", "gemini"})

	req := httptest.NewRequest("GET", "/v1/models", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp ModelList
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Object != "list" {
		t.Errorf("object = %q, want %q", resp.Object, "list")
	}
	if len(resp.Data) != 2 {
		t.Errorf("models count = %d, want 2", len(resp.Data))
	}
}

func TestHandleHealth(t *testing.T) {
	reg := server.NewRunRegistry(5)
	h := NewHandler(nil, nil, reg, nil)

	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp HealthResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Status != "ok" {
		t.Errorf("status = %q, want %q", resp.Status, "ok")
	}
	if resp.MaxConcurrent != 5 {
		t.Errorf("max_concurrent = %d, want 5", resp.MaxConcurrent)
	}
}

func TestHandleChatCompletions_BadMethod(t *testing.T) {
	h := NewHandler(nil, nil, nil, []string{"claude"})

	req := httptest.NewRequest("GET", "/v1/chat/completions", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
}

func TestHandleChatCompletions_UnknownTool(t *testing.T) {
	h := NewHandler(nil, map[string]server.ToolFactory{}, nil, []string{})

	body := `{"model":"unknown","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}
```

Note: Add `"strings"` to the import block.

**Step 2: Run tests to verify they fail**

Run: `cd /Users/cliff/Desktop/_code/codegen_suite/rcodegen && go test ./pkg/server/openai/ -v -run 'TestHandleModels|TestHandleHealth|TestHandleChatCompletions_Bad|TestHandleChatCompletions_Unknown'`
Expected: FAIL

**Step 3: Write the implementation**

Create `pkg/server/openai/handler.go`:

```go
package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"

	"rcodegen/pkg/runner"
	"rcodegen/pkg/server"
	"rcodegen/pkg/settings"

	"github.com/ai8future/chassis-go/v9/logz"
)

// Handler implements the OpenAI-compatible HTTP API.
type Handler struct {
	mux           *http.ServeMux
	settings      *settings.Settings
	toolFactories map[string]server.ToolFactory
	registry      *server.RunRegistry
	availableTools []string
}

// NewHandler creates a new HTTP handler with all routes registered.
func NewHandler(
	s *settings.Settings,
	toolFactories map[string]server.ToolFactory,
	registry *server.RunRegistry,
	availableTools []string,
) *Handler {
	h := &Handler{
		mux:            http.NewServeMux(),
		settings:       s,
		toolFactories:  toolFactories,
		registry:       registry,
		availableTools: availableTools,
	}
	h.mux.HandleFunc("/v1/chat/completions", h.handleChatCompletions)
	h.mux.HandleFunc("/v1/models", h.handleModels)
	h.mux.HandleFunc("/health", h.handleHealth)
	return h
}

// ServeHTTP dispatches to the internal mux.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mux.ServeHTTP(w, r)
}

func (h *Handler) handleModels(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, BuildModelList(h.availableTools))
}

func (h *Handler) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, HealthResponse{
		Status:        "ok",
		Version:       ToolVersion(),
		ActiveRuns:    h.registry.ActiveCount(),
		MaxConcurrent: h.registry.MaxConcurrent(),
	})
}

func (h *Handler) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed,
			NewErrorResponse("method not allowed", "invalid_request_error", "method_not_allowed"))
		return
	}

	var req ChatCompletionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest,
			NewErrorResponse("invalid JSON: "+err.Error(), "invalid_request_error", "invalid_json"))
		return
	}

	toolName, modelOverride := ParseModel(req.Model)

	factory, ok := h.toolFactories[toolName]
	if !ok {
		writeJSON(w, http.StatusBadRequest,
			NewErrorResponse(fmt.Sprintf("unknown tool: %q (use claude, codex, or gemini)", toolName),
				"invalid_request_error", "model_not_found"))
		return
	}

	task := ExtractTaskPrompt(req.Messages)
	if task == "" {
		writeJSON(w, http.StatusBadRequest,
			NewErrorResponse("no user message found in messages array",
				"invalid_request_error", "missing_user_message"))
		return
	}

	showToolUse := r.Header.Get("X-Show-Tool-Use") == "true"

	// Create tool instance
	tool := factory()

	// Acquire concurrency slot
	runID, runCtx, cancel, err := h.registry.Acquire(r.Context(), toolName, task)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable,
			NewErrorResponse("server busy: "+err.Error(), "server_error", "capacity_exceeded"))
		return
	}
	defer cancel()
	defer h.registry.Release(runID)

	// Inject settings
	if sa, ok := tool.(runner.SettingsAware); ok && h.settings != nil {
		sa.SetSettings(h.settings)
	}

	// Build runner config
	cfg := runner.NewConfig()
	cfg.Task = task
	cfg.Output = io.Discard
	cfg.Logger = logz.New("warn")

	var stderrBuf bytes.Buffer
	cfg.Stderr = &stderrBuf

	tool.ApplyToolDefaults(cfg)
	if modelOverride != "" {
		cfg.Model = modelOverride
	}
	if cfg.Model == "" {
		cfg.Model = tool.DefaultModel()
	}

	oaiModel := req.Model // echo back the original model string

	if req.Stream {
		h.handleStreaming(w, runCtx, runID, oaiModel, tool, cfg, showToolUse, cancel)
	} else {
		h.handleNonStreaming(w, runCtx, tool, cfg, oaiModel, runID)
	}
}

func (h *Handler) handleStreaming(
	w http.ResponseWriter,
	ctx context.Context,
	runID, model string,
	tool runner.Tool,
	cfg *runner.Config,
	showToolUse bool,
	cancel context.CancelFunc,
) {
	sse := NewSSEWriter(w)
	sse.SetHeaders()

	// Send initial role chunk
	sse.WriteChunk(ChatCompletionChunk{
		ID: "run-" + runID, Object: "chat.completion.chunk", Created: nowUnix(), Model: model,
		Choices: []StreamChoice{{Index: 0, Delta: Delta{Role: "assistant"}}},
	})

	var sendMu sync.Mutex

	cfg.OnStreamEvent = func(event *runner.StreamEvent) {
		sendMu.Lock()
		defer sendMu.Unlock()

		switch event.Type {
		case "assistant":
			if event.Message == nil {
				return
			}
			for _, block := range event.Message.Content {
				switch block.Type {
				case "text":
					if block.Text != "" {
						sse.WriteChunk(ChatCompletionChunk{
							ID: "run-" + runID, Object: "chat.completion.chunk", Created: nowUnix(), Model: model,
							Choices: []StreamChoice{{Index: 0, Delta: Delta{Content: block.Text}}},
						})
					}
				case "tool_use":
					if showToolUse {
						summary := formatToolUse(block)
						sse.WriteChunk(ChatCompletionChunk{
							ID: "run-" + runID, Object: "chat.completion.chunk", Created: nowUnix(), Model: model,
							Choices: []StreamChoice{{Index: 0, Delta: Delta{Content: summary}}},
						})
					}
				}
			}
		}
	}

	rn := &runner.Runner{Tool: tool, Settings: h.settings}
	result := rn.RunWithContext(ctx, cfg)

	// Send final chunk with finish_reason and optional usage
	finalChunk := ChatCompletionChunk{
		ID: "run-" + runID, Object: "chat.completion.chunk", Created: nowUnix(), Model: model,
		Choices: []StreamChoice{{Index: 0, Delta: Delta{}, FinishReason: finishStop()}},
	}
	if result.TokenUsage != nil {
		finalChunk.Usage = &Usage{
			PromptTokens:     result.TokenUsage.InputTokens,
			CompletionTokens: result.TokenUsage.OutputTokens,
			TotalTokens:      result.TokenUsage.InputTokens + result.TokenUsage.OutputTokens,
		}
	}
	sendMu.Lock()
	sse.WriteChunk(finalChunk)
	sse.WriteDone()
	sendMu.Unlock()
}

func (h *Handler) handleNonStreaming(
	w http.ResponseWriter,
	ctx context.Context,
	tool runner.Tool,
	cfg *runner.Config,
	model, runID string,
) {
	// Collect all text output
	var textBuf bytes.Buffer
	cfg.OnStreamEvent = func(event *runner.StreamEvent) {
		if event.Type == "assistant" && event.Message != nil {
			for _, block := range event.Message.Content {
				if block.Type == "text" && block.Text != "" {
					textBuf.WriteString(block.Text)
				}
			}
		}
	}

	rn := &runner.Runner{Tool: tool, Settings: h.settings}
	result := rn.RunWithContext(ctx, cfg)

	resp := ChatCompletionResponse{
		ID:      "run-" + runID,
		Object:  "chat.completion",
		Created: nowUnix(),
		Model:   model,
		Choices: []Choice{{
			Index:        0,
			Message:      &Message{Role: "assistant", Content: textBuf.String()},
			FinishReason: finishStop(),
		}},
	}
	if result.TokenUsage != nil {
		resp.Usage = &Usage{
			PromptTokens:     result.TokenUsage.InputTokens,
			CompletionTokens: result.TokenUsage.OutputTokens,
			TotalTokens:      result.TokenUsage.InputTokens + result.TokenUsage.OutputTokens,
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

// formatToolUse creates a text annotation for a tool use block.
func formatToolUse(block runner.ContentBlock) string {
	summary := block.Name
	if len(block.Input) > 0 {
		var inputMap map[string]interface{}
		if json.Unmarshal(block.Input, &inputMap) == nil {
			for _, key := range []string{"file_path", "command", "pattern", "description"} {
				if v, ok := inputMap[key].(string); ok {
					summary = block.Name + ": " + v
					break
				}
			}
		}
	}
	return "[" + summary + "]\n"
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
```

**Step 4: Run tests to verify they pass**

Run: `cd /Users/cliff/Desktop/_code/codegen_suite/rcodegen && go test ./pkg/server/openai/ -v`
Expected: ALL PASS

**Step 5: Commit**

```bash
git add pkg/server/openai/handler.go pkg/server/openai/handler_test.go
git commit -m "feat(openai): add HTTP handler with /v1/chat/completions, /v1/models, /health"
```

---

### Task 5: Wire HTTP Server into rserve main

**Files:**
- Modify: `cmd/rserve/main.go`

**Step 1: Modify main.go to start HTTP server alongside gRPC**

Add the following changes to `cmd/rserve/main.go`:

1. Add import: `"rcodegen/pkg/server/openai"` and `"net/http"`
2. After `runRegistry` is created, detect available tools:

```go
availableTools := openai.DetectAvailableTools(toolFactories)
logger.Info("detected tools", "available", availableTools)
```

3. Create the HTTP handler:

```go
httpHandler := openai.NewHandler(s, toolFactories, runRegistry, availableTools)
httpPort := *port + 1
```

4. Add an HTTP server component to the `components` slice (after the gRPC component):

```go
// OpenAI-compatible HTTP server on port+1
func(ctx context.Context) error {
    httpServer := &http.Server{
        Addr:    fmt.Sprintf("127.0.0.1:%d", httpPort),
        Handler: httpHandler,
    }
    errCh := make(chan error, 1)
    go func() { errCh <- httpServer.ListenAndServe() }()
    logger.Info("openai HTTP server listening", "port", httpPort)
    select {
    case <-ctx.Done():
        shutCtx, shutCancel := context.WithTimeout(context.Background(), 10*time.Second)
        defer shutCancel()
        return httpServer.Shutdown(shutCtx)
    case err := <-errCh:
        if err == http.ErrServerClosed {
            return nil
        }
        return err
    }
},
```

5. Register the HTTP port in the chassis registry:

```go
registry.Port(chassis.PortHTTP, httpPort, "OpenAI-compatible HTTP API")
```

6. Update the startup log to mention both ports:

```go
logger.Info("rserve starting",
    "version", runner.GetVersion(),
    "grpc_port", *port,
    "http_port", httpPort,
    "max_concurrent", *maxConcurrent,
)
```

**Step 2: Verify it compiles**

Run: `cd /Users/cliff/Desktop/_code/codegen_suite/rcodegen && make rserve`
Expected: builds successfully

**Step 3: Commit**

```bash
git add cmd/rserve/main.go
git commit -m "feat(rserve): start OpenAI HTTP server alongside gRPC on port+1"
```

---

### Task 6: Manual Smoke Test

**Step 1: Start rserve**

Run: `cd /Users/cliff/Desktop/_code/codegen_suite/rcodegen && ./bin/rserve`
Expected: log output shows both gRPC and HTTP ports

**Step 2: Test /v1/models**

Run: `curl -s http://127.0.0.1:<http_port>/v1/models | jq .`
Expected: JSON with `"object": "list"` and detected tools in `data`

**Step 3: Test /health**

Run: `curl -s http://127.0.0.1:<http_port>/health | jq .`
Expected: `{"status":"ok","version":"...","active_runs":0,"max_concurrent":3}`

**Step 4: Test streaming chat completion (if claude CLI is available)**

Run:
```bash
curl -s -N http://127.0.0.1:<http_port>/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"claude","messages":[{"role":"user","content":"say hello in one word"}],"stream":true}'
```
Expected: SSE stream with `data: {...}` lines ending with `data: [DONE]`

**Step 5: Test non-streaming**

Run:
```bash
curl -s http://127.0.0.1:<http_port>/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"claude","messages":[{"role":"user","content":"say hello in one word"}]}' | jq .
```
Expected: JSON with `choices[0].message.content` and `usage`

**Step 6: Test X-Show-Tool-Use header**

Run:
```bash
curl -s -N http://127.0.0.1:<http_port>/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "X-Show-Tool-Use: true" \
  -d '{"model":"claude","messages":[{"role":"system","content":"Read VERSION file and report it"},{"role":"user","content":"what version is this project?"}],"stream":true}'
```
Expected: SSE stream includes `[Read: ...]` annotations between text chunks

---

### Task 7: Version Bump, CHANGELOG, and Final Build

**Step 1: Read current VERSION (at the last second per AGENTS.md)**

Run: `cat VERSION`

**Step 2: Increment version and update CHANGELOG**

Increment the version. Add a CHANGELOG entry:

```
## vX.Y.Z - 2026-03-12
- feat: add OpenAI-compatible HTTP endpoint to rserve (/v1/chat/completions, /v1/models, /health)
  - Streaming SSE and non-streaming JSON responses
  - Model routing via prefixed format (claude:opus-4, codex:o3-pro, etc.)
  - CLI detection at startup for /v1/models
  - Optional tool-use visibility via X-Show-Tool-Use header
  - Agent: Claude:Opus 4.6
```

**Step 3: Build all binaries**

Run: `make clean && make`
Expected: all 6 binaries built successfully

**Step 4: Run tests**

Run: `make test`
Expected: all tests pass

**Step 5: Commit and push**

```bash
git add -A
git commit -m "feat: add OpenAI-compatible HTTP endpoint to rserve

Adds /v1/chat/completions (streaming SSE + non-streaming JSON),
/v1/models (CLI auto-detection), and /health endpoints.
HTTP server runs on gRPC port+1, sharing concurrency limits."
git push
```
