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
	"strconv"
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
	// async holds the runs that outlive their request: their lifecycle
	// contexts, their retained results, and the callback dispatcher.
	async *asyncRuns
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// HandlerOption carries deployment configuration a handler cannot discover for
// itself. Parsing belongs at startup, where a bad value can still stop the
// process; by the time a request is being served it is too late to reject one.
type HandlerOption func(*handlerConfig)

// handlerConfig is the resolved configuration NewHandler builds from.
type handlerConfig struct {
	asyncLimits AsyncLimits
}

// WithAsyncLimits sets the admission bounds for async callback mode. Without
// it a handler uses DefaultAsyncLimits for its registry's slot count.
func WithAsyncLimits(limits AsyncLimits) HandlerOption {
	return func(c *handlerConfig) { c.asyncLimits = limits }
}

// NewHandler creates a new Handler and registers routes on its internal mux.
// If fileStore is non-nil, file upload/download endpoints are enabled.
func NewHandler(s *settings.Settings, toolFactories map[string]server.ToolFactory, registry *server.RunRegistry, availableTools []string, fileStore *FileStore, sessions *server.SessionStore, opts ...HandlerOption) *Handler {
	cfg := handlerConfig{asyncLimits: DefaultAsyncLimits(registry.MaxConcurrent())}
	for _, opt := range opts {
		opt(&cfg)
	}
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
		async:          newAsyncRuns(cfg.asyncLimits),
	}
	h.mux.HandleFunc("/v1/chat/completions", h.handleChatCompletions)
	h.mux.HandleFunc("/v1/models", h.handleModels)
	h.mux.HandleFunc("/v1/bundles", h.handleBundles)
	h.mux.HandleFunc("/v1/bundles/", h.handleBundleByName)
	h.mux.HandleFunc("/v1/runs", h.handleRuns)
	h.mux.HandleFunc("/v1/runs/", h.handleRunByID)
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
	writeJSON(w, http.StatusOK, BuildModelList(r.Context(), h.availableTools, h.toolFactories, h.settings))
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

// handleHealth returns server health information, including how much of the
// async admission budget is spoken for. A caller that starts seeing retryable
// 503s can tell here whether the server is genuinely full or whether its limits
// were configured too low for the workload.
func (h *Handler) handleHealth(w http.ResponseWriter, r *http.Request) {
	async := h.async.stats()
	writeJSON(w, http.StatusOK, HealthResponse{
		Status:        "ok",
		Version:       ToolVersion(),
		ActiveRuns:    h.registry.ActiveCount(),
		Queued:        h.registry.QueuedCount(),
		MaxConcurrent: h.registry.MaxConcurrent(),
		AsyncLive:     async.live,
		AsyncMaxLive:  async.maxLive,
		AsyncBytes:    async.bytes,
		AsyncMaxBytes: async.maxBytes,
	})
}

// handleChatCompletions is the main handler for chat completion requests. A
// request is validated in full before either path starts, so a bad one is
// refused on this connection whether it asked for a callback or not.
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

	plan, status, errResp := h.planChatCompletion(r, &req)
	if errResp != nil {
		writeJSON(w, status, *errResp)
		return
	}
	if plan.callback != nil {
		h.submitAsyncRun(w, plan)
		return
	}
	h.runChatSync(w, r, plan)
}

// chatPlan is a chat completion request that has passed every check and is
// ready to run — on this connection, or detached from it behind a callback.
type chatPlan struct {
	tool            runner.Tool
	cfg             *runner.Config
	toolName        string
	model           string // the model string the caller sent, echoed back
	correlationID   string
	workDirs        []string
	cloneRequested  bool
	returnArtifacts bool
	stream          bool
	showToolUse     bool
	// callback is non-nil when the caller asked for async delivery.
	callback *callbackTarget
}

