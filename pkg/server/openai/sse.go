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

// NewSSEWriter creates a new SSEWriter. If w implements http.Flusher, it is
// stored so that each write can be flushed immediately.
func NewSSEWriter(w http.ResponseWriter) *SSEWriter {
	s := &SSEWriter{w: w}
	if f, ok := w.(http.Flusher); ok {
		s.flusher = f
	}
	return s
}

// SetHeaders sets the standard SSE response headers.
func (s *SSEWriter) SetHeaders() {
	h := s.w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
}

// WriteChunk marshals the chunk to JSON and writes it as a single SSE data
// frame: "data: {json}\n\n".
func (s *SSEWriter) WriteChunk(chunk ChatCompletionChunk) error {
	return s.WriteEvent(chunk)
}

// WriteEvent writes any value as one SSE data frame. Completion chunks are the
// usual payload; the stream also carries out-of-band events (queue progress)
// that are not chunks.
func (s *SSEWriter) WriteEvent(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("sse: marshal event: %w", err)
	}
	if _, err := fmt.Fprintf(s.w, "data: %s\n\n", data); err != nil {
		return fmt.Errorf("sse: write event: %w", err)
	}
	if s.flusher != nil {
		s.flusher.Flush()
	}
	return nil
}

// WriteDone writes the SSE termination frame: "data: [DONE]\n\n".
func (s *SSEWriter) WriteDone() {
	fmt.Fprint(s.w, "data: [DONE]\n\n")
	if s.flusher != nil {
		s.flusher.Flush()
	}
}
