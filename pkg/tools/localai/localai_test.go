package localai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"rcodegen/pkg/runner"
	"rcodegen/pkg/settings"

	chassis "github.com/ai8future/chassis-go/v11"
)

func TestMain(m *testing.M) {
	chassis.RequireMajor(11)
	os.Exit(m.Run())
}

func configureTool(tool *Tool, serverURL, model string) {
	s := settings.GetDefaultSettings()
	configured := settings.LocalAIDefaults{BaseURL: serverURL, Model: model, TimeoutSeconds: 2}
	if tool.flavor == FlavorOllama {
		s.Defaults.Ollama = configured
	} else {
		s.Defaults.LMStudio = configured
	}
	tool.SetSettings(s)
}

func TestToolIdentityDefaultsAndEfforts(t *testing.T) {
	ollama := NewOllama()
	lmstudio := NewLMStudio()
	if ollama.Name() != "ollama" || lmstudio.Name() != "lmstudio" {
		t.Fatalf("unexpected names: %q %q", ollama.Name(), lmstudio.Name())
	}
	if ollama.BinaryName() != "" || lmstudio.BinaryName() != "" {
		t.Fatal("API-only tools must not advertise binaries")
	}
	if got := ollama.ValidEfforts(); !reflect.DeepEqual(got, []string{"none", "low", "medium", "high", "max"}) {
		t.Fatalf("ollama efforts = %v", got)
	}
	if lmstudio.ValidEfforts() != nil {
		t.Fatal("LM Studio effort must remain unspecified in phase 1")
	}
	if ollama.DefaultModel() != "" || lmstudio.DefaultModel() != "" {
		t.Fatal("local runtime must not fabricate a default model")
	}
}

func TestStatsAndRunLogNeverExposeAPIKey(t *testing.T) {
	tool := NewLMStudio()
	configureTool(tool, "http://localhost:1234", "model")
	tool.settings.Defaults.LMStudio.APIKey = "top-secret"
	cfg := &runner.Config{Model: "model"}
	text := fmt.Sprint(tool.StatsJSONFields(cfg), tool.RunLogFields(cfg))
	if strings.Contains(text, "top-secret") || !strings.Contains(text, "http://localhost:1234") {
		t.Fatalf("stats/runlog = %s", text)
	}
}

func TestValidateConfig(t *testing.T) {
	tool := NewOllama()
	if err := tool.ValidateConfig(&runner.Config{}); err == nil || !strings.Contains(err.Error(), "ollama:<model>") {
		t.Fatalf("empty model error = %v", err)
	}
	if err := tool.ValidateConfig(&runner.Config{Model: "m", Effort: "ultra"}); err == nil {
		t.Fatal("unsupported effort accepted")
	}
	if err := tool.ValidateConfig(&runner.Config{Model: "m", Messages: []runner.ChatMessage{{Role: "tool", Content: "x"}}}); err == nil {
		t.Fatal("unsupported role accepted")
	}
	if err := tool.ValidateConfig(&runner.Config{Model: "m", Messages: []runner.ChatMessage{{Role: "user"}}}); err == nil {
		t.Fatal("empty content accepted")
	}
}

func TestRunDirectAPIOrderedMessagesEffortAndUsage(t *testing.T) {
	var got chatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"answer"}}],"usage":{"prompt_tokens":7,"completion_tokens":3}}`))
	}))
	defer server.Close()

	tool := NewOllama()
	configureTool(tool, server.URL, "test-model")
	var output, diagnostics bytes.Buffer
	cfg := &runner.Config{
		Model: "test-model", Effort: "max", Output: &output, Stderr: &diagnostics,
		Messages: []runner.ChatMessage{{Role: "system", Content: "rules"}, {Role: "user", Content: "question"}, {Role: "assistant", Content: "prior"}},
	}
	if exit := tool.RunDirectAPI(context.Background(), cfg, "", "flattened"); exit != 0 {
		t.Fatalf("exit = %d, diagnostic = %s", exit, diagnostics.String())
	}
	if output.String() != "answer" || got.ReasoningEffort != "max" || got.Stream {
		t.Fatalf("output/request mismatch: output=%q request=%+v", output.String(), got)
	}
	if len(got.Messages) != 3 || got.Messages[0].Role != "system" || got.Messages[2].Content != "prior" {
		t.Fatalf("message order was not preserved: %+v", got.Messages)
	}
	if cfg.TokenUsage == nil || cfg.TokenUsage.InputTokens != 7 || cfg.TokenUsage.OutputTokens != 3 {
		t.Fatalf("usage = %+v", cfg.TokenUsage)
	}
	usage, ok := tool.ReportedUsage(&runner.RunResult{TokenUsage: cfg.TokenUsage})
	if !ok || usage.InputTokens != 7 || usage.OutputTokens != 3 || usage.CostUSD != 0 {
		t.Fatalf("reported usage = %+v, %v", usage, ok)
	}
}

func TestRunDirectAPITaskFallbackAndLMStudioAuth(t *testing.T) {
	var got chatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer server.Close()
	tool := NewLMStudio()
	configureTool(tool, server.URL, "model")
	tool.settings.Defaults.LMStudio.APIKey = "token"
	var output bytes.Buffer
	cfg := &runner.Config{Model: "model", Output: &output}
	if exit := tool.RunDirectAPI(context.Background(), cfg, "", "hello"); exit != 0 {
		t.Fatalf("exit = %d", exit)
	}
	if len(got.Messages) != 1 || got.Messages[0].Role != "user" || got.Messages[0].Content != "hello" {
		t.Fatalf("fallback messages = %+v", got.Messages)
	}
	if got.ReasoningEffort != "" {
		t.Fatalf("LM Studio received reasoning_effort %q", got.ReasoningEffort)
	}
}

func TestRunDirectAPIFailuresAreBoundedAndRedacted(t *testing.T) {
	secret := "super-secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"denied super-secret` + strings.Repeat("x", maxDiagnosticBytes+100) + `"}}`))
	}))
	defer server.Close()
	tool := NewLMStudio()
	configureTool(tool, server.URL, "model")
	tool.settings.Defaults.LMStudio.APIKey = secret
	var diagnostics bytes.Buffer
	exit := tool.RunDirectAPI(context.Background(), &runner.Config{Model: "model", Stderr: &diagnostics}, "", "hello")
	if exit == 0 {
		t.Fatal("401 returned success")
	}
	if strings.Contains(diagnostics.String(), secret) || len(diagnostics.String()) > maxDiagnosticBytes+1 {
		t.Fatalf("diagnostic was not bounded/redacted: len=%d", len(diagnostics.String()))
	}
}

