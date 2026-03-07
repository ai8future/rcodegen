package orchestrator

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"rcodegen/pkg/envelope"
)

func TestNewContext(t *testing.T) {
	inputs := map[string]string{"foo": "bar", "baz": "qux"}
	ctx := NewContext(context.Background(), inputs)

	if ctx.Inputs["foo"] != "bar" {
		t.Errorf("expected input 'foo' = 'bar', got %q", ctx.Inputs["foo"])
	}
	if ctx.StepResults == nil {
		t.Error("expected StepResults to be initialized")
	}
	if ctx.Variables == nil {
		t.Error("expected Variables to be initialized")
	}
	if ctx.ToolSessions == nil {
		t.Error("expected ToolSessions to be initialized")
	}
}

func TestNewContext_NilInputs(t *testing.T) {
	ctx := NewContext(context.Background(), nil)
	if ctx.Inputs != nil {
		t.Error("expected nil inputs to stay nil")
	}
	if ctx.StepResults == nil {
		t.Error("expected StepResults to be initialized even with nil inputs")
	}
}

func TestContext_Resolve_Inputs(t *testing.T) {
	ctx := NewContext(context.Background(), map[string]string{
		"name":    "test-project",
		"version": "1.0.0",
	})

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"single input", "${inputs.name}", "test-project"},
		{"multiple inputs", "Project: ${inputs.name} v${inputs.version}", "Project: test-project v1.0.0"},
		{"missing input", "${inputs.missing}", "${inputs.missing}"},
		{"no variables", "no variables here", "no variables here"},
		{"empty string", "", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := ctx.Resolve(tc.input)
			if result != tc.expected {
				t.Errorf("Resolve(%q) = %q, want %q", tc.input, result, tc.expected)
			}
		})
	}
}

func TestContext_Resolve_StepResults(t *testing.T) {
	ctx := NewContext(context.Background(), nil)

	ctx.SetResult("analyze", &envelope.Envelope{
		Status:    envelope.StatusSuccess,
		OutputRef: "/tmp/analyze-output.json",
		Result: map[string]interface{}{
			"count": 42,
			"items": "a,b,c",
		},
	})

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"step status", "${steps.analyze.status}", "success"},
		{"step output_ref", "${steps.analyze.output_ref}", "/tmp/analyze-output.json"},
		{"step result field", "${steps.analyze.result.count}", "42"},
		{"step result string", "${steps.analyze.result.items}", "a,b,c"},
		{"missing step", "${steps.missing.status}", "${steps.missing.status}"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := ctx.Resolve(tc.input)
			if result != tc.expected {
				t.Errorf("Resolve(%q) = %q, want %q", tc.input, result, tc.expected)
			}
		})
	}
}

func TestContext_Resolve_StdoutStderr(t *testing.T) {
	tmpDir := t.TempDir()
	outputFile := filepath.Join(tmpDir, "output.json")

	// Use actual newlines in the streaming output
	content := map[string]interface{}{
		"stdout": "{\"type\":\"system\"}\n{\"type\":\"result\",\"result\":\"hello stdout\"}",
		"stderr": "error message",
	}
	data, _ := json.Marshal(content)
	if err := os.WriteFile(outputFile, data, 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	ctx := NewContext(context.Background(), nil)
	ctx.SetResult("build", &envelope.Envelope{
		Status:    envelope.StatusSuccess,
		OutputRef: outputFile,
	})

	// Test stdout resolution with streaming result extraction
	stdout := ctx.Resolve("${steps.build.stdout}")
	if stdout != "hello stdout" {
		t.Errorf("Resolve stdout = %q, want 'hello stdout'", stdout)
	}

	// Test stderr resolution
	stderr := ctx.Resolve("${steps.build.stderr}")
	if stderr != "error message" {
		t.Errorf("Resolve stderr = %q, want 'error message'", stderr)
	}
}

func TestContext_Resolve_FullResultJSON(t *testing.T) {
	ctx := NewContext(context.Background(), nil)
	ctx.SetResult("step1", &envelope.Envelope{
		Status: envelope.StatusSuccess,
		Result: map[string]interface{}{
			"answer": "42",
			"found":  true,
		},
	})

	// When requesting just ${steps.step1.result}, should get JSON
	result := ctx.Resolve("${steps.step1.result}")

	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("result should be valid JSON: %v", err)
	}
	if parsed["answer"] != "42" {
		t.Errorf("expected answer=42, got %v", parsed["answer"])
	}
}

