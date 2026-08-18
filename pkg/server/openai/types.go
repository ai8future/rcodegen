package openai

import "time"

// ---------------------------------------------------------------------------
// Request types
// ---------------------------------------------------------------------------

// ChatCompletionRequest represents an OpenAI-compatible chat completion request.
type ChatCompletionRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Stream    bool      `json:"stream,omitempty"`
	WorkDirs  []string  `json:"work_dirs,omitempty"`
	SessionID string    `json:"session_id,omitempty"`
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
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices   []Choice `json:"choices"`
	Usage     *Usage   `json:"usage,omitempty"`
	SessionID string   `json:"session_id,omitempty"`
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

// ---------------------------------------------------------------------------
// Streaming types
// ---------------------------------------------------------------------------

// ChatCompletionChunk represents a single chunk in a streaming response.
type ChatCompletionChunk struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Created int64          `json:"created"`
	Model   string         `json:"model"`
	Choices   []StreamChoice `json:"choices"`
	Usage     *Usage         `json:"usage,omitempty"`
	SessionID string         `json:"session_id,omitempty"`
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
	Efforts []string `json:"efforts,omitempty"` // bare tool entries: valid "-{effort}" suffixes
}

// ---------------------------------------------------------------------------
// Health types
// ---------------------------------------------------------------------------

// HealthResponse represents the server health check response.
type HealthResponse struct {
	Status        string `json:"status"`
	Version       string `json:"version"`
	ActiveRuns    int    `json:"active_runs"`
	MaxConcurrent int    `json:"max_concurrent"`
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

// ErrorDetail contains the error information.
type ErrorDetail struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code"`
}

// NewErrorResponse constructs an ErrorResponse with the given details.
func NewErrorResponse(msg, errType, code string) ErrorResponse {
	return ErrorResponse{
		Error: ErrorDetail{
			Message: msg,
			Type:    errType,
			Code:    code,
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
