package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
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

// A tool whose CLI reports nothing says so, rather than publishing zeros that
// look like a measured free run.
func TestHandleChatCompletions_UnreportedUsage(t *testing.T) {
	chassis.RequireMajor(11)
	installFakeOpenCode(t, "no usage here")
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
	if resp.UsageSource != usageSourceUnreported {
		t.Errorf("usage_source = %q, want %s", resp.UsageSource, usageSourceUnreported)
	}
	if resp.Usage != nil {
		t.Errorf("usage = %+v, want none for an unreporting tool", resp.Usage)
	}
	if strings.Contains(rec.Body.String(), "cost_usd") {
		t.Errorf("cost_usd present for an unreporting tool: %s", rec.Body.String())
	}
}

// reportingTool stands in for a CLI that publishes usage, so the "cli"
// provenance path can be exercised without one.
type reportingTool struct {
	runner.Tool
	usage runner.RunUsage
}

func (t *reportingTool) ReportedUsage(*runner.RunResult) (runner.RunUsage, bool) {
	return t.usage, true
}

func TestHandleChatCompletions_ReportedUsageAndCost(t *testing.T) {
	chassis.RequireMajor(11)
	installFakeOpenCode(t, "usage reported")
	tool := &reportingTool{
		Tool:  opencode.New(),
		usage: runner.RunUsage{InputTokens: 1200, OutputTokens: 3500, CostUSD: 0.0432},
	}
	h := NewHandler(nil, map[string]server.ToolFactory{
		"opencode": func() runner.Tool { return tool },
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
	if resp.UsageSource != usageSourceCLI {
		t.Errorf("usage_source = %q, want %s", resp.UsageSource, usageSourceCLI)
	}
	if resp.CostUSD != 0.0432 {
		t.Errorf("cost_usd = %v, want 0.0432", resp.CostUSD)
	}
	if resp.Usage == nil {
		t.Fatal("usage missing for a reporting tool")
	}
	if resp.Usage.PromptTokens != 1200 || resp.Usage.CompletionTokens != 3500 || resp.Usage.TotalTokens != 4700 {
		t.Errorf("usage = %+v, want 1200/3500/4700", resp.Usage)
	}
}

// A tool that reports tokens but no cost (gemini's shape) must not publish a
// zero cost.
func TestHandleChatCompletions_ReportedUsageWithoutCostOmitsCost(t *testing.T) {
	chassis.RequireMajor(11)
	installFakeOpenCode(t, "tokens only")
	tool := &reportingTool{
		Tool:  opencode.New(),
		usage: runner.RunUsage{InputTokens: 800, OutputTokens: 250},
	}
	h := NewHandler(nil, map[string]server.ToolFactory{
		"opencode": func() runner.Tool { return tool },
	}, server.NewRunRegistry(1), []string{"opencode"}, nil, nil)

	body := `{"model":"opencode","messages":[{"role":"user","content":"hello"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if strings.Contains(rec.Body.String(), "cost_usd") {
		t.Errorf("cost_usd present when the tool reported none: %s", rec.Body.String())
	}
	var resp ChatCompletionResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.UsageSource != usageSourceCLI {
		t.Errorf("usage_source = %q, want %s", resp.UsageSource, usageSourceCLI)
	}
	if resp.Usage == nil || resp.Usage.TotalTokens != 1050 {
		t.Errorf("usage = %+v, want 1050 total tokens", resp.Usage)
	}
}

func TestHandleChatCompletions_StreamingReportsUsageOnFinalChunk(t *testing.T) {
	chassis.RequireMajor(11)
	installFakeOpenCode(t, "streamed with usage")
	tool := &reportingTool{
		Tool:  opencode.New(),
		usage: runner.RunUsage{InputTokens: 10, OutputTokens: 20, CostUSD: 0.5},
	}
	h := NewHandler(nil, map[string]server.ToolFactory{
		"opencode": func() runner.Tool { return tool },
	}, server.NewRunRegistry(1), []string{"opencode"}, nil, nil)

	body := `{"model":"opencode","messages":[{"role":"user","content":"hello"}],"stream":true}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	chunks := parseSSEChunks(t, rec.Body.String())
	final := chunks[len(chunks)-1]
	if final.UsageSource != usageSourceCLI {
		t.Errorf("final chunk usage_source = %q, want %s", final.UsageSource, usageSourceCLI)
	}
	if final.CostUSD != 0.5 {
		t.Errorf("final chunk cost_usd = %v, want 0.5", final.CostUSD)
	}
	if final.Usage == nil || final.Usage.TotalTokens != 30 {
		t.Errorf("final chunk usage = %+v, want 30 total tokens", final.Usage)
	}
	for i, c := range chunks[:len(chunks)-1] {
		if c.UsageSource != "" {
			t.Errorf("chunk %d carries usage_source before the final chunk", i)
		}
	}
}

// A request that waits for a busy slot is indistinguishable from a slow one
// unless the wait is announced. Streaming callers hear it as it happens.
func TestHandleChatCompletions_StreamingAnnouncesQueuePosition(t *testing.T) {
	chassis.RequireMajor(11)
	installFakeOpenCode(t, "ran after waiting")
	reg := server.NewRunRegistry(1)
	h := NewHandler(nil, map[string]server.ToolFactory{
		"opencode": func() runner.Tool { return opencode.New() },
	}, reg, []string{"opencode"}, nil, nil)

	heldID, _, heldCancel, err := reg.Acquire(context.Background(), "opencode", "held")
	if err != nil {
		t.Fatalf("acquire holding slot: %v", err)
	}

	body := `{"model":"opencode","messages":[{"role":"user","content":"hello"}],"stream":true}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.ServeHTTP(rec, req)
	}()

	waitForQueued(t, reg, 1)
	heldCancel()
	reg.Release(heldID)

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("queued request never completed after the slot freed")
	}

	frames := parseSSEFrames(t, rec.Body.String())
	if len(frames) < 2 {
		t.Fatalf("expected queue events, got frames %v", frames)
	}
	var queued queueEvent
	if err := json.Unmarshal([]byte(frames[0]), &queued); err != nil {
		t.Fatalf("decode first frame %q: %v", frames[0], err)
	}
	if queued.Type != "queued" || queued.Position != 1 {
		t.Errorf("first frame = %+v, want queued at position 1", queued)
	}
	var started queueEvent
	if err := json.Unmarshal([]byte(frames[1]), &started); err != nil {
		t.Fatalf("decode second frame %q: %v", frames[1], err)
	}
	if started.Type != "started" {
		t.Errorf("second frame = %+v, want started", started)
	}
	if !strings.Contains(rec.Body.String(), "ran after waiting") {
		t.Errorf("run output missing from the stream: %s", rec.Body.String())
	}
}

// No wait, no events: an unqueued stream is byte-for-byte what it always was.
func TestHandleChatCompletions_StreamingWithoutWaitHasNoQueueEvents(t *testing.T) {
	chassis.RequireMajor(11)
	installFakeOpenCode(t, "no waiting")
	h := NewHandler(nil, map[string]server.ToolFactory{
		"opencode": func() runner.Tool { return opencode.New() },
	}, server.NewRunRegistry(1), []string{"opencode"}, nil, nil)

	body := `{"model":"opencode","messages":[{"role":"user","content":"hello"}],"stream":true}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if strings.Contains(rec.Body.String(), `"queued"`) || strings.Contains(rec.Body.String(), `"started"`) {
		t.Errorf("queue events on a stream that never waited: %s", rec.Body.String())
	}
}

// Non-streaming callers cannot be told mid-flight, so they get the total
// afterwards — and only when there was one.
func TestHandleChatCompletions_NonStreamingReportsQueueWaitHeader(t *testing.T) {
	chassis.RequireMajor(11)
	installFakeOpenCode(t, "ran after waiting")
	reg := server.NewRunRegistry(1)
	h := NewHandler(nil, map[string]server.ToolFactory{
		"opencode": func() runner.Tool { return opencode.New() },
	}, reg, []string{"opencode"}, nil, nil)

	heldID, _, heldCancel, err := reg.Acquire(context.Background(), "opencode", "held")
	if err != nil {
		t.Fatalf("acquire holding slot: %v", err)
	}

	body := `{"model":"opencode","messages":[{"role":"user","content":"hello"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.ServeHTTP(rec, req)
	}()

	waitForQueued(t, reg, 1)
	// Hold it long enough that the wait cannot round down to zero.
	time.Sleep(10 * time.Millisecond)
	heldCancel()
	reg.Release(heldID)

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("queued request never completed after the slot freed")
	}

	got := rec.Header().Get("X-Queue-Wait-Ms")
	if got == "" {
		t.Fatal("X-Queue-Wait-Ms missing after a queued request")
	}
	ms, err := strconv.Atoi(got)
	if err != nil || ms <= 0 {
		t.Errorf("X-Queue-Wait-Ms = %q, want a positive integer", got)
	}
}

func TestHandleChatCompletions_NoQueueWaitHeaderWithoutAWait(t *testing.T) {
	chassis.RequireMajor(11)
	installFakeOpenCode(t, "no waiting")
	h := NewHandler(nil, map[string]server.ToolFactory{
		"opencode": func() runner.Tool { return opencode.New() },
	}, server.NewRunRegistry(1), []string{"opencode"}, nil, nil)

	body := `{"model":"opencode","messages":[{"role":"user","content":"hello"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("X-Queue-Wait-Ms"); got != "" {
		t.Errorf("X-Queue-Wait-Ms = %q on a request that never waited", got)
	}
}

// A saturated server is a different condition from a busy one, and /health has
// to be able to say which.
func TestHandleHealth_ReportsQueuedRequests(t *testing.T) {
	reg := server.NewRunRegistry(1)
	h := NewHandler(nil, nil, reg, nil, nil, nil)

	heldID, _, heldCancel, err := reg.Acquire(context.Background(), "opencode", "held")
	if err != nil {
		t.Fatalf("acquire holding slot: %v", err)
	}
	defer func() {
		heldCancel()
		reg.Release(heldID)
	}()

	waiterCtx, cancelWaiter := context.WithCancel(context.Background())
	waiterDone := make(chan struct{})
	go func() {
		defer close(waiterDone)
		_, _ = reg.AcquireWith(waiterCtx, "opencode", "waiter", server.AcquireOptions{})
	}()
	waitForQueued(t, reg, 1)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	var hr HealthResponse
	if err := json.NewDecoder(rec.Body).Decode(&hr); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if hr.ActiveRuns != 1 {
		t.Errorf("active_runs = %d, want 1", hr.ActiveRuns)
	}
	if hr.Queued != 1 {
		t.Errorf("queued = %d, want 1", hr.Queued)
	}

	cancelWaiter()
	<-waiterDone
}

func TestHandleHealth_QueuedIsAlwaysPresent(t *testing.T) {
	h := NewHandler(nil, nil, server.NewRunRegistry(5), nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if !strings.Contains(rec.Body.String(), `"queued":0`) {
		t.Errorf("health body omits queued when idle: %s", rec.Body.String())
	}
}

// waitForQueued blocks until the registry reports want waiters.
func waitForQueued(t *testing.T, reg *server.RunRegistry, want int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if reg.QueuedCount() == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("queued count never reached %d (currently %d)", want, reg.QueuedCount())
}

// parseSSEFrames returns the raw payload of every "data:" frame except [DONE].
func parseSSEFrames(t *testing.T, body string) []string {
	t.Helper()
	var frames []string
	for _, line := range strings.Split(body, "\n") {
		payload, ok := strings.CutPrefix(line, "data: ")
		if !ok || payload == "[DONE]" {
			continue
		}
		frames = append(frames, payload)
	}
	if len(frames) == 0 {
		t.Fatalf("no SSE data frames in body: %s", body)
	}
	return frames
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
