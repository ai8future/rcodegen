package tracking

import (
	"testing"
)

func TestFormatCredit_Nil(t *testing.T) {
	result := FormatCredit(nil)
	if result != "N/A" {
		t.Errorf("FormatCredit(nil) = %q, want %q", result, "N/A")
	}
}

func TestFormatCredit_Values(t *testing.T) {
	tests := []struct {
		name     string
		input    int
		expected string
	}{
		{"zero", 0, "0"},
		{"positive", 42, "42"},
		{"hundred", 100, "100"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			val := tc.input
			result := FormatCredit(&val)
			if result != tc.expected {
				t.Errorf("FormatCredit(%d) = %q, want %q", tc.input, result, tc.expected)
			}
		})
	}
}

func TestClaudeStatus_IsITerm2Error(t *testing.T) {
	tests := []struct {
		name  string
		error string
		want  bool
	}{
		{"not_iterm2", "not_iterm2", true},
		{"no_iterm2_package", "no_iterm2_package", true},
		{"other error", "connection_failed", false},
		{"empty error", "", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			status := &ClaudeStatus{Error: tc.error}
			if got := status.IsITerm2Error(); got != tc.want {
				t.Errorf("IsITerm2Error() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestGeminiStatus_IsITerm2Error(t *testing.T) {
	tests := []struct {
		name  string
		error string
		want  bool
	}{
		{"not_iterm2", "not_iterm2", true},
		{"no_iterm2_package", "no_iterm2_package", true},
		{"other error", "timeout", false},
		{"empty error", "", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			status := &GeminiStatus{Error: tc.error}
			if got := status.IsITerm2Error(); got != tc.want {
				t.Errorf("IsITerm2Error() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestGeminiStatus_GetModelStatus(t *testing.T) {
	pct := 75.0
	status := &GeminiStatus{
		Models: []GeminiModelStatus{
			{Name: "gemini-2.0-flash", PctLeft: &pct},
			{Name: "gemini-2.5-pro", PctLeft: nil},
		},
	}

	t.Run("found", func(t *testing.T) {
		m := status.GetModelStatus("gemini-2.0-flash")
		if m == nil {
			t.Fatal("expected to find model")
		}
		if m.PctLeft == nil || *m.PctLeft != 75.0 {
			t.Errorf("expected PctLeft=75.0, got %v", m.PctLeft)
		}
	})

	t.Run("found nil pct", func(t *testing.T) {
		m := status.GetModelStatus("gemini-2.5-pro")
		if m == nil {
			t.Fatal("expected to find model")
		}
		if m.PctLeft != nil {
			t.Errorf("expected nil PctLeft, got %v", *m.PctLeft)
		}
	})

	t.Run("not found", func(t *testing.T) {
		m := status.GetModelStatus("nonexistent-model")
		if m != nil {
			t.Error("expected nil for nonexistent model")
		}
	})
}

func TestGeminiStatus_GetModelStatus_EmptyModels(t *testing.T) {
	status := &GeminiStatus{}
	if m := status.GetModelStatus("any"); m != nil {
		t.Error("expected nil for empty models list")
	}
}

func TestFormatResetsIn_NilInput(t *testing.T) {
	result := FormatResetsIn(nil)
	if result != "" {
		t.Errorf("FormatResetsIn(nil) = %q, want empty", result)
	}
}

func TestFormatResetsIn_EmptyString(t *testing.T) {
	empty := ""
	result := FormatResetsIn(&empty)
	if result != "" {
		t.Errorf("FormatResetsIn('') = %q, want empty", result)
	}
}

func TestFormatResetsIn_InvalidFormat(t *testing.T) {
	invalid := "not-a-date"
	result := FormatResetsIn(&invalid)
	if result != "" {
		t.Errorf("FormatResetsIn('not-a-date') = %q, want empty", result)
	}
}

func TestFormatResetsIn_PastTime(t *testing.T) {
	past := "2020-01-01T00:00:00Z"
	result := FormatResetsIn(&past)
	if result != "now" {
		t.Errorf("FormatResetsIn(past) = %q, want 'now'", result)
	}
}

func TestGetScriptDir_Succeeds(t *testing.T) {
	// This should return a non-empty string (the test binary's directory)
	dir := GetScriptDir()
	if dir == "" {
		t.Error("GetScriptDir() returned empty string")
	}
}
