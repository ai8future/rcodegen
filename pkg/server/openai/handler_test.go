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

// installFakeOpenCodeEchoingArgs installs a fake CLI that reports the argv it
// received, so a test can see which directory the run was pointed at.
func installFakeOpenCodeEchoingArgs(t *testing.T) {
	t.Helper()
	binDir := t.TempDir()
	script := filepath.Join(binDir, "opencode")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s' \"$*\"\n"), 0o755); err != nil {
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

func TestHandleChatCompletions_CloneWorkDirsRunsAgainstScratchCopy(t *testing.T) {
	chassis.RequireMajor(11)
	installFakeOpenCodeEchoingArgs(t)
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, ".env"), []byte("SECRET=1"), 0o600); err != nil {
		t.Fatalf("write .env: %v", err)
	}
	h := NewHandler(nil, map[string]server.ToolFactory{
		"opencode": func() runner.Tool { return opencode.New() },
	}, server.NewRunRegistry(1), []string{"opencode"}, nil, nil)

	body := `{"model":"opencode","messages":[{"role":"user","content":"hello"}],` +
		`"work_dirs":["` + src + `"],"clone_work_dirs":true}`
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
	if resp.ClonedWorkDirs != 1 {
		t.Errorf("cloned_work_dirs = %d, want 1", resp.ClonedWorkDirs)
	}

	// The CLI was pointed at the scratch copy, not the caller's tree.
	args := resp.Choices[0].Message.Content
	if strings.Contains(args, src) {
		t.Errorf("CLI ran against the source directory: %s", args)
	}
	clonePath := extractClonePath(t, args)
	if _, err := os.Stat(clonePath); !os.IsNotExist(err) {
		t.Errorf("scratch root survived the run at %s (err = %v)", clonePath, err)
	}
}

func TestHandleChatCompletions_WorkDirsWithoutCloneUsesSource(t *testing.T) {
	chassis.RequireMajor(11)
	installFakeOpenCodeEchoingArgs(t)
	src := t.TempDir()
	h := NewHandler(nil, map[string]server.ToolFactory{
		"opencode": func() runner.Tool { return opencode.New() },
	}, server.NewRunRegistry(1), []string{"opencode"}, nil, nil)

	body := `{"model":"opencode","messages":[{"role":"user","content":"hello"}],"work_dirs":["` + src + `"]}`
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
	if resp.ClonedWorkDirs != 0 {
		t.Errorf("cloned_work_dirs = %d, want 0", resp.ClonedWorkDirs)
	}
	if args := resp.Choices[0].Message.Content; !strings.Contains(args, src) {
		t.Errorf("CLI args %q do not reference the source directory %s", args, src)
	}
	if strings.Contains(rec.Body.String(), "cloned_work_dirs") {
		t.Errorf("cloned_work_dirs present without cloning: %s", rec.Body.String())
	}
}

func TestHandleChatCompletions_CloneWorkDirsRejectsMissingDir(t *testing.T) {
	chassis.RequireMajor(11)
	installFakeOpenCode(t, "unused")
	h := NewHandler(nil, map[string]server.ToolFactory{
		"opencode": func() runner.Tool { return opencode.New() },
	}, server.NewRunRegistry(1), []string{"opencode"}, nil, nil)

	missing := filepath.Join(t.TempDir(), "does-not-exist")
	body := `{"model":"opencode","messages":[{"role":"user","content":"hello"}],` +
		`"work_dirs":["` + missing + `"],"clone_work_dirs":true}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var errResp ErrorResponse
	if err := json.NewDecoder(rec.Body).Decode(&errResp); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if errResp.Error.Code != "invalid_work_dir" {
		t.Errorf("code = %q, want invalid_work_dir", errResp.Error.Code)
	}
	if !strings.Contains(errResp.Error.Message, missing) {
		t.Errorf("message %q does not name the missing directory", errResp.Error.Message)
	}
}

func TestHandleChatCompletions_CloneWorkDirsWithoutWorkDirsIsNoOp(t *testing.T) {
	chassis.RequireMajor(11)
	installFakeOpenCode(t, "no work dirs")
	h := NewHandler(nil, map[string]server.ToolFactory{
		"opencode": func() runner.Tool { return opencode.New() },
	}, server.NewRunRegistry(1), []string{"opencode"}, nil, nil)

	body := `{"model":"opencode","messages":[{"role":"user","content":"hello"}],"clone_work_dirs":true}`
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
	if resp.ClonedWorkDirs != 0 {
		t.Errorf("cloned_work_dirs = %d, want 0", resp.ClonedWorkDirs)
	}
	if got := resp.Choices[0].Message.Content; got != "no work dirs" {
		t.Fatalf("content = %q, want 'no work dirs'", got)
	}
}

// extractClonePath pulls the scratch directory out of the fake CLI's argv.
func extractClonePath(t *testing.T, args string) string {
	t.Helper()
	for _, field := range strings.Fields(args) {
		if i := strings.Index(field, "rserve-clone-"); i >= 0 {
			return field
		}
	}
	t.Fatalf("no rserve-clone path in CLI args: %s", args)
	return ""
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
