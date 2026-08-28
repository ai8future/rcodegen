package executor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"rcodegen/pkg/bundle"
	"rcodegen/pkg/orchestrator"
	"rcodegen/pkg/runner"
	"rcodegen/pkg/settings"
	"rcodegen/pkg/tools/claude"
	"rcodegen/pkg/tools/localai"
	"rcodegen/pkg/workspace"

	chassis "github.com/ai8future/chassis-go/v11"
)

func TestExtractCostInfo_OpencodePlaceholder(t *testing.T) {
	info := extractCostInfo("opencode", `{"any":"json"}`, "")
	if info.InputTokens != 0 || info.OutputTokens != 0 || info.CostUSD != 0 {
		t.Errorf("expected zero usage placeholder, got input=%d output=%d cost=%v", info.InputTokens, info.OutputTokens, info.CostUSD)
	}
}

func TestExtractSessionID_OpencodePlaceholder(t *testing.T) {
	if got := extractSessionID("opencode", `{"session":"abc"}`, ""); got != "" {
		t.Errorf("expected empty session placeholder, got %q", got)
	}
}

func TestExtractCostInfo_KilocodePlaceholder(t *testing.T) {
	info := extractCostInfo("kilocode", `{"any":"json"}`, "")
	if info.InputTokens != 0 || info.OutputTokens != 0 || info.CostUSD != 0 {
		t.Errorf("expected zero usage placeholder, got input=%d output=%d cost=%v", info.InputTokens, info.OutputTokens, info.CostUSD)
	}
}

func TestExtractSessionID_KilocodePlaceholder(t *testing.T) {
	if got := extractSessionID("kilocode", `{"session":"abc"}`, ""); got != "" {
		t.Errorf("expected empty session placeholder, got %q", got)
	}
}

func TestToolExecutorUsesDirectAPIForLocalRuntime(t *testing.T) {
	chassis.RequireMajor(11)
	var effort string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		effort, _ = payload["reasoning_effort"].(string)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"bundle local output"}}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`))
	}))
	defer backend.Close()
	s := settings.GetDefaultSettings()
	s.Defaults.Ollama.BaseURL = backend.URL
	tool := localai.NewOllama()
	tool.SetSettings(s)
	ws, err := workspace.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	workDir := t.TempDir()
	executor := &ToolExecutor{Tools: map[string]runner.Tool{"ollama": tool}}
	env, err := executor.Execute(&bundle.Step{Name: "local", Tool: "ollama", Model: "model-high", Effort: "max", Task: "hello"},
		orchestrator.NewContext(context.Background(), map[string]string{"codebase": workDir}), ws)
	if err != nil || env.Status != "success" {
		t.Fatalf("Execute = %+v, %v", env, err)
	}
	if effort != "max" || env.Result["input_tokens"] != 5 || env.Result["output_tokens"] != 2 {
		t.Fatalf("effort/result = %q %+v", effort, env.Result)
	}
	data, err := os.ReadFile(env.OutputRef)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(data) || !strings.Contains(string(data), "bundle local output") {
		t.Fatalf("output = %s", data)
	}
	logData, err := os.ReadFile(ws.JobDir + "/logs/local.log")
	if err != nil || !strings.Contains(string(logData), "bundle local output") {
		t.Fatalf("log = %q, err = %v", logData, err)
	}
}

func TestToolExecutorDirectAPIFailureCancellationAndValidation(t *testing.T) {
	chassis.RequireMajor(11)
	ws, err := workspace.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	workDir := t.TempDir()

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"backend failed"}}`))
	}))
	defer backend.Close()
	s := settings.GetDefaultSettings()
	s.Defaults.Ollama.BaseURL = backend.URL
	tool := localai.NewOllama()
	tool.SetSettings(s)
	executor := &ToolExecutor{Tools: map[string]runner.Tool{"ollama": tool}}

	env, err := executor.Execute(&bundle.Step{Name: "failure", Tool: "ollama", Model: "model", Task: "hello"},
		orchestrator.NewContext(context.Background(), map[string]string{"codebase": workDir}), ws)
	if err != nil || env.Error == nil || env.Error.Code != "EXEC_FAILED" {
		t.Fatalf("failure envelope = %+v, err = %v", env, err)
	}

	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel()
	env, err = executor.Execute(&bundle.Step{Name: "cancel", Tool: "ollama", Model: "model", Task: "hello"},
		orchestrator.NewContext(cancelCtx, map[string]string{"codebase": workDir}), ws)
	if err != context.Canceled || env.Error == nil || env.Error.Code != "CANCELLED" {
		t.Fatalf("cancel envelope = %+v, err = %v", env, err)
	}

	invalidTool := localai.NewOllama()
	invalidTool.SetSettings(settings.GetDefaultSettings())
	env, err = (&ToolExecutor{Tools: map[string]runner.Tool{"ollama": invalidTool}}).Execute(
		&bundle.Step{Name: "invalid", Tool: "ollama", Task: "hello"},
		orchestrator.NewContext(context.Background(), map[string]string{"codebase": workDir}), ws)
	if err != nil || env.Error == nil || env.Error.Code != "INVALID_CONFIG" {
		t.Fatalf("invalid envelope = %+v, err = %v", env, err)
	}

	env, err = (&ToolExecutor{Tools: map[string]runner.Tool{"claude": claude.New()}}).Execute(
		&bundle.Step{Name: "conflict", Tool: "claude", Model: "opus-high", Effort: "low", Task: "hello"},
		orchestrator.NewContext(context.Background(), map[string]string{"codebase": workDir}), ws)
	if err != nil || env.Error == nil || env.Error.Code != "INVALID_CONFIG" || !strings.Contains(env.Error.Message, "conflicting") {
		t.Fatalf("conflict envelope = %+v, err = %v", env, err)
	}
}
