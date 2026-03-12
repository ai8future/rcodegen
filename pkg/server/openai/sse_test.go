package openai

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSSEWriter_WriteChunk(t *testing.T) {
	rec := httptest.NewRecorder()
	sw := NewSSEWriter(rec)

	chunk := ChatCompletionChunk{
		ID:     "chatcmpl-1",
		Object: "chat.completion.chunk",
		Choices: []StreamChoice{
			{
				Index: 0,
				Delta: Delta{Content: "hello"},
			},
		},
	}

	if err := sw.WriteChunk(chunk); err != nil {
		t.Fatalf("WriteChunk returned error: %v", err)
	}

	body := rec.Body.String()

	if !strings.HasPrefix(body, "data: ") {
		t.Errorf("body should start with 'data: ', got: %q", body)
	}
	if !strings.Contains(body, `"hello"`) {
		t.Errorf("body should contain '\"hello\"', got: %q", body)
	}
	if !strings.HasSuffix(body, "\n\n") {
		t.Errorf("body should end with '\\n\\n', got: %q", body)
	}
}

func TestSSEWriter_WriteDone(t *testing.T) {
	rec := httptest.NewRecorder()
	sw := NewSSEWriter(rec)

	sw.WriteDone()

	body := rec.Body.String()
	expected := "data: [DONE]\n\n"
	if body != expected {
		t.Errorf("expected %q, got %q", expected, body)
	}
}
