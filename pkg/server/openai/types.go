package openai

import (
	"time"

	"rcodegen/pkg/runner"
)

// ---------------------------------------------------------------------------
// Request types
// ---------------------------------------------------------------------------

// ChatCompletionRequest represents an OpenAI-compatible chat completion request.
type ChatCompletionRequest struct {
	Model     string    `json:"model"`
	Messages  []Message `json:"messages"`
	Stream    bool      `json:"stream,omitempty"`
	WorkDirs  []string  `json:"work_dirs,omitempty"`
	SessionID string    `json:"session_id,omitempty"`
	// CloneWorkDirs runs the tool against throwaway copies of work_dirs so
	// concurrent runs never write state into the same source tree.
	CloneWorkDirs bool `json:"clone_work_dirs,omitempty"`
	// ReturnArtifacts returns the text files the run created or modified inside
	// its clone, which cleanup would otherwise destroy. Requires CloneWorkDirs:
	// without a clone there is no sandbox to diff.
	ReturnArtifacts bool `json:"return_artifacts,omitempty"`
	// CallbackURL switches the request to async callback mode: the server
	// validates it, answers 202 with a run_id, releases the connection, and
	// POSTs the completion here when the run ends. Mutually exclusive with
	// stream.
	CallbackURL string `json:"callback_url,omitempty"`
	// CallbackHeaders ride the callback POST verbatim — a bearer token for a
	// receiver that needs one. Values are never logged.
	CallbackHeaders map[string]string `json:"callback_headers,omitempty"`
}

// Message represents a single chat message with a role and content.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ---------------------------------------------------------------------------
// Response types
// ---------------------------------------------------------------------------

// ChatCompletionResponse represents an OpenAI-compatible chat completion response.
type ChatCompletionResponse struct {
	ID             string   `json:"id"`
	Object         string   `json:"object"`
	Created        int64    `json:"created"`
	Model          string   `json:"model"`
	Choices        []Choice `json:"choices"`
	Usage          *Usage   `json:"usage,omitempty"`
	SessionID      string   `json:"session_id,omitempty"`
	ClonedWorkDirs int      `json:"cloned_work_dirs,omitempty"`
	// CorrelationID echoes the request's X-Correlation-ID, mirroring bundle
	// runs, so a caller can tie a completion back to its own job.
	CorrelationID string `json:"correlation_id,omitempty"`
	// CostUSD is what the tool's CLI said this run cost. Absent when the CLI
	// reports no cost — never zero to mean "unknown".
	CostUSD float64 `json:"cost_usd,omitempty"`
	// UsageSource says where usage and cost came from: "cli" when the tool
	// reported them, "unreported" when it publishes none.
	UsageSource string `json:"usage_source,omitempty"`
	// Artifacts are the files the run wrote inside its work_dir clone, present
	// only when the request asked for them. ArtifactsSkipped names the ones
	// that were found but not returned, with the reason for each.
	Artifacts        []Artifact        `json:"artifacts,omitempty"`
	ArtifactsSkipped []ArtifactSkipped `json:"artifacts_skipped,omitempty"`
}

// Choice represents a single completion choice.
type Choice struct {
	Index        int      `json:"index"`
	Message      *Message `json:"message,omitempty"`
	FinishReason *string  `json:"finish_reason"`
}

// Usage represents token usage information.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Usage provenance values for the usage_source field.
const (
	// usageSourceCLI means the tool's CLI reported the numbers.
	usageSourceCLI = "cli"
	// usageSourceUnreported means it published none. Usage and cost are then
	// omitted entirely rather than sent as zeros.
	usageSourceUnreported = "unreported"
)

// runUsage asks the tool adapter what its CLI reported for this run and
// returns the completion fields to publish. Tools that do not implement
// runner.UsageReporter report nothing.
func runUsage(tool runner.Tool, res *runner.RunResult) (*Usage, float64, string) {
	reporter, ok := tool.(runner.UsageReporter)
	if !ok {
		return nil, 0, usageSourceUnreported
	}
	reported, ok := reporter.ReportedUsage(res)
	if !ok {
		return nil, 0, usageSourceUnreported
	}
	return &Usage{
		PromptTokens:     reported.InputTokens,
		CompletionTokens: reported.OutputTokens,
		TotalTokens:      reported.InputTokens + reported.OutputTokens,
	}, reported.CostUSD, usageSourceCLI
}

