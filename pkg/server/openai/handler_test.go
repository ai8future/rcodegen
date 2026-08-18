package openai

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rcodegen/pkg/runner"
	"rcodegen/pkg/server"
	"rcodegen/pkg/tools/opencode"

	chassis "github.com/ai8future/chassis-go/v11"
)

func installFakeOpenCode(t *testing.T, output string) {
	t.Helper()
	binDir := t.TempDir()
	script := filepath.Join(binDir, "opencode")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s' '"+output+"'\n"), 0o755); err != nil {
		t.Fatalf("write fake opencode: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

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

func TestHandleChatCompletions_NonStreamToolReturnsStdout(t *testing.T) {
	chassis.RequireMajor(11)
	installFakeOpenCode(t, "plain CLI output")
	h := NewHandler(nil, map[string]server.ToolFactory{
		"opencode": func() runner.Tool { return opencode.New() },
	}, server.NewRunRegistry(1), []string{"opencode"}, nil, nil)

	body := `{"model":"opencode","messages":[{"role":"user","content":"hello"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp ChatCompletionResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got := resp.Choices[0].Message.Content; got != "plain CLI output" {
		t.Fatalf("content = %q, want plain CLI output", got)
	}
}

func TestHandleChatCompletions_DynamicModelOverrideAccepted(t *testing.T) {
	chassis.RequireMajor(11)
	installFakeOpenCode(t, "dynamic model output")
	h := NewHandler(nil, map[string]server.ToolFactory{
		"opencode": func() runner.Tool { return opencode.New() },
	}, server.NewRunRegistry(1), []string{"opencode"}, nil, nil)

	body := `{"model":"opencode:custom/provider-model","messages":[{"role":"user","content":"hello"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp ChatCompletionResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got := resp.Choices[0].Message.Content; got != "dynamic model output" {
		t.Fatalf("content = %q, want dynamic model output", got)
	}
}

func TestHandleChatCompletions_NonStreamToolWritesSSE(t *testing.T) {
	chassis.RequireMajor(11)
	installFakeOpenCode(t, "streamed CLI output")
	h := NewHandler(nil, map[string]server.ToolFactory{
		"opencode": func() runner.Tool { return opencode.New() },
	}, server.NewRunRegistry(1), []string{"opencode"}, nil, nil)

	body := `{"model":"opencode","messages":[{"role":"user","content":"hello"}],"stream":true}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "streamed CLI output") {
		t.Fatalf("SSE body does not contain CLI output: %s", rec.Body.String())
	}
}
