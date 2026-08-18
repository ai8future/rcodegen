package openai

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"rcodegen/pkg/runner"
	"rcodegen/pkg/server"
	"rcodegen/pkg/tools/claude"
	"rcodegen/pkg/tools/codex"
)

func claudeCodexFactories() map[string]server.ToolFactory {
	return map[string]server.ToolFactory{
		"claude": func() runner.Tool { return claude.New() },
		"codex":  func() runner.Tool { return codex.New() },
	}
}

func TestBuildModelList_EnumeratesToolModels(t *testing.T) {
	ml := BuildModelList([]string{"claude", "codex"}, claudeCodexFactories())

	ids := make(map[string]ModelInfo, len(ml.Data))
	for _, m := range ml.Data {
		ids[m.ID] = m
	}

	// Bare tool entries plus every tool:model combination.
	for _, want := range []string{"claude", "codex", "claude:opus", "claude:fable", "claude:sonnet", "claude:haiku", "codex:gpt-5.5"} {
		if _, ok := ids[want]; !ok {
			t.Errorf("model list missing %q; got %d entries", want, len(ml.Data))
		}
	}

	// Defaults flagged.
	if !ids["claude:opus"].Default {
		t.Error("claude:opus should be flagged default")
	}
	if ids["claude:haiku"].Default {
		t.Error("claude:haiku should not be flagged default")
	}
	if !ids["codex:gpt-5.5"].Default {
		t.Error("codex:gpt-5.5 should be flagged default")
	}

	// The guessing failure from the field: luna must not exist.
	if _, ok := ids["codex:luna"]; ok {
		t.Error("codex:luna must not be in the model list")
	}
}

func TestHandleModels_WithFactories(t *testing.T) {
	h := NewHandler(nil, claudeCodexFactories(), server.NewRunRegistry(2), []string{"claude", "codex"}, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var ml ModelList
	if err := json.NewDecoder(rec.Body).Decode(&ml); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// 2 bare tools + 4 claude models + 6 codex models.
	if len(ml.Data) != 12 {
		t.Errorf("expected 12 entries, got %d", len(ml.Data))
	}
}

func TestChatCompletions_InvalidModelRejected(t *testing.T) {
	h := NewHandler(nil, claudeCodexFactories(), server.NewRunRegistry(2), []string{"claude", "codex"}, nil, nil)

	body := `{"model":"codex:luna","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid model, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Valid options") || !strings.Contains(rec.Body.String(), "gpt-5.5") {
		t.Errorf("error should list valid options, got: %s", rec.Body.String())
	}
}

func TestChatCompletions_ValidModelPassesValidation(t *testing.T) {
	// A valid model must get past validation. It will then block on registry
	// Acquire and actually run the tool — so instead of running, verify only
	// that validation itself accepts every advertised model.
	for _, tc := range []struct{ toolName, model string }{
		{"claude", "opus"}, {"claude", "fable"}, {"codex", "gpt-5.5"},
	} {
		factory := claudeCodexFactories()[tc.toolName]
		if err := runner.ValidateModel(factory(), tc.model); err != nil {
			t.Errorf("ValidateModel(%s, %s) = %v, want nil", tc.toolName, tc.model, err)
		}
	}
}