// ---------------------------------------------------------------------------
// Streaming types
// ---------------------------------------------------------------------------

// ChatCompletionChunk represents a single chunk in a streaming response.
type ChatCompletionChunk struct {
	ID             string         `json:"id"`
	Object         string         `json:"object"`
	Created        int64          `json:"created"`
	Model          string         `json:"model"`
	Choices        []StreamChoice `json:"choices"`
	Usage          *Usage         `json:"usage,omitempty"`
	SessionID      string         `json:"session_id,omitempty"`
	ClonedWorkDirs int            `json:"cloned_work_dirs,omitempty"`
	// CorrelationID rides the final chunk only, alongside session_id and
	// cloned_work_dirs.
	CorrelationID string `json:"correlation_id,omitempty"`
	// CostUSD and UsageSource ride the final chunk, same as the completion
	// object's fields of those names.
	CostUSD     float64 `json:"cost_usd,omitempty"`
	UsageSource string  `json:"usage_source,omitempty"`
	// Artifacts and ArtifactsSkipped ride the final chunk too: the run's files
	// exist only once it has finished, so there is no earlier chunk to put them
	// on.
	Artifacts        []Artifact        `json:"artifacts,omitempty"`
	ArtifactsSkipped []ArtifactSkipped `json:"artifacts_skipped,omitempty"`
}

// StreamChoice represents a single choice within a streaming chunk.
type StreamChoice struct {
	Index        int     `json:"index"`
	Delta        Delta   `json:"delta"`
	FinishReason *string `json:"finish_reason"`
}

// Delta represents the incremental content in a streaming choice.
type Delta struct {
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
}

// ---------------------------------------------------------------------------
// Async callback mode types
// ---------------------------------------------------------------------------

// Run lifecycle states, reported by the run endpoints and by the terminal
// status field of a callback payload.
const (
	runStatusQueued  = "queued"
	runStatusRunning = "running"
	runStatusSuccess = "success"
	runStatusFailure = "failure"
)

// AsyncSubmitResponse is the 202 answer to a chat completion carrying a
// callback_url: the run's identity, handed back before any work starts.
type AsyncSubmitResponse struct {
	RunID         string `json:"run_id"`
	Status        string `json:"status"`
	CorrelationID string `json:"correlation_id,omitempty"`
}

// AsyncCompletion is what a finished async run publishes — POSTed to the
// callback URL and retained for GET /v1/runs/{run_id}/result. It is the
// synchronous completion shape (inlined) plus the run's identity and outcome,
// so a caller can hand it to the same parser either way.
type AsyncCompletion struct {
	ChatCompletionResponse
	RunID  string `json:"run_id"`
	Status string `json:"status"` // success or failure
	// Error carries the same envelope detail a synchronous failure returns,
	// retryable included. Present only when status is "failure".
	Error *ErrorDetail `json:"error,omitempty"`
	// OutputTruncated marks a completion whose message content was cut to the
	// retention size cap. The result is never dropped silently instead.
	OutputTruncated bool `json:"output_truncated,omitempty"`
}

// RunSummary is the status view of one async run: GET /v1/runs/{run_id} and
// each entry of the correlation lookup. Timestamps are Unix seconds and are
// omitted until the run reaches that stage.
type RunSummary struct {
	RunID         string `json:"run_id"`
	Status        string `json:"status"`
	CorrelationID string `json:"correlation_id,omitempty"`
	CreatedAt     int64  `json:"created_at"`
	StartedAt     int64  `json:"started_at,omitempty"`
	FinishedAt    int64  `json:"finished_at,omitempty"`
	// QueueWaitMs is how long the run waited for a slot, mirroring the
	// synchronous path's X-Queue-Wait-Ms.
	QueueWaitMs int64 `json:"queue_wait_ms,omitempty"`
}

