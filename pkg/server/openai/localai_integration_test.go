package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"rcodegen/pkg/runner"
	"rcodegen/pkg/server"
	"rcodegen/pkg/settings"
	"rcodegen/pkg/tools/localai"

	chassis "github.com/ai8future/chassis-go/v11"
)

func localFactories() map[string]server.ToolFactory {
	return map[string]server.ToolFactory{
		"ollama":   func() runner.Tool { return localai.NewOllama() },
		"lmstudio": func() runner.Tool { return localai.NewLMStudio() },
	}
}

func TestLocalAIChatCompletionPreservesMessagesEffortAndUsage(t *testing.T) {
	chassis.RequireMajor(11)
	var request struct {
		Model           string    `json:"model"`
		ReasoningEffort string    `json:"reasoning_effort"`
		Messages        []Message `json:"messages"`
	}
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"local answer"}}],"usage":{"prompt_tokens":11,"completion_tokens":4}}`))
	}))
	defer backend.Close()

	s := settings.GetDefaultSettings()
	s.Defaults.Ollama.BaseURL = backend.URL
	s.Defaults.Ollama.Model = "configured"
	h := NewHandler(s, localFactories(), server.NewRunRegistry(1), []string{"ollama", "lmstudio"}, nil, nil)
	body := `{"model":"ollama:literal-high","reasoning_effort":"max","messages":[` +
		`{"role":"system","content":"rules"},{"role":"user","content":"question"},{"role":"assistant","content":"history"}]}`
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if request.Model != "literal-high" || request.ReasoningEffort != "max" {
		t.Fatalf("backend request = %+v", request)
	}
	if len(request.Messages) != 3 || request.Messages[0].Role != "system" || request.Messages[2].Content != "history" {
		t.Fatalf("messages = %+v", request.Messages)
	}
	var response ChatCompletionResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Choices[0].Message.Content != "local answer" || response.Usage == nil || response.Usage.TotalTokens != 15 {
		t.Fatalf("response = %+v", response)
	}

	for _, effort := range []string{"none", "low", "medium", "high", "max"} {
		model := "literal-" + effort
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(
			`{"model":"ollama:`+model+`","reasoning_effort":"`+effort+`","messages":[{"role":"user","content":"hi"}]}`)))
		if rec.Code != http.StatusOK || request.Model != model || request.ReasoningEffort != effort {
			t.Fatalf("effort %q response/request = %d %+v", effort, rec.Code, request)
		}
	}

	bare := httptest.NewRecorder()
	h.ServeHTTP(bare, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(
		`{"model":"ollama","messages":[{"role":"user","content":"hi"}]}`)))
	if bare.Code != http.StatusOK || request.Model != "configured" {
		t.Fatalf("configured bare request = %d %+v", bare.Code, request)
	}
}

func TestLocalAIValidationAndFailureContracts(t *testing.T) {
	chassis.RequireMajor(11)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"runtime unavailable"}}`))
	}))
	defer backend.Close()
	s := settings.GetDefaultSettings()
	s.Defaults.Ollama.BaseURL = backend.URL
	s.Defaults.Ollama.Model = ""
	h := NewHandler(s, localFactories(), server.NewRunRegistry(1), []string{"ollama"}, nil, nil)

	bare := httptest.NewRecorder()
	h.ServeHTTP(bare, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(
		`{"model":"ollama","messages":[{"role":"user","content":"hi"}]}`)))
	if bare.Code != http.StatusBadRequest || !strings.Contains(bare.Body.String(), "invalid_model") {
		t.Fatalf("bare response = %d %s", bare.Code, bare.Body.String())
	}

	failure := httptest.NewRecorder()
	h.ServeHTTP(failure, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(
		`{"model":"ollama:model","messages":[{"role":"user","content":"hi"}]}`)))
	if failure.Code != http.StatusBadGateway || !strings.Contains(failure.Body.String(), "tool_execution_failed") || !strings.Contains(failure.Body.String(), "runtime unavailable") {
		t.Fatalf("failure response = %d %s", failure.Code, failure.Body.String())
	}

	stream := httptest.NewRecorder()
	h.ServeHTTP(stream, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(
		`{"model":"ollama:model","stream":true,"messages":[{"role":"user","content":"hi"}]}`)))
	if !strings.Contains(stream.Body.String(), "tool_execution_failed") || strings.Contains(stream.Body.String(), `"finish_reason":"stop"`) || !strings.Contains(stream.Body.String(), "[DONE]") {
		t.Fatalf("stream failure = %s", stream.Body.String())
	}
}

