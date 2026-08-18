package openai

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"

	"rcodegen/pkg/runner"
	"rcodegen/pkg/server"
	"rcodegen/pkg/settings"

	"github.com/ai8future/chassis-go/v11/logz"
)

// Handler implements the OpenAI-compatible HTTP API.
type Handler struct {
	mux            *http.ServeMux
	settings       *settings.Settings
	toolFactories  map[string]server.ToolFactory
	registry       *server.RunRegistry
	availableTools []string
	fileStore      *FileStore
	sessions       *server.SessionStore
	authToken      string        // optional bearer token from RSERVE_TOKEN; empty = no auth
	runBundleFn    bundleRunFunc // bundle execution, replaceable in tests
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// NewHandler creates a new Handler and registers routes on its internal mux.
// If fileStore is non-nil, file upload/download endpoints are enabled.
func NewHandler(s *settings.Settings, toolFactories map[string]server.ToolFactory, registry *server.RunRegistry, availableTools []string, fileStore *FileStore, sessions *server.SessionStore) *Handler {
	h := &Handler{
		mux:            http.NewServeMux(),
		settings:       s,
		toolFactories:  toolFactories,
		registry:       registry,
		availableTools: availableTools,
		fileStore:      fileStore,
		sessions:       sessions,
		authToken:      os.Getenv("RSERVE_TOKEN"),
		runBundleFn:    defaultBundleRun(s),
	}
	h.mux.HandleFunc("/v1/chat/completions", h.handleChatCompletions)
	h.mux.HandleFunc("/v1/models", h.handleModels)
	h.mux.HandleFunc("/v1/bundles", h.handleBundles)
	h.mux.HandleFunc("/v1/bundles/", h.handleBundleByName)
	h.mux.HandleFunc("/health", h.handleHealth)
	if fileStore != nil {
		h.mux.HandleFunc("/v1/files", h.handleFiles)
		h.mux.HandleFunc("/v1/files/", h.handleFileByID)
	}
	return h
}

// handleFiles routes /v1/files to upload (POST) or list (GET).
func (h *Handler) handleFiles(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		h.handleUploadFile(w, r)
	case http.MethodGet:
		h.handleListFiles(w, r)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, NewErrorResponse(
			"method not allowed", "invalid_request_error", "method_not_allowed",
		))
	}
}

// ServeHTTP enforces optional bearer-token auth, then delegates to the internal mux.
// Auth is enabled by setting the RSERVE_TOKEN environment variable; /health stays
// open so monitoring keeps working.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.authToken != "" && r.URL.Path != "/health" {
		want := "Bearer " + h.authToken
		got := r.Header.Get("Authorization")
		if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
			writeJSON(w, http.StatusUnauthorized, NewErrorResponse(
				"invalid or missing bearer token", "invalid_request_error", "unauthorized",
			))
			return
		}
	}
	h.mux.ServeHTTP(w, r)
}

// handleModels returns the list of available tools and every valid
// tool:model combination.
func (h *Handler) handleModels(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, BuildModelList(h.availableTools, h.toolFactories))
}

// handleHealth returns server health information.
func (h *Handler) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, HealthResponse{
		Status:        "ok",
		Version:       ToolVersion(),
		ActiveRuns:    h.registry.ActiveCount(),
		MaxConcurrent: h.registry.MaxConcurrent(),
	})
}

