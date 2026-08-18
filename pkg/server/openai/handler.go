package openai

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
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
			"method not allowed", "invalid_request_error", codeMethodNotAllowed,
		))
	}
}

// ServeHTTP echoes the caller's correlation ID, enforces optional bearer-token
// auth, then delegates to the internal mux. Auth is enabled by setting the
// RSERVE_TOKEN environment variable; /health stays open so monitoring keeps
// working.
//
// The echo happens here, before routing, so every response on every endpoint
// carries X-Correlation-ID back — including the errors a caller most wants to
// tie to its own job.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if corrID := correlationID(r); corrID != "" {
		w.Header().Set("X-Correlation-ID", corrID)
	}
	if h.authToken != "" && r.URL.Path != "/health" {
		want := "Bearer " + h.authToken
		got := r.Header.Get("Authorization")
		if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
			writeJSON(w, http.StatusUnauthorized, NewErrorResponse(
				"invalid or missing bearer token", "invalid_request_error", codeUnauthorized,
			))
			return
		}
	}
	h.mux.ServeHTTP(w, r)
}

// correlationID returns the request's sanitized X-Correlation-ID, empty when
// the caller sent none.
func correlationID(r *http.Request) string {
	return sanitizeCorrelationID(r.Header.Get("X-Correlation-ID"))
}

// handleModels returns the list of available tools and every valid
// tool:model combination.
func (h *Handler) handleModels(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, BuildModelList(h.availableTools, h.toolFactories, h.settings))
}

// splitToolEffort resolves "claude-max" style names where an effort suffix
// rides on a bare tool name. Returns the tool, effort, and whether it matched.
func (h *Handler) splitToolEffort(name string) (tool, effort string, ok bool) {
	for tn, factory := range h.toolFactories {
		prefix := tn + "-"
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		rest := name[len(prefix):]
		instance := factory()
		applyToolSettings(instance, h.settings)
		for _, e := range runner.EffortsForModel(instance, instance.DefaultModelSetting()) {
			if rest == e {
				return tn, e, true
			}
		}
	}
	return "", "", false
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
			"method not allowed", "invalid_request_error", codeMethodNotAllowed,
		))
		return
	}

	// Limit request body to 10MB to prevent memory exhaustion from oversized payloads.
	const maxBodySize = 10 << 20 // 10 MB
	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)

	var req ChatCompletionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, NewErrorResponse(
			"invalid JSON: "+err.Error(), "invalid_request_error", codeInvalidJSON,
		))
		return
	}

	toolName, modelOverride := ParseModel(req.Model)

	// "claude-max" style: an effort suffix riding on the bare tool name.
	requestEffort := ""
	factory, ok := h.toolFactories[toolName]
	if !ok {
		if baseTool, e, found := h.splitToolEffort(toolName); found {
			toolName, requestEffort = baseTool, e
			factory = h.toolFactories[toolName]
		} else {
			writeJSON(w, http.StatusBadRequest, NewErrorResponse(
				fmt.Sprintf("unknown tool: %s", toolName), "invalid_request_error", codeUnknownTool,
			))
			return
		}
	}

	task := ExtractTaskPrompt(req.Messages)
	if task == "" {
		writeJSON(w, http.StatusBadRequest, NewErrorResponse(
			"no user message found", "invalid_request_error", codeEmptyTask,
		))
		return
	}

	showToolUse := r.Header.Get("X-Show-Tool-Use") == "true"

	tool := factory()
	applyToolSettings(tool, h.settings)

	// "claude:opus-max" style: an effort suffix on the model name. Split before
	// validation so the base model is checked, not the suffixed string.
	if modelOverride != "" {
		base, e := runner.SplitModelEffort(tool, modelOverride)
		modelOverride = base
		if e != "" {
			requestEffort = e
		}
	}

	// Reject unknown models up front with the valid list — a bad model passed
	// through to the CLI fails silently (200 with empty content). GET
	// /v1/models enumerates every valid tool:model combination and each
	// tool's valid effort suffixes.
	if modelOverride != "" {
		if err := runner.ValidateModel(tool, modelOverride); err != nil {
			writeJSON(w, http.StatusBadRequest, NewErrorResponse(
				err.Error(), "invalid_request_error", codeInvalidModel,
			))
			return
		}
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
	// Effort from the request's suffix overrides the settings default.
	if requestEffort != "" {
		cfg.Effort = requestEffort
	}
	if err := runner.ValidateModel(tool, cfg.Model); err != nil {
		writeJSON(w, http.StatusBadRequest, NewErrorResponse(
			err.Error(), "invalid_request_error", codeInvalidModel,
		))
		return
	}
	if err := runner.ValidateEffort(tool, cfg.Model, cfg.Effort); err != nil {
		writeJSON(w, http.StatusBadRequest, NewErrorResponse(
			err.Error(), "invalid_request_error", codeInvalidEffort,
		))
		return
	}

	// Validate clone sources before taking a run slot. Acquire blocks until one
	// frees up, so checking afterwards makes an unusable work_dir queue behind
	// real work and burn a slot just to return its 400.
	cloneRequested := req.CloneWorkDirs && len(req.WorkDirs) > 0
	if cloneRequested {
		if _, err := checkWorkDirSources(req.WorkDirs); err != nil {
			writeJSON(w, http.StatusBadRequest, NewErrorResponse(
				err.Error(), "invalid_request_error", workDirErrorCode(err),
			))
			return
		}
	}

	corrID := correlationID(r)
	run, err := h.registry.AcquireWith(r.Context(), toolName, task, server.AcquireOptions{
		CorrelationID: corrID,
	})
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, NewErrorResponse(
			"failed to acquire run slot: "+err.Error(), "server_error", codeConcurrencyLimit,
		))
		return
	}
	runID, runCtx, cancel := run.RunID, run.Ctx, run.Cancel
	defer cancel()
	defer h.registry.Release(runID)

	// Clone work_dirs into per-run scratch copies when asked. The cleanup defer
	// sits in the same teardown stack as cancel/Release, so a client disconnect
	// removes the scratch root too.
	var clone *workDirClone
	if cloneRequested {
		cloneLogger := logz.New("info")
		clone, err = cloneWorkDirs(runCtx, runID, req.WorkDirs, cloneLogger)
		if err != nil {
			// These sources passed validation before the slot was acquired, so a
			// failure here is a source that changed underneath the wait or a copy
			// that broke — a server-side failure, not a bad request.
			writeJSON(w, http.StatusInternalServerError, NewErrorResponse(
				err.Error(), "server_error", codeCloneFailed,
			))
			return
		}
		defer clone.cleanup(cloneLogger)
		cfg.WorkDirs = clone.dirs
	}

	meta := completionMeta{
		runID:          runID,
		model:          req.Model, // echo back the original model string
		toolName:       toolName,
		correlationID:  corrID,
		clonedWorkDirs: clone.count(),
	}

	if req.Stream {
		h.handleStreaming(w, runCtx, tool, cfg, meta, showToolUse, cancel)
	} else {
		h.handleNonStreaming(w, runCtx, tool, cfg, meta)
	}
}