// RunList is the GET /v1/runs response body.
type RunList struct {
	Object string       `json:"object"`
	Data   []RunSummary `json:"data"`
}

// ---------------------------------------------------------------------------
// Models types
// ---------------------------------------------------------------------------

// ModelList represents the response from the /v1/models endpoint.
type ModelList struct {
	Object string      `json:"object"`
	Data   []ModelInfo `json:"data"`
}

// ModelInfo represents a single model entry.
type ModelInfo struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	OwnedBy string   `json:"owned_by"`
	Default bool     `json:"default,omitempty"` // true for a tool's default model
	Efforts []string `json:"efforts,omitempty"` // valid "-{effort}" suffixes for this entry
	Dynamic bool     `json:"dynamic,omitempty"` // true when arbitrary provider/model names are accepted
}

// ---------------------------------------------------------------------------
// Health types
// ---------------------------------------------------------------------------

// HealthResponse represents the server health check response.
type HealthResponse struct {
	Status  string `json:"status"`
	Version string `json:"version"`
	// ActiveRuns are running; Queued are waiting for a slot. A server that is
	// saturated rather than slow shows it here.
	ActiveRuns    int `json:"active_runs"`
	Queued        int `json:"queued"`
	MaxConcurrent int `json:"max_concurrent"`
	// Async admission: AsyncLive is how many submitted async runs the server
	// still holds — queued, running, or finishing — and AsyncBytes their
	// estimated retained request payload. Submissions past either ceiling are
	// refused with a retryable 503 rather than accepted, so these are the two
	// numbers that explain an async_capacity refusal.
	AsyncLive     int   `json:"async_live"`
	AsyncMaxLive  int   `json:"async_max_live"`
	AsyncBytes    int64 `json:"async_bytes"`
	AsyncMaxBytes int64 `json:"async_max_bytes"`
}

// Queue progress events, written to a streaming response when — and only
// when — the request had to wait for a run slot.
type queueEvent struct {
	Type string `json:"type"`
	// Position is the waiter's place in line, counting from 1. Set on the
	// "queued" event only.
	Position int `json:"position,omitempty"`
}

// ---------------------------------------------------------------------------
// File types
// ---------------------------------------------------------------------------

// FileObject represents an uploaded file (OpenAI Files API compatible).
type FileObject struct {
	ID        string `json:"id"`
	Object    string `json:"object"`
	Bytes     int64  `json:"bytes"`
	CreatedAt int64  `json:"created_at"`
	Filename  string `json:"filename"`
	Purpose   string `json:"purpose"`
	Path      string `json:"path"` // local disk path for reference in prompts
}

// FileList represents the response from GET /v1/files.
type FileList struct {
	Object string       `json:"object"`
	Data   []FileObject `json:"data"`
}

// FileDeleteResponse represents the response from DELETE /v1/files/{id}.
type FileDeleteResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Deleted bool   `json:"deleted"`
}

// ---------------------------------------------------------------------------
// Error types
// ---------------------------------------------------------------------------

// ErrorResponse represents an OpenAI-compatible error response.
type ErrorResponse struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail contains the error information. Retryable is derived from Code
// by the classification in errorcodes.go, never set by hand at a call site.
type ErrorDetail struct {
	Message   string    `json:"message"`
	Type      string    `json:"type"`
	Code      ErrorCode `json:"code"`
	Retryable bool      `json:"retryable"`
}

// NewErrorResponse constructs an ErrorResponse with the given details.
func NewErrorResponse(msg, errType string, code ErrorCode) ErrorResponse {
	return ErrorResponse{
		Error: ErrorDetail{
			Message:   msg,
			Type:      errType,
			Code:      code,
			Retryable: retryableForCode(code),
		},
	}
}

// ---------------------------------------------------------------------------
// Helper functions
// ---------------------------------------------------------------------------

// finishStop returns a pointer to the string "stop".
func finishStop() *string {
	s := "stop"
	return &s
}

// nowUnix returns the current Unix timestamp.
func nowUnix() int64 {
	return time.Now().Unix()
}
