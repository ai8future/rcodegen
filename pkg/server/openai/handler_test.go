package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestHandleChatCompletions_CloneWorkDirsRejectsUnsafeSymlink(t *testing.T) {
	chassis.RequireMajor(11)
	installFakeOpenCode(t, "unused")
	h := NewHandler(nil, map[string]server.ToolFactory{
		"opencode": func() runner.Tool { return opencode.New() },
	}, server.NewRunRegistry(1), []string{"opencode"}, nil, nil)

	src := t.TempDir()
	if err := os.Symlink("/etc/hosts", filepath.Join(src, "hosts-link")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	body := `{"model":"opencode","messages":[{"role":"user","content":"hello"}],` +
		`"work_dirs":["` + src + `"],"clone_work_dirs":true}`
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
	if errResp.Error.Code != "unsafe_symlink" {
		t.Errorf("code = %q, want unsafe_symlink", errResp.Error.Code)
	}
	if !strings.Contains(errResp.Error.Message, "hosts-link") {
		t.Errorf("message %q does not name the offending path", errResp.Error.Message)
	}
}

func TestHandleChatCompletions_CloneWorkDirsRejectsLinkedWorktree(t *testing.T) {
	chassis.RequireMajor(11)
	installFakeOpenCode(t, "unused")
	h := NewHandler(nil, map[string]server.ToolFactory{
		"opencode": func() runner.Tool { return opencode.New() },
	}, server.NewRunRegistry(1), []string{"opencode"}, nil, nil)

	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, ".git"), []byte("gitdir: /elsewhere/.git/worktrees/wt\n"), 0o644); err != nil {
		t.Fatalf("write .git file: %v", err)
	}

	body := `{"model":"opencode","messages":[{"role":"user","content":"hello"}],` +
		`"work_dirs":["` + src + `"],"clone_work_dirs":true}`
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
	if errResp.Error.Code != "unsupported_git_worktree" {
		t.Errorf("code = %q, want unsupported_git_worktree", errResp.Error.Code)
	}
}

// The same refusal applies to a pointer file anywhere in the tree, which is
// what a submodule checkout looks like.
func TestHandleChatCompletions_CloneWorkDirsRejectsNestedGitPointer(t *testing.T) {
	chassis.RequireMajor(11)
	installFakeOpenCode(t, "unused")
	h := NewHandler(nil, map[string]server.ToolFactory{
		"opencode": func() runner.Tool { return opencode.New() },
	}, server.NewRunRegistry(1), []string{"opencode"}, nil, nil)

	src := t.TempDir()
	nested := filepath.Join(src, "vendor", "lib")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nested, ".git"), []byte("gitdir: /elsewhere/.git/modules/lib\n"), 0o644); err != nil {
		t.Fatalf("write nested .git file: %v", err)
	}

	body := `{"model":"opencode","messages":[{"role":"user","content":"hello"}],` +
		`"work_dirs":["` + src + `"],"clone_work_dirs":true}`
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
	if errResp.Error.Code != codeUnsupportedGitWorktree {
		t.Errorf("code = %q, want %s", errResp.Error.Code, codeUnsupportedGitWorktree)
	}
	if errResp.Error.Retryable {
		t.Error("unsupported_git_worktree reported as retryable")
	}
	if !strings.Contains(errResp.Error.Message, filepath.Join("vendor", "lib", ".git")) {
		t.Errorf("message %q does not name the offending path", errResp.Error.Message)
	}
}

// A request that can never run must not wait for the run slot it will never
// use: Acquire blocks, so validating after it would hold the 400 behind
// whatever is already running.
func TestHandleChatCompletions_InvalidWorkDirRejectedWithoutARunSlot(t *testing.T) {
	chassis.RequireMajor(11)
	installFakeOpenCode(t, "unused")
	reg := server.NewRunRegistry(1)
	h := NewHandler(nil, map[string]server.ToolFactory{
		"opencode": func() runner.Tool { return opencode.New() },
	}, reg, []string{"opencode"}, nil, nil)

	// Occupy the only slot for the duration of the request.
	heldID, _, heldCancel, err := reg.Acquire(context.Background(), "opencode", "held")
	if err != nil {
		t.Fatalf("acquire holding slot: %v", err)
	}
	defer func() {
		heldCancel()
		reg.Release(heldID)
	}()

	missing := filepath.Join(t.TempDir(), "does-not-exist")
	body := `{"model":"opencode","messages":[{"role":"user","content":"hello"}],` +
		`"work_dirs":["` + missing + `"],"clone_work_dirs":true}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		h.ServeHTTP(rec, req)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("request blocked on a run slot instead of failing validation")
	}

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := reg.ActiveCount(); got != 1 {
		t.Errorf("active runs = %d, want 1 (the rejected request must not take a slot)", got)
	}
}

func TestHandleChatCompletions_StreamingReportsClonedWorkDirs(t *testing.T) {
	chassis.RequireMajor(11)
	installFakeOpenCodeEchoingArgs(t)
	src := t.TempDir()
	h := NewHandler(nil, map[string]server.ToolFactory{
		"opencode": func() runner.Tool { return opencode.New() },
	}, server.NewRunRegistry(1), []string{"opencode"}, nil, nil)

	body := `{"model":"opencode","messages":[{"role":"user","content":"hello"}],"stream":true,` +
		`"work_dirs":["` + src + `"],"clone_work_dirs":true}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	chunks := parseSSEChunks(t, rec.Body.String())
	final := chunks[len(chunks)-1]
	if final.ClonedWorkDirs != 1 {
		t.Errorf("final chunk cloned_work_dirs = %d, want 1", final.ClonedWorkDirs)
	}
	if final.Choices[0].FinishReason == nil {
		t.Error("final chunk has no finish_reason")
	}
	// Earlier chunks carry content, not the clone count.
	for i, c := range chunks[:len(chunks)-1] {
		if c.ClonedWorkDirs != 0 {
			t.Errorf("chunk %d reports cloned_work_dirs = %d before the final chunk", i, c.ClonedWorkDirs)
		}
	}
}