func TestLocalAIAsyncFailureIsRetainedAndDelivered(t *testing.T) {
	chassis.RequireMajor(11)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"message":"runtime loading failed"}}`))
	}))
	defer backend.Close()
	receiver := newCallbackReceiver(t, http.StatusOK)
	s := settings.GetDefaultSettings()
	s.Defaults.Ollama.BaseURL = backend.URL
	h := NewHandler(s, localFactories(), server.NewRunRegistry(1), []string{"ollama"}, nil, nil)
	body := `{"model":"ollama:model","messages":[{"role":"user","content":"hi"}],"callback_url":"` + receiver.server.URL + `"}`
	submitted := submitAsync(t, h, body, "local-failure")
	payload := receiver.await(t, 1)
	if payload.Status != runStatusFailure || payload.Error == nil || payload.Error.Code != codeToolExecutionFailed || !payload.Error.Retryable {
		t.Fatalf("callback payload = %+v", payload)
	}

	rec := do(t, h, http.MethodGet, "/v1/runs/"+submitted.RunID+"/result")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "tool_execution_failed") {
		t.Fatalf("retained result = %d %s", rec.Code, rec.Body.String())
	}
}

func TestLocalAIModelInventoryAvailability(t *testing.T) {
	chassis.RequireMajor(11)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			_, _ = w.Write([]byte(`{"models":[{"name":"configured"},{"name":"other"}]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer backend.Close()
	s := settings.GetDefaultSettings()
	s.Defaults.Ollama.BaseURL = backend.URL
	s.Defaults.Ollama.Model = "configured"
	s.Defaults.LMStudio.BaseURL = "http://127.0.0.1:1"
	s.Defaults.LMStudio.Model = "missing"
	factories := localFactories()
	available := DetectAvailableTools(factories)
	if len(available) != 2 {
		t.Fatalf("API-only tools not detected: %v", available)
	}
	list := BuildModelList(context.Background(), available, factories, s)
	byID := make(map[string]ModelInfo, len(list.Data))
	for _, model := range list.Data {
		byID[model.ID] = model
	}
	if byID["ollama"].Available != nil || byID["lmstudio"].Available != nil {
		t.Fatal("bare entries must omit availability")
	}
	if got := byID["ollama:configured"]; got.Available == nil || !*got.Available || !got.Default {
		t.Fatalf("configured ollama model = %+v", got)
	}
	if got := byID["ollama:other"]; got.Available == nil || !*got.Available || got.Default {
		t.Fatalf("discovered ollama model = %+v", got)
	}
	if got := byID["lmstudio:missing"]; got.Available == nil || *got.Available || !got.Default {
		t.Fatalf("missing LM Studio default = %+v", got)
	}
	if efforts := byID["ollama:configured"].Efforts; len(efforts) != 5 || efforts[4] != "max" {
		t.Fatalf("ollama efforts = %v", efforts)
	}
}

func TestLocalAIModelInventoryProbesRunConcurrentlyWithoutFabricatedModels(t *testing.T) {
	chassis.RequireMajor(11)
	started := make(chan string, 2)
	newBlockingBackend := func() *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			started <- r.URL.Path
			<-r.Context().Done()
		}))
	}
	ollamaBackend := newBlockingBackend()
	defer ollamaBackend.Close()
	lmBackend := newBlockingBackend()
	defer lmBackend.Close()
	s := settings.GetDefaultSettings()
	s.Defaults.Ollama.BaseURL = ollamaBackend.URL
	s.Defaults.LMStudio.BaseURL = lmBackend.URL
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan ModelList, 1)
	go func() { done <- BuildModelList(ctx, []string{"ollama", "lmstudio"}, localFactories(), s) }()

	paths := map[string]bool{}
	for len(paths) < 2 {
		select {
		case path := <-started:
			paths[path] = true
		case <-time.After(time.Second):
			cancel()
			t.Fatal("local inventory probes did not start concurrently")
		}
	}
	cancel()
	list := <-done
	if !paths["/api/tags"] || !paths["/v1/models"] {
		t.Fatalf("probe paths = %v", paths)
	}
	for _, model := range list.Data {
		if strings.Contains(model.ID, ":") {
			t.Fatalf("fabricated local model entry = %+v", model)
		}
		if model.Available != nil {
			t.Fatalf("bare entry unexpectedly has availability = %+v", model)
		}
	}
}
