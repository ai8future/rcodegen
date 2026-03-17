package openai

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"rcodegen/pkg/server"
)

func TestHandleModels(t *testing.T) {
	h := NewHandler(nil, nil, server.NewRunRegistry(5), []string{"claude", "gemini"}, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var ml ModelList
	if err := json.NewDecoder(rec.Body).Decode(&ml); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if ml.Object != "list" {
		t.Errorf("expected Object='list', got %q", ml.Object)
	}
	if len(ml.Data) != 2 {
		t.Errorf("expected 2 models, got %d", len(ml.Data))
	}
}

func TestHandleHealth(t *testing.T) {
	reg := server.NewRunRegistry(5)
	h := NewHandler(nil, nil, reg, nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var hr HealthResponse
	if err := json.NewDecoder(rec.Body).Decode(&hr); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if hr.Status != "ok" {
		t.Errorf("expected Status='ok', got %q", hr.Status)
	}
	if hr.MaxConcurrent != 5 {
		t.Errorf("expected MaxConcurrent=5, got %d", hr.MaxConcurrent)
	}
}

func TestHandleChatCompletions_BadMethod(t *testing.T) {
	h := NewHandler(nil, nil, server.NewRunRegistry(5), nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/chat/completions", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}

func TestHandleChatCompletions_UnknownTool(t *testing.T) {
	h := NewHandler(nil, map[string]server.ToolFactory{}, server.NewRunRegistry(5), nil, nil, nil)

	body := `{"model":"unknown","messages":[{"role":"user","content":"hello"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}