func TestContext_ToolSession(t *testing.T) {
	ctx := NewContext(context.Background(), nil)

	// Initially empty
	if s := ctx.GetToolSession("claude"); s != "" {
		t.Errorf("expected empty session, got %q", s)
	}

	// Set and get
	ctx.SetToolSession("claude", "session-123")
	if s := ctx.GetToolSession("claude"); s != "session-123" {
		t.Errorf("expected 'session-123', got %q", s)
	}

	// Different tool
	if s := ctx.GetToolSession("gemini"); s != "" {
		t.Errorf("expected empty session for different tool, got %q", s)
	}
}

func TestContext_SetResult_GetResult(t *testing.T) {
	ctx := NewContext(context.Background(), nil)

	env := &envelope.Envelope{Status: envelope.StatusSuccess}
	ctx.SetResult("step1", env)

	// GetResult should return the envelope
	got, ok := ctx.GetResult("step1")
	if !ok {
		t.Error("expected GetResult to return ok=true")
	}
	if got != env {
		t.Error("expected same envelope instance")
	}

	// Missing step
	_, ok = ctx.GetResult("missing")
	if ok {
		t.Error("expected ok=false for missing step")
	}
}

func TestContext_ThreadSafety_ToolSession(t *testing.T) {
	ctx := NewContext(context.Background(), nil)
	var wg sync.WaitGroup

	// Concurrent writes
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			ctx.SetToolSession("claude", "session-"+string(rune('A'+n%26)))
		}(i)
	}

	// Concurrent reads
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = ctx.GetToolSession("claude")
		}()
	}

	wg.Wait()
	// If we get here without deadlock/race, test passes
}

func TestContext_ThreadSafety_SetResult(t *testing.T) {
	ctx := NewContext(context.Background(), nil)
	var wg sync.WaitGroup

	for i := 0; i < 50; i++ {
		wg.Add(2)
		stepName := "step" + string(rune('A'+i%26))

		go func(name string) {
			defer wg.Done()
			ctx.SetResult(name, &envelope.Envelope{Status: envelope.StatusSuccess})
		}(stepName)

		go func() {
			defer wg.Done()
			_ = ctx.Resolve("${steps.stepA.status}")
		}()
	}

	wg.Wait()
	// If we get here without race condition, test passes
}

func TestExtractStreamingResult(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "single result object",
			input:    `{"type":"result","result":"Final answer here"}`,
			expected: "Final answer here",
		},
		{
			name:     "streaming with result at end",
			input:    "{\"type\":\"assistant\",\"text\":\"thinking...\"}\n{\"type\":\"result\",\"result\":\"Done!\"}",
			expected: "Done!",
		},
		{
			name:     "no result object",
			input:    "Plain text output",
			expected: "Plain text output",
		},
		{
			name:     "empty input",
			input:    "",
			expected: "",
		},
		{
			name:     "result without result field",
			input:    `{"type":"result","status":"ok"}`,
			expected: `{"type":"result","status":"ok"}`,
		},
		{
			name:     "multiple lines no result",
			input:    "{\"type\":\"assistant\"}\n{\"type\":\"tool_use\"}",
			expected: "{\"type\":\"assistant\"}\n{\"type\":\"tool_use\"}",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := extractStreamingResult(tc.input)
			if result != tc.expected {
				t.Errorf("extractStreamingResult() = %q, want %q", result, tc.expected)
			}
		})
	}
}