// planChatCompletion validates a decoded request and builds the run it
// describes. On rejection it returns the HTTP status and envelope to send and a
// nil plan; nothing has been acquired or created either way.
func (h *Handler) planChatCompletion(r *http.Request, req *ChatCompletionRequest) (*chatPlan, int, *ErrorResponse) {
	// Async delivery and streaming are two answers to the same question, and a
	// request that asks for both has no defined outcome. Refuse it before any
	// of the work below.
	var callback *callbackTarget
	if req.CallbackURL != "" {
		if req.Stream {
			return rejected(http.StatusBadRequest, NewErrorResponse(
				"callback_url cannot be combined with stream: a callback delivers the completion once, "+
					"a stream delivers it incrementally — pick one",
				"invalid_request_error", codeCallbackStreamConflict,
			))
		}
		// The request's own context bounds the plaintext hostname lookup, so a
		// caller that hangs up during validation stops paying for it.
		cb, err := newCallbackTarget(r.Context(), req.CallbackURL, req.CallbackHeaders)
		if err != nil {
			return rejected(http.StatusBadRequest, NewErrorResponse(
				err.Error(), "invalid_request_error", callbackErrorCode(err),
			))
		}
		callback = cb
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
			return rejected(http.StatusBadRequest, NewErrorResponse(
				fmt.Sprintf("unknown tool: %s", toolName), "invalid_request_error", codeUnknownTool,
			))
		}
	}

	task := ExtractTaskPrompt(req.Messages)
	if task == "" {
		return rejected(http.StatusBadRequest, NewErrorResponse(
			"no user message found", "invalid_request_error", codeEmptyTask,
		))
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
	if req.ReasoningEffort != "" {
		if requestEffort != "" && requestEffort != req.ReasoningEffort {
			return rejected(http.StatusBadRequest, NewErrorResponse(
				fmt.Sprintf("conflicting reasoning efforts %q and %q", requestEffort, req.ReasoningEffort),
				"invalid_request_error", codeInvalidEffort,
			))
		}
		requestEffort = req.ReasoningEffort
	}

	// Reject unknown models up front with the valid list — a bad model passed
	// through to the CLI fails silently (200 with empty content). GET
	// /v1/models enumerates every valid tool:model combination and each
	// tool's valid effort suffixes.
	if modelOverride != "" {
		if err := runner.ValidateModel(tool, modelOverride); err != nil {
			return rejected(http.StatusBadRequest, NewErrorResponse(
				err.Error(), "invalid_request_error", codeInvalidModel,
			))
		}
	}

	// Build config
	cfg := runner.NewConfig()
	cfg.Task = task
	cfg.Output = io.Discard
	cfg.Logger = logz.New("warn")
	cfg.Stderr = runner.NewBoundedBuffer(64 << 10)
	if toolName == "ollama" || toolName == "lmstudio" {
		cfg.Messages = make([]runner.ChatMessage, len(req.Messages))
		for i, message := range req.Messages {
			cfg.Messages[i] = runner.ChatMessage{Role: message.Role, Content: message.Content}
		}
	}
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
		return rejected(http.StatusBadRequest, NewErrorResponse(
			err.Error(), "invalid_request_error", codeInvalidModel,
		))
	}
	if err := runner.ValidateEffort(tool, cfg.Model, cfg.Effort); err != nil {
		return rejected(http.StatusBadRequest, NewErrorResponse(
			err.Error(), "invalid_request_error", codeInvalidEffort,
		))
	}
	if err := tool.ValidateConfig(cfg); err != nil {
		if cfg.Model == "" {
			return rejected(http.StatusBadRequest, NewErrorResponse(
				err.Error(), "invalid_request_error", codeInvalidModel,
			))
		}
		return rejected(http.StatusBadRequest, NewErrorResponse(
			err.Error(), "invalid_request_error", codeInvalidJSON,
		))
	}

	// Validate clone sources before taking a run slot. Acquire blocks until one
	// frees up, so checking afterwards makes an unusable work_dir queue behind
	// real work and burn a slot just to return its 400. An async submission has
	// the same reason and one more: after the 202 there is no connection left to
	// report a bad work_dir on.
	cloneRequested := req.CloneWorkDirs && len(req.WorkDirs) > 0

	// Artifacts are the diff of a clone against the manifest taken when it was
	// made. Without a clone there is no manifest, no sandbox, and no boundary
	// that makes returning the caller's own files defensible — so the request
	// is refused rather than answered with an empty list, which would read as
	// "the agent wrote nothing".
	if req.ReturnArtifacts && !cloneRequested {
		return rejected(http.StatusBadRequest, NewErrorResponse(
			"return_artifacts requires clone_work_dirs: true and at least one work_dir; "+
				"artifacts are the files a run wrote inside its own clone, and an uncloned run "+
				"writes into the caller's tree, where they already are",
			"invalid_request_error", codeArtifactsRequireClone,
		))
	}

	if cloneRequested {
		if _, err := checkWorkDirSources(req.WorkDirs); err != nil {
			return rejected(http.StatusBadRequest, NewErrorResponse(
				err.Error(), "invalid_request_error", workDirErrorCode(err),
			))
		}
	}

	return &chatPlan{
		tool:            tool,
		cfg:             cfg,
		toolName:        toolName,
		model:           req.Model, // echo back the original model string
		correlationID:   correlationID(r),
		workDirs:        req.WorkDirs,
		cloneRequested:  cloneRequested,
		returnArtifacts: req.ReturnArtifacts,
		stream:          req.Stream,
		showToolUse:     showToolUse,
		callback:        callback,
	}, 0, nil
}