// parseSSEChunks decodes every "data:" frame of an SSE body except [DONE].
func parseSSEChunks(t *testing.T, body string) []ChatCompletionChunk {
	t.Helper()
	var chunks []ChatCompletionChunk
	for _, line := range strings.Split(body, "\n") {
		payload, ok := strings.CutPrefix(line, "data: ")
		if !ok || payload == "[DONE]" {
			continue
		}
		var chunk ChatCompletionChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			t.Fatalf("decode SSE frame %q: %v", payload, err)
		}
		chunks = append(chunks, chunk)
	}
	if len(chunks) == 0 {
		t.Fatalf("no SSE data frames in body: %s", body)
	}
	return chunks
}

// blockingDirectAPITool takes the direct-API path and stays inside it until its
// context is cancelled, standing in for a slow API call.
type blockingDirectAPITool struct {
	runner.Tool
	started chan string // receives the directory the run was pointed at
}

func (t *blockingDirectAPITool) ShouldUseDirectAPI(*runner.Config) bool { return true }

func (t *blockingDirectAPITool) RunDirectAPI(ctx context.Context, cfg *runner.Config, workDir, task string) int {
	t.started <- workDir
	<-ctx.Done()
	return 130
}

// A client that disconnects mid-run must not leave the direct-API path holding
// its run slot and scratch clone until the API answers.
func TestHandleChatCompletions_DirectAPIRunCancelsWithTheClient(t *testing.T) {
	chassis.RequireMajor(11)
	tool := &blockingDirectAPITool{Tool: opencode.New(), started: make(chan string, 1)}
	reg := server.NewRunRegistry(1)
	h := NewHandler(nil, map[string]server.ToolFactory{
		"opencode": func() runner.Tool { return tool },
	}, reg, []string{"opencode"}, nil, nil)

	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "main.go"), []byte("package main"), 0o644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	body := `{"model":"opencode","messages":[{"role":"user","content":"hello"}],` +
		`"work_dirs":["` + src + `"],"clone_work_dirs":true}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)).WithContext(ctx)
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		h.ServeHTTP(rec, req)
	}()

	var clonePath string
	select {
	case clonePath = <-tool.started:
	case <-time.After(10 * time.Second):
		t.Fatal("direct-API run never started")
	}
	if !strings.Contains(clonePath, "rserve-clone-") {
		t.Fatalf("run was pointed at %s, not a scratch clone", clonePath)
	}
	if _, err := os.Stat(clonePath); err != nil {
		t.Fatalf("scratch clone missing while the run is live: %v", err)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("direct-API run outlived its cancelled request")
	}

	if _, err := os.Stat(clonePath); !os.IsNotExist(err) {
		t.Errorf("scratch clone survived the cancelled run at %s (err = %v)", clonePath, err)
	}
	if got := reg.ActiveCount(); got != 0 {
		t.Errorf("active runs = %d after cancellation, want 0", got)
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

func TestHandleChatCompletions_EchoesCorrelationIDOnSuccess(t *testing.T) {
	chassis.RequireMajor(11)
	installFakeOpenCode(t, "correlated output")
	h := NewHandler(nil, map[string]server.ToolFactory{
		"opencode": func() runner.Tool { return opencode.New() },
	}, server.NewRunRegistry(1), []string{"opencode"}, nil, nil)

	body := `{"model":"opencode","messages":[{"role":"user","content":"hello"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("X-Correlation-ID", "wm-job-77")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Correlation-ID"); got != "wm-job-77" {
		t.Errorf("X-Correlation-ID header = %q, want wm-job-77", got)
	}
	var resp ChatCompletionResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.CorrelationID != "wm-job-77" {
		t.Errorf("correlation_id = %q, want wm-job-77", resp.CorrelationID)
	}
}

// Without a correlation header nothing is invented: no echo header, no body
// field.
func TestHandleChatCompletions_OmitsCorrelationIDWhenUnset(t *testing.T) {
	chassis.RequireMajor(11)
	installFakeOpenCode(t, "uncorrelated output")
	h := NewHandler(nil, map[string]server.ToolFactory{
		"opencode": func() runner.Tool { return opencode.New() },
	}, server.NewRunRegistry(1), []string{"opencode"}, nil, nil)

	body := `{"model":"opencode","messages":[{"role":"user","content":"hello"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("X-Correlation-ID"); got != "" {
		t.Errorf("X-Correlation-ID header = %q, want none", got)
	}
	if strings.Contains(rec.Body.String(), "correlation_id") {
		t.Errorf("correlation_id present without a request header: %s", rec.Body.String())
	}
}

// The errors are what a caller most needs to tie back to its own job, so the
// echo has to survive the failure paths — and carry the retry verdict.
func TestHandleChatCompletions_EchoesCorrelationIDOnError(t *testing.T) {
	chassis.RequireMajor(11)
	h := NewHandler(nil, map[string]server.ToolFactory{}, server.NewRunRegistry(1), nil, nil, nil)

	body := `{"model":"nosuchtool","messages":[{"role":"user","content":"hello"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("X-Correlation-ID", "wm-job-err")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Correlation-ID"); got != "wm-job-err" {
		t.Errorf("X-Correlation-ID header = %q, want wm-job-err", got)
	}
	var errResp ErrorResponse
	if err := json.NewDecoder(rec.Body).Decode(&errResp); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if errResp.Error.Code != codeUnknownTool {
		t.Errorf("code = %q, want %s", errResp.Error.Code, codeUnknownTool)
	}
	if errResp.Error.Retryable {
		t.Error("unknown_tool reported as retryable")
	}
}

// Externally supplied identifiers are sanitized before they are echoed or
// logged, so a caller cannot inject control characters into either.
func TestHandleChatCompletions_SanitizesEchoedCorrelationID(t *testing.T) {
	chassis.RequireMajor(11)
	installFakeOpenCode(t, "sanitized")
	h := NewHandler(nil, map[string]server.ToolFactory{
		"opencode": func() runner.Tool { return opencode.New() },
	}, server.NewRunRegistry(1), []string{"opencode"}, nil, nil)

	body := `{"model":"opencode","messages":[{"role":"user","content":"hello"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("X-Correlation-ID", "wm job\t42!")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("X-Correlation-ID"); got != "wmjob42" {
		t.Errorf("X-Correlation-ID header = %q, want wmjob42", got)
	}
	var resp ChatCompletionResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.CorrelationID != "wmjob42" {
		t.Errorf("correlation_id = %q, want wmjob42", resp.CorrelationID)
	}
}

func TestHandleChatCompletions_StreamingEchoesCorrelationIDOnFinalChunk(t *testing.T) {
	chassis.RequireMajor(11)
	installFakeOpenCode(t, "streamed correlated output")
	h := NewHandler(nil, map[string]server.ToolFactory{
		"opencode": func() runner.Tool { return opencode.New() },
	}, server.NewRunRegistry(1), []string{"opencode"}, nil, nil)

	body := `{"model":"opencode","messages":[{"role":"user","content":"hello"}],"stream":true}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("X-Correlation-ID", "wm-stream-9")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Correlation-ID"); got != "wm-stream-9" {
		t.Errorf("X-Correlation-ID header = %q, want wm-stream-9", got)
	}
	chunks := parseSSEChunks(t, rec.Body.String())
	final := chunks[len(chunks)-1]
	if final.CorrelationID != "wm-stream-9" {
		t.Errorf("final chunk correlation_id = %q, want wm-stream-9", final.CorrelationID)
	}
	for i, c := range chunks[:len(chunks)-1] {
		if c.CorrelationID != "" {
			t.Errorf("chunk %d carries correlation_id before the final chunk", i)
		}
	}
}

// Chat runs join bundle runs in the registry: a caller's identifier is on the
// entry, so status output can say which external job owns each slot.
func TestHandleChatCompletions_RecordsCorrelationIDInRunRegistry(t *testing.T) {
	chassis.RequireMajor(11)
	tool := &blockingDirectAPITool{Tool: opencode.New(), started: make(chan string, 1)}
	reg := server.NewRunRegistry(1)
	h := NewHandler(nil, map[string]server.ToolFactory{
		"opencode": func() runner.Tool { return tool },
	}, reg, []string{"opencode"}, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	body := `{"model":"opencode","messages":[{"role":"user","content":"hello"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)).WithContext(ctx)
	req.Header.Set("X-Correlation-ID", "wm-registry-5")
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		h.ServeHTTP(rec, req)
	}()

	select {
	case <-tool.started:
	case <-time.After(10 * time.Second):
		t.Fatal("run never started")
	}

	runs := reg.List()
	if len(runs) != 1 {
		t.Fatalf("active runs = %d, want 1", len(runs))
	}
	if runs[0].CorrelationID != "wm-registry-5" {
		t.Errorf("registry correlation_id = %q, want wm-registry-5", runs[0].CorrelationID)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("run outlived its cancelled request")
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