// handleChatCompletions is the main handler for chat completion requests.
func (h *Handler) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, NewErrorResponse(
			"method not allowed", "invalid_request_error", "method_not_allowed",
		))
		return
	}

	// Limit request body to 10MB to prevent memory exhaustion from oversized payloads.
	const maxBodySize = 10 << 20 // 10 MB
	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)

	var req ChatCompletionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, NewErrorResponse(
			"invalid JSON: "+err.Error(), "invalid_request_error", "invalid_json",
		))
		return
	}

	toolName, modelOverride := ParseModel(req.Model)

	factory, ok := h.toolFactories[toolName]
	if !ok {
		writeJSON(w, http.StatusBadRequest, NewErrorResponse(
			fmt.Sprintf("unknown tool: %s", toolName), "invalid_request_error", "unknown_tool",
		))
		return
	}

	task := ExtractTaskPrompt(req.Messages)
	if task == "" {
		writeJSON(w, http.StatusBadRequest, NewErrorResponse(
			"no user message found", "invalid_request_error", "empty_task",
		))
		return
	}

	showToolUse := r.Header.Get("X-Show-Tool-Use") == "true"

	tool := factory()

	// Reject unknown models up front with the valid list — a bad model passed
	// through to the CLI fails silently (200 with empty content). GET
	// /v1/models enumerates every valid tool:model combination.
	if modelOverride != "" {
		if err := runner.ValidateModel(tool, modelOverride); err != nil {
			writeJSON(w, http.StatusBadRequest, NewErrorResponse(
				err.Error(), "invalid_request_error", "invalid_model",
			))
			return
		}
	}

	runID, runCtx, cancel, err := h.registry.Acquire(r.Context(), toolName, task)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, NewErrorResponse(
			"failed to acquire run slot: "+err.Error(), "server_error", "concurrency_limit",
		))
		return
	}
	defer cancel()
	defer h.registry.Release(runID)

	// Inject settings if the tool supports it
	if sa, ok := tool.(runner.SettingsAware); ok && h.settings != nil {
		sa.SetSettings(h.settings)
	}

	// Build config
	cfg := runner.NewConfig()
	cfg.Task = task
	cfg.Output = io.Discard
	cfg.Logger = logz.New("warn")
	cfg.Stderr = &bytes.Buffer{}
	if len(req.WorkDirs) > 0 {
		cfg.WorkDirs = req.WorkDirs
	}

	// Resume existing session if session_id provided (validate tool matches)
	if req.SessionID != "" && h.sessions != nil {
		if entry, ok := h.sessions.Get(req.SessionID); ok && entry.Tool == toolName {
			cfg.SessionID = entry.ToolSessionID
		}
	}

	// Apply tool defaults, then model override, then fallback
	tool.ApplyToolDefaults(cfg)
	if modelOverride != "" {
		cfg.Model = modelOverride
	}
	if cfg.Model == "" {
		cfg.Model = tool.DefaultModel()
	}

	oaiModel := req.Model // echo back the original model string

	if req.Stream {
		h.handleStreaming(w, runCtx, runID, oaiModel, toolName, tool, cfg, showToolUse, cancel)
	} else {
		h.handleNonStreaming(w, runCtx, tool, cfg, oaiModel, toolName, runID)
	}
}

