package batch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"rcodegen/pkg/settings"

	chassis "github.com/ai8future/chassis-go/v11"
)

func TestLocalExecutorRetainsLocalAIOutput(t *testing.T) {
	chassis.RequireMajor(11)
	var effort string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			ReasoningEffort string `json:"reasoning_effort"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		effort = payload.ReasoningEffort
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"batch output"}}]}`))
	}))
	defer backend.Close()
	s := settings.GetDefaultSettings()
	s.Defaults.Ollama.BaseURL = backend.URL
	executor := NewLocalExecutor(s)
	result, err := executor.Execute(context.Background(), &JobDef{Name: "local", Tool: "ollama", Model: "model-high", Effort: "max", Task: "hello"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 || result.Output != "batch output" || result.OutputTruncated || result.Error != "" || effort != "max" {
		t.Fatalf("result = %+v", result)
	}
}

func TestLocalExecutorCapturesLocalAIFailure(t *testing.T) {
	chassis.RequireMajor(11)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"backend broke"}}`))
	}))
	defer backend.Close()
	s := settings.GetDefaultSettings()
	s.Defaults.LMStudio.BaseURL = backend.URL
	result, err := NewLocalExecutor(s).Execute(context.Background(), &JobDef{Name: "local", Tool: "lmstudio", Model: "model", Task: "hello"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode == 0 || result.Error == "" {
		t.Fatalf("result = %+v", result)
	}
}

func TestLocalExecutorBoundsOutputAndHandlesCancellation(t *testing.T) {
	chassis.RequireMajor(11)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"` + strings.Repeat("x", 70<<10) + `"}}]}`))
	}))
	defer backend.Close()
	s := settings.GetDefaultSettings()
	s.Defaults.Ollama.BaseURL = backend.URL
	executor := NewLocalExecutor(s)
	result, err := executor.Execute(context.Background(), &JobDef{Name: "bounded", Tool: "ollama", Model: "model", Task: "hello"}, "")
	if err != nil || result.ExitCode != 0 || !result.OutputTruncated || len(result.Output) != 64<<10 {
		t.Fatalf("bounded result = %+v, err = %v", result, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err = executor.Execute(ctx, &JobDef{Name: "cancelled", Tool: "ollama", Model: "model", Task: "hello"}, "")
	if err != nil || result.ExitCode == 0 || !strings.Contains(result.Error, "cancelled") {
		t.Fatalf("cancelled result = %+v, err = %v", result, err)
	}
}
