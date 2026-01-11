package runner

import (
	"bytes"
	"strings"
	"testing"
)

func TestStreamParser_ProcessLine_Empty(t *testing.T) {
	var buf bytes.Buffer
	p := NewStreamParser(&buf)

	p.ProcessLine("")
	p.ProcessLine("   ")

	if buf.Len() != 0 {
		t.Errorf("expected no output for empty lines, got %q", buf.String())
	}
}

func TestStreamParser_ProcessLine_InvalidJSON(t *testing.T) {
	var buf bytes.Buffer
	p := NewStreamParser(&buf)

	p.ProcessLine("not json at all")

	output := buf.String()
	if !strings.Contains(output, "not json at all") {
		t.Errorf("expected invalid JSON to pass through, got %q", output)
	}
}

func TestStreamParser_ProcessLine_SystemInit(t *testing.T) {
	var buf bytes.Buffer
	p := NewStreamParser(&buf)

	p.ProcessLine(`{"type":"system","subtype":"init"}`)

	output := buf.String()
	if !strings.Contains(output, "initialized") {
		t.Errorf("expected initialization message, got %q", output)
	}

	// Second init should not print again
	buf.Reset()
	p.ProcessLine(`{"type":"system","subtype":"init"}`)
	if buf.Len() != 0 {
		t.Errorf("expected no output for second init, got %q", buf.String())
	}
}

func TestStreamParser_ProcessLine_AssistantText(t *testing.T) {
	var buf bytes.Buffer
	p := NewStreamParser(&buf)

	p.ProcessLine(`{"type":"assistant","message":{"content":[{"type":"text","text":"Hello world"}]}}`)

	output := buf.String()
	if !strings.Contains(output, "Hello world") {
		t.Errorf("expected assistant text in output, got %q", output)
	}
}

func TestStreamParser_ProcessLine_Result(t *testing.T) {
	var buf bytes.Buffer
	p := NewStreamParser(&buf)

	p.ProcessLine(`{"type":"result","usage":{"input_tokens":100,"output_tokens":50},"total_cost_usd":0.0025}`)

	if p.Usage == nil {
		t.Fatal("expected usage to be captured")
	}
	if p.Usage.InputTokens != 100 {
		t.Errorf("expected input_tokens=100, got %d", p.Usage.InputTokens)
	}
	if p.Usage.OutputTokens != 50 {
		t.Errorf("expected output_tokens=50, got %d", p.Usage.OutputTokens)
	}
	if p.TotalCostUSD != 0.0025 {
		t.Errorf("expected total_cost_usd=0.0025, got %f", p.TotalCostUSD)
	}
}

func TestStreamParser_ProcessLine_ResultError(t *testing.T) {
	var buf bytes.Buffer
	p := NewStreamParser(&buf)

	p.ProcessLine(`{"type":"result","is_error":true}`)

	output := buf.String()
	if !strings.Contains(output, "failed") {
		t.Errorf("expected error message, got %q", output)
	}
}

func TestStreamParser_ProcessLine_ToolUse(t *testing.T) {
	var buf bytes.Buffer
	p := NewStreamParser(&buf)

	p.ProcessLine(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/foo/bar.go"}}]}}`)

	output := buf.String()
	if !strings.Contains(output, "Reading") {
		t.Errorf("expected 'Reading file' in output, got %q", output)
	}
	if !strings.Contains(output, "bar.go") {
		t.Errorf("expected file path in output, got %q", output)
	}
}

func TestExtractToolInfo(t *testing.T) {
	tests := []struct {
		name     string
		toolName string
		input    map[string]interface{}
		expected string
	}{
		{
			name:     "Read file path",
			toolName: "Read",
			input:    map[string]interface{}{"file_path": "/home/user/code/main.go"},
			expected: "/home/user/code/main.go",
		},
		{
			name:     "Bash command short",
			toolName: "Bash",
			input:    map[string]interface{}{"command": "ls -la"},
			expected: "ls -la",
		},
		{
			name:     "Bash command long",
			toolName: "Bash",
			input:    map[string]interface{}{"command": strings.Repeat("x", 100)},
			expected: strings.Repeat("x", 57) + "...",
		},
		{
			name:     "Glob pattern",
			toolName: "Glob",
			input:    map[string]interface{}{"pattern": "**/*.go"},
			expected: "**/*.go",
		},
		{
			name:     "TodoWrite items",
			toolName: "TodoWrite",
			input:    map[string]interface{}{"todos": []interface{}{1, 2, 3}},
			expected: "3 items",
		},
		{
			name:     "Unknown tool",
			toolName: "Unknown",
			input:    map[string]interface{}{"foo": "bar"},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractToolInfo(tt.toolName, tt.input)
			if result != tt.expected {
				t.Errorf("extractToolInfo(%q, %v) = %q, want %q", tt.toolName, tt.input, result, tt.expected)
			}
		})
	}
}