func TestRunDirectAPINonSuccessStatuses(t *testing.T) {
	for _, flavor := range []struct {
		name string
		new  func() *Tool
	}{
		{name: "ollama", new: NewOllama},
		{name: "lmstudio", new: NewLMStudio},
	} {
		for _, status := range []int{http.StatusUnauthorized, http.StatusNotFound, http.StatusInternalServerError} {
			t.Run(fmt.Sprintf("%s-%d", flavor.name, status), func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(status)
					_, _ = w.Write([]byte(`{"error":{"message":"upstream failure"}}`))
				}))
				defer server.Close()
				tool := flavor.new()
				configureTool(tool, server.URL, "model")
				if exit := tool.RunDirectAPI(context.Background(), &runner.Config{Model: "model"}, "", "hello"); exit == 0 {
					t.Fatalf("HTTP %d returned success", status)
				}
			})
		}
	}
}

func TestRunDirectAPIRejectsOversizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"`))
		_, _ = w.Write([]byte(strings.Repeat("x", maxResponseBytes)))
		_, _ = w.Write([]byte(`"}}]}`))
	}))
	defer server.Close()
	tool := NewOllama()
	configureTool(tool, server.URL, "model")
	var diagnostics bytes.Buffer
	exit := tool.RunDirectAPI(context.Background(), &runner.Config{Model: "model", Stderr: &diagnostics}, "", "hello")
	if exit == 0 || !strings.Contains(diagnostics.String(), "exceeds 32 MiB limit") {
		t.Fatalf("exit = %d, diagnostic = %q", exit, diagnostics.String())
	}
}

func TestRunDirectAPIMalformedMissingChoiceTimeoutAndCancellation(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"malformed", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("{")) }},
		{"missing choice", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"choices":[]}`)) }},
		{"timeout", func(w http.ResponseWriter, _ *http.Request) {
			time.Sleep(100 * time.Millisecond)
			_, _ = w.Write([]byte(`{"choices":[]}`))
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(tc.handler)
			defer server.Close()
			tool := NewOllama()
			configureTool(tool, server.URL, "model")
			ctx := context.Background()
			if tc.name == "timeout" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 10*time.Millisecond)
				defer cancel()
			}
			if exit := tool.RunDirectAPI(ctx, &runner.Config{Model: "model"}, "", "hello"); exit == 0 {
				t.Fatal("failure returned success")
			}
		})
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	tool := NewOllama()
	configureTool(tool, "http://localhost:11434", "model")
	if exit := tool.RunDirectAPI(ctx, &runner.Config{Model: "model"}, "", "hello"); exit == 0 {
		t.Fatal("cancelled request returned success")
	}
}

func TestRunnerDirectAPIResultContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	tool := NewOllama()
	configureTool(tool, server.URL, "model")
	res := runner.NewRunner(tool).RunWithContext(context.Background(), &runner.Config{Task: "hi", Model: "model", Output: &bytes.Buffer{}})
	server.Close()
	if res.ExitCode != 0 || res.Error != nil {
		t.Fatalf("success result = %+v", res)
	}

	res = runner.NewRunner(tool).RunWithContext(context.Background(), &runner.Config{Task: "hi", Model: "model"})
	if res.ExitCode == 0 || res.Error == nil {
		t.Fatalf("failed result = %+v", res)
	}
}