// handleStreaming handles a streaming chat completion request.
func (h *Handler) handleStreaming(w http.ResponseWriter, ctx context.Context, runID, model, toolName string, tool runner.Tool, cfg *runner.Config, showToolUse bool, cancel context.CancelFunc) {
	sse := NewSSEWriter(w)
	sse.SetHeaders()

	// Send initial role chunk
	_ = sse.WriteChunk(ChatCompletionChunk{
		ID:      "chatcmpl-" + runID,
		Object:  "chat.completion.chunk",
		Created: nowUnix(),
		Model:   model,
		Choices: []StreamChoice{
			{
				Index: 0,
				Delta: Delta{Role: "assistant"},
			},
		},
	})

	var mu sync.Mutex
	if !tool.UsesStreamOutput() {
		cfg.Output = writerFunc(func(p []byte) (int, error) {
			chunk := ChatCompletionChunk{
				ID: "chatcmpl-" + runID, Object: "chat.completion.chunk", Created: nowUnix(), Model: model,
				Choices: []StreamChoice{{Index: 0, Delta: Delta{Content: string(p)}}},
			}
			mu.Lock()
			err := sse.WriteChunk(chunk)
			mu.Unlock()
			if err != nil {
				cancel()
				return 0, err
			}
			return len(p), nil
		})
	}
	cfg.OnStreamEvent = func(event *runner.StreamEvent) {
		if event.Type != "assistant" || event.Message == nil {
			return
		}
		for _, block := range event.Message.Content {
			var chunk *ChatCompletionChunk
			switch block.Type {
			case "text":
				if block.Text != "" {
					chunk = &ChatCompletionChunk{
						ID: "chatcmpl-" + runID, Object: "chat.completion.chunk", Created: nowUnix(), Model: model,
						Choices: []StreamChoice{{Index: 0, Delta: Delta{Content: block.Text}}},
					}
				}
			case "tool_use":
				if showToolUse {
					chunk = &ChatCompletionChunk{
						ID: "chatcmpl-" + runID, Object: "chat.completion.chunk", Created: nowUnix(), Model: model,
						Choices: []StreamChoice{{Index: 0, Delta: Delta{Content: formatToolUse(block)}}},
					}
				}
			}
			if chunk != nil {
				mu.Lock()
				err := sse.WriteChunk(*chunk)
				mu.Unlock()
				if err != nil {
					cancel() // Stop the subprocess — client is gone
					return
				}
			}
		}
	}

	rn := &runner.Runner{
		Tool:     tool,
		Settings: h.settings,
	}
	result := rn.RunWithContext(ctx, cfg)

	// Store session for multi-turn (use toolName, NOT cfg.Model)
	sessionID := ""
	if result.SessionID != "" && h.sessions != nil {
		sessionID = runID
		h.sessions.Store(sessionID, toolName, result.SessionID)
	}

	// Send final chunk with finish_reason and optional usage
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
	if result.TokenUsage != nil {
		finalChunk.Usage = &Usage{
			PromptTokens:     result.TokenUsage.InputTokens,
			CompletionTokens: result.TokenUsage.OutputTokens,
			TotalTokens:      result.TokenUsage.InputTokens + result.TokenUsage.OutputTokens,
		}
	}
	mu.Lock()
	_ = sse.WriteChunk(finalChunk)
	mu.Unlock()

	sse.WriteDone()
}

// handleNonStreaming handles a non-streaming chat completion request.
func (h *Handler) handleNonStreaming(w http.ResponseWriter, ctx context.Context, tool runner.Tool, cfg *runner.Config, model, toolName, runID string) {
	var buf bytes.Buffer
	if !tool.UsesStreamOutput() {
		cfg.Output = &buf
	}
	cfg.OnStreamEvent = func(event *runner.StreamEvent) {
		if event.Type != "assistant" || event.Message == nil {
			return
		}
		for _, block := range event.Message.Content {
			if block.Type == "text" && block.Text != "" {
				buf.WriteString(block.Text)
			}
		}
	}

	rn := &runner.Runner{
		Tool:     tool,
		Settings: h.settings,
	}
	result := rn.RunWithContext(ctx, cfg)

	// Store session for multi-turn (use toolName, NOT cfg.Model)
	sessionID := ""
	if result.SessionID != "" && h.sessions != nil {
		sessionID = runID
		h.sessions.Store(sessionID, toolName, result.SessionID)
	}

	resp := ChatCompletionResponse{
		ID:        "chatcmpl-" + runID,
		Object:    "chat.completion",
		Created:   nowUnix(),
		Model:     model,
		SessionID: sessionID,
		Choices: []Choice{
			{
				Index:        0,
				Message:      &Message{Role: "assistant", Content: buf.String()},
				FinishReason: finishStop(),
			},
		},
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

// formatToolUse builds a "[ToolName: summary]\n" string from a content block.
func formatToolUse(block runner.ContentBlock) string {
	summary := ""
	if len(block.Input) > 0 {
		var inputMap map[string]interface{}
		if json.Unmarshal(block.Input, &inputMap) == nil {
			for _, key := range []string{"file_path", "command", "pattern", "description"} {
				if v, ok := inputMap[key].(string); ok {
					summary = v
					break
				}
			}
		}
	}
	if summary != "" {
		return fmt.Sprintf("[%s: %s]\n", block.Name, summary)
	}
	return fmt.Sprintf("[%s]\n", block.Name)
}

// writeJSON writes a JSON response with the given status code.
func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
