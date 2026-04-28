package openai

import (
	"testing"
)

func TestParseModel(t *testing.T) {
	tests := []struct {
		input     string
		wantTool  string
		wantModel string
	}{
		{"claude", "claude", ""},
		{"claude:opus-4", "claude", "opus-4"},
		{"codex:o3-pro", "codex", "o3-pro"},
		{"gemini", "gemini", ""},
		{"gemini:2.5-flash", "gemini", "2.5-flash"},
		{"opencode:deepinfra/Qwen/Qwen3-Coder-480B-A35B-Instruct", "opencode", "deepinfra/Qwen/Qwen3-Coder-480B-A35B-Instruct"},
		{"kilocode:deepinfra/Qwen/Qwen3-Coder-480B-A35B-Instruct", "kilocode", "deepinfra/Qwen/Qwen3-Coder-480B-A35B-Instruct"},
		{"claude:sonnet-4:thinking", "claude", "sonnet-4:thinking"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			gotTool, gotModel := ParseModel(tt.input)
			if gotTool != tt.wantTool {
				t.Errorf("ParseModel(%q) tool = %q, want %q", tt.input, gotTool, tt.wantTool)
			}
			if gotModel != tt.wantModel {
				t.Errorf("ParseModel(%q) model = %q, want %q", tt.input, gotModel, tt.wantModel)
			}
		})
	}
}

func TestParseModelInvalid(t *testing.T) {
	gotTool, gotModel := ParseModel("")
	if gotTool != "" {
		t.Errorf("ParseModel(\"\") tool = %q, want \"\"", gotTool)
	}
	if gotModel != "" {
		t.Errorf("ParseModel(\"\") model = %q, want \"\"", gotModel)
	}
}

func TestExtractTaskPrompt(t *testing.T) {
	tests := []struct {
		name     string
		messages []Message
		want     string
	}{
		{
			name:     "user only",
			messages: []Message{{Role: "user", Content: "fix the bug"}},
			want:     "fix the bug",
		},
		{
			name: "system + user",
			messages: []Message{
				{Role: "system", Content: "You are a Go expert."},
				{Role: "user", Content: "fix the bug"},
			},
			want: "You are a Go expert.\n\nfix the bug",
		},
		{
			name: "multi-turn takes last user",
			messages: []Message{
				{Role: "system", Content: "Be concise."},
				{Role: "user", Content: "hello"},
				{Role: "assistant", Content: "hi"},
				{Role: "user", Content: "fix the bug"},
			},
			want: "Be concise.\n\nfix the bug",
		},
		{
			name: "multiple system messages",
			messages: []Message{
				{Role: "system", Content: "Rule 1."},
				{Role: "system", Content: "Rule 2."},
				{Role: "user", Content: "do it"},
			},
			want: "Rule 1.\nRule 2.\n\ndo it",
		},
		{
			name:     "no user message",
			messages: []Message{{Role: "system", Content: "hello"}},
			want:     "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractTaskPrompt(tt.messages)
			if got != tt.want {
				t.Errorf("ExtractTaskPrompt() = %q, want %q", got, tt.want)
			}
		})
	}
}