// completionMeta carries the per-run values that ride on a completion response
// beside the model's own output.
type completionMeta struct {
	runID          string
	model          string // the model string the caller sent
	toolName       string
	correlationID  string
	clonedWorkDirs int
}

// handleStreaming handles a streaming chat completion request.
func (h *Handler) handleStreaming(w http.ResponseWriter, ctx context.Context, tool runner.Tool, cfg *runner.Config, meta completionMeta, showToolUse bool, cancel context.CancelFunc) {
	runID, model := meta.runID, meta.model
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
		h.sessions.Store(sessionID, meta.toolName, result.SessionID)
	}

	// Send final chunk with finish_reason and optional usage
	finalChunk := ChatCompletionChunk{
		ID:             "chatcmpl-" + runID,
		Object:         "chat.completion.chunk",
		Created:        nowUnix(),
		Model:          model,
		SessionID:      sessionID,
		ClonedWorkDirs: meta.clonedWorkDirs,
		CorrelationID:  meta.correlationID,
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
func (h *Handler) handleNonStreaming(w http.ResponseWriter, ctx context.Context, tool runner.Tool, cfg *runner.Config, meta completionMeta) {
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
		sessionID = meta.runID
		h.sessions.Store(sessionID, meta.toolName, result.SessionID)
	}

	resp := ChatCompletionResponse{
		ID:             "chatcmpl-" + meta.runID,
		Object:         "chat.completion",
		Created:        nowUnix(),
		Model:          meta.model,
		SessionID:      sessionID,
		ClonedWorkDirs: meta.clonedWorkDirs,
		CorrelationID:  meta.correlationID,
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

// workDirErrorCode maps a work_dirs validation failure to its API error code.
func workDirErrorCode(err error) ErrorCode {
	switch {
	case errors.Is(err, errUnsafeSymlink):
		return codeUnsafeSymlink
	case errors.Is(err, errGitWorktree):
		return codeUnsupportedGitWorktree
	default:
		return codeInvalidWorkDir
	}
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