// rejected packages a validation failure for planChatCompletion's caller.
func rejected(status int, resp ErrorResponse) (*chatPlan, int, *ErrorResponse) {
	return nil, status, &resp
}

// runChatSync executes a validated plan on the caller's own connection: the
// original behaviour, where the run lives and dies with the request.
func (h *Handler) runChatSync(w http.ResponseWriter, r *http.Request, plan *chatPlan) {
	tool, cfg, corrID := plan.tool, plan.cfg, plan.correlationID

	// A request that waits for a busy slot looks exactly like a slow one from
	// outside. Streaming callers are told as it happens; non-streaming callers
	// get the total afterwards in X-Queue-Wait-Ms. The stream is opened only
	// if a wait actually occurs, so an unqueued request is byte-for-byte what
	// it was before.
	var sse *SSEWriter
	acquireOpts := server.AcquireOptions{CorrelationID: corrID}
	if plan.stream {
		acquireOpts.OnQueued = func(position int) {
			sse = NewSSEWriter(w)
			sse.SetHeaders()
			_ = sse.WriteEvent(queueEvent{Type: "queued", Position: position})
		}
	}

	run, err := h.registry.AcquireWith(r.Context(), plan.toolName, cfg.Task, acquireOpts)
	if err != nil {
		h.writeChatError(w, sse, http.StatusServiceUnavailable, NewErrorResponse(
			"failed to acquire run slot: "+err.Error(), "server_error", codeConcurrencyLimit,
		))
		return
	}
	if sse != nil {
		_ = sse.WriteEvent(queueEvent{Type: "started"})
	}
	if !plan.stream {
		if ms := run.QueueWait.Milliseconds(); ms > 0 {
			w.Header().Set("X-Queue-Wait-Ms", strconv.FormatInt(ms, 10))
		}
	}
	runID, runCtx, cancel := run.RunID, run.Ctx, run.Cancel
	defer cancel()
	defer h.registry.Release(runID)

	// Clone work_dirs into per-run scratch copies when asked. The cleanup defer
	// sits in the same teardown stack as cancel/Release, so a client disconnect
	// removes the scratch root too.
	var clone *workDirClone
	var artifacts *artifactCollector
	if plan.cloneRequested {
		cloneLogger := logz.New("info")
		clone, err = cloneWorkDirs(runCtx, runID, plan.workDirs, cloneLogger)
		if err != nil {
			// These sources passed validation before the slot was acquired, so a
			// failure here is a source that changed underneath the wait or a copy
			// that broke — a server-side failure, not a bad request.
			h.writeChatError(w, sse, http.StatusInternalServerError, NewErrorResponse(
				err.Error(), "server_error", codeCloneFailed,
			))
			return
		}
		defer clone.cleanup(cloneLogger)
		cfg.WorkDirs = clone.dirs
		if plan.returnArtifacts {
			// The manifest is taken here — after the clone, before the CLI — so
			// the diff shows this run's writes and nothing the source already
			// held. close runs ahead of cleanup, both before the scratch root
			// goes away.
			artifacts = newArtifactCollector(clone, cloneLogger)
			defer artifacts.close()
		}
	}

	meta := completionMeta{
		runID:          runID,
		model:          plan.model,
		toolName:       plan.toolName,
		correlationID:  corrID,
		clonedWorkDirs: clone.count(),
		artifacts:      artifacts,
	}

	if plan.stream {
		h.handleStreaming(w, runCtx, tool, cfg, meta, plan.showToolUse, cancel, sse)
	} else {
		resp, result := h.completeNonStreaming(runCtx, tool, cfg, meta)
		if result.ExitCode != 0 || result.Error != nil {
			writeJSON(w, http.StatusBadGateway, executionErrorResponse(cfg, result))
			return
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

// writeChatError reports a chat completion failure. Once queue events have
// gone out the status line is long gone, so the envelope rides the event
// stream instead of a JSON body.
func (h *Handler) writeChatError(w http.ResponseWriter, sse *SSEWriter, status int, resp ErrorResponse) {
	if sse == nil {
		writeJSON(w, status, resp)
		return
	}
	_ = sse.WriteEvent(resp)
	sse.WriteDone()
}

// completionMeta carries the per-run values that ride on a completion response
// beside the model's own output.
type completionMeta struct {
	runID          string
	model          string // the model string the caller sent
	toolName       string
	correlationID  string
	clonedWorkDirs int
	// artifacts collects what the run wrote inside its clone, once the run is
	// over and while the clone still exists. Nil unless the caller asked for
	// artifacts; a nil collector answers nothing.
	artifacts *artifactCollector
}

// handleStreaming handles a streaming chat completion request. sse is non-nil
// when the stream is already open because the request reported a queue wait on
// it; otherwise the stream starts here.
func (h *Handler) handleStreaming(w http.ResponseWriter, ctx context.Context, tool runner.Tool, cfg *runner.Config, meta completionMeta, showToolUse bool, cancel context.CancelFunc, sse *SSEWriter) {
	runID, model := meta.runID, meta.model
	if sse == nil {
		sse = NewSSEWriter(w)
		sse.SetHeaders()
	}

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
	if result.ExitCode != 0 || result.Error != nil {
		mu.Lock()
		_ = sse.WriteEvent(executionErrorResponse(cfg, result))
		mu.Unlock()
		sse.WriteDone()
		return
	}

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
	finalChunk.Usage, finalChunk.CostUSD, finalChunk.UsageSource = runUsage(tool, result)
	finalChunk.Artifacts, finalChunk.ArtifactsSkipped = meta.artifacts.collect()
	mu.Lock()
	_ = sse.WriteChunk(finalChunk)
	mu.Unlock()

	sse.WriteDone()
}

// completeNonStreaming runs the tool to completion and builds the response
// object. It writes nothing: the synchronous path sends the result on the
// caller's connection, the async path POSTs it to a callback and retains it.
func (h *Handler) completeNonStreaming(ctx context.Context, tool runner.Tool, cfg *runner.Config, meta completionMeta) (ChatCompletionResponse, *runner.RunResult) {
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
	resp.Usage, resp.CostUSD, resp.UsageSource = runUsage(tool, result)
	// Collected here, with the run over and the clone still standing: a run that
	// failed is exactly the one whose half-written files are worth reading.
	resp.Artifacts, resp.ArtifactsSkipped = meta.artifacts.collect()

	return resp, result
}

func executionErrorResponse(cfg *runner.Config, result *runner.RunResult) ErrorResponse {
	message := "tool execution failed"
	if cfg != nil && cfg.Stderr != nil {
		if text, ok := cfg.Stderr.(interface{ String() string }); ok {
			if detail := strings.TrimSpace(text.String()); detail != "" {
				message += ": " + detail
			}
		}
	}
	if result != nil && result.Error != nil && message == "tool execution failed" {
		message += ": " + result.Error.Error()
	}
	return NewErrorResponse(message, "server_error", codeToolExecutionFailed)
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
