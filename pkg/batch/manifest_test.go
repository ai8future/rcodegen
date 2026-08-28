package batch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadManifest(t *testing.T) {
	// Full manifest with all fields specified
	manifest := map[string]any{
		"name":        "deploy-suite",
		"concurrency": 3,
		"budget": map[string]any{
			"threshold_pct":  80,
			"on_budget":      "wait",
			"check_interval": "5m",
			"max_wait":       "30m",
		},
		"jobs": []map[string]any{
			{
				"name":       "lint",
				"task":       "Run linting on the codebase",
				"tool":       "claude",
				"dir":        "/tmp/project",
				"session":    "lint-session",
				"model":      "opus",
				"effort":     "high",
				"max_budget": "$5.00",
			},
			{
				"name": "test",
				"task": "Run all unit tests",
				"tool": "codex",
			},
			{
				"name": "docs",
				"task": "Generate documentation",
				"tool": "gemini",
			},
			{
				"name": "deepinfra",
				"task": "Run opencode",
				"tool": "opencode",
			},
			{
				"name": "kilocode",
				"task": "Run kilocode",
				"tool": "kilocode",
			},
		},
	}

	path := writeManifestJSON(t, manifest)
	m, err := LoadManifest(path)
	if err != nil {
		t.Fatalf("LoadManifest() error = %v", err)
	}

	if m.Name != "deploy-suite" {
		t.Errorf("Name = %q, want %q", m.Name, "deploy-suite")
	}
	if m.Concurrency != 3 {
		t.Errorf("Concurrency = %d, want %d", m.Concurrency, 3)
	}
	if m.Budget.ThresholdPct != 80 {
		t.Errorf("Budget.ThresholdPct = %d, want %d", m.Budget.ThresholdPct, 80)
	}
	if m.Budget.OnBudget != "wait" {
		t.Errorf("Budget.OnBudget = %q, want %q", m.Budget.OnBudget, "wait")
	}
	if m.Budget.CheckInterval != "5m" {
		t.Errorf("Budget.CheckInterval = %q, want %q", m.Budget.CheckInterval, "5m")
	}
	if m.Budget.MaxWait != "30m" {
		t.Errorf("Budget.MaxWait = %q, want %q", m.Budget.MaxWait, "30m")
	}

	if len(m.Jobs) != 5 {
		t.Fatalf("len(Jobs) = %d, want 5", len(m.Jobs))
	}

	// Check first job has all fields
	j := m.Jobs[0]
	if j.Name != "lint" {
		t.Errorf("Jobs[0].Name = %q, want %q", j.Name, "lint")
	}
	if j.Task != "Run linting on the codebase" {
		t.Errorf("Jobs[0].Task = %q, want %q", j.Task, "Run linting on the codebase")
	}
	if j.Tool != "claude" {
		t.Errorf("Jobs[0].Tool = %q, want %q", j.Tool, "claude")
	}
	if j.Dir != "/tmp/project" {
		t.Errorf("Jobs[0].Dir = %q, want %q", j.Dir, "/tmp/project")
	}
	if j.Session != "lint-session" {
		t.Errorf("Jobs[0].Session = %q, want %q", j.Session, "lint-session")
	}
	if j.Model != "opus" {
		t.Errorf("Jobs[0].Model = %q, want %q", j.Model, "opus")
	}
	if j.Effort != "high" {
		t.Errorf("Jobs[0].Effort = %q, want %q", j.Effort, "high")
	}
	if j.MaxBudget != "$5.00" {
		t.Errorf("Jobs[0].MaxBudget = %q, want %q", j.MaxBudget, "$5.00")
	}

	// Check second job
	if m.Jobs[1].Name != "test" {
		t.Errorf("Jobs[1].Name = %q, want %q", m.Jobs[1].Name, "test")
	}
	if m.Jobs[1].Tool != "codex" {
		t.Errorf("Jobs[1].Tool = %q, want %q", m.Jobs[1].Tool, "codex")
	}

	// Check third job
	if m.Jobs[2].Tool != "gemini" {
		t.Errorf("Jobs[2].Tool = %q, want %q", m.Jobs[2].Tool, "gemini")
	}

	if m.Jobs[3].Tool != "opencode" {
		t.Errorf("Jobs[3].Tool = %q, want %q", m.Jobs[3].Tool, "opencode")
	}

	if m.Jobs[4].Tool != "kilocode" {
		t.Errorf("Jobs[4].Tool = %q, want %q", m.Jobs[4].Tool, "kilocode")
	}

	// Verify duration methods
	ci, err := m.Budget.CheckIntervalDuration()
	if err != nil {
		t.Fatalf("CheckIntervalDuration() error = %v", err)
	}
	if ci != 5*time.Minute {
		t.Errorf("CheckIntervalDuration() = %v, want %v", ci, 5*time.Minute)
	}

	mw, err := m.Budget.MaxWaitDuration()
	if err != nil {
		t.Fatalf("MaxWaitDuration() error = %v", err)
	}
	if mw != 30*time.Minute {
		t.Errorf("MaxWaitDuration() = %v, want %v", mw, 30*time.Minute)
	}
}

func TestLoadManifestAcceptsLocalRuntimeTools(t *testing.T) {
	for _, tool := range []string{"ollama", "lmstudio"} {
		path := writeManifestJSON(t, map[string]any{"name": "local", "jobs": []map[string]any{{"name": tool, "task": "hi", "tool": tool, "model": "model"}}})
		if _, err := LoadManifest(path); err != nil {
			t.Fatalf("%s manifest: %v", tool, err)
		}
	}
}

func TestLoadManifestRejectsUnsafeOrDuplicateNames(t *testing.T) {
	for _, manifest := range []map[string]any{
		{"name": "../escape", "jobs": []map[string]any{{"name": "job", "task": "hi"}}},
		{"name": "safe", "jobs": []map[string]any{{"name": "../job", "task": "hi"}}},
		{"name": "safe", "jobs": []map[string]any{{"name": "same", "task": "hi"}, {"name": "same", "task": "hi"}}},
	} {
		if _, err := LoadManifest(writeManifestJSON(t, manifest)); err == nil {
			t.Fatalf("unsafe manifest accepted: %+v", manifest)
		}
	}
}

func TestLoadManifest_KilocodeIsValidTool(t *testing.T) {
	manifest := map[string]any{
		"name": "smoke",
		"jobs": []map[string]any{
			{
				"name": "j1",
				"task": "echo",
				"tool": "kilocode",
				"dir":  "/tmp",
			},
		},
	}

	path := writeManifestJSON(t, manifest)
	m, err := LoadManifest(path)
	if err != nil {
		t.Fatalf("LoadManifest() error = %v", err)
	}
	if m.Jobs[0].Tool != "kilocode" {
		t.Errorf("Jobs[0].Tool = %q, want %q", m.Jobs[0].Tool, "kilocode")
	}
}

func TestLoadManifestDefaults(t *testing.T) {
	// Minimal manifest: only required field is jobs with task
	manifest := map[string]any{
		"jobs": []map[string]any{
			{
				"task": "Do something useful",
			},
		},
	}

	path := writeManifestJSON(t, manifest)
	m, err := LoadManifest(path)
	if err != nil {
		t.Fatalf("LoadManifest() error = %v", err)
	}

	// Default concurrency
	if m.Concurrency != 1 {
		t.Errorf("Concurrency = %d, want default 1", m.Concurrency)
	}

	// Default tool
	if m.Jobs[0].Tool != "claude" {
		t.Errorf("Jobs[0].Tool = %q, want default %q", m.Jobs[0].Tool, "claude")
	}

	// Auto-generated name
	if m.Jobs[0].Name != "job-1" {
		t.Errorf("Jobs[0].Name = %q, want auto-generated %q", m.Jobs[0].Name, "job-1")
	}

	// Default budget config
	if m.Budget.OnBudget != "stop" {
		t.Errorf("Budget.OnBudget = %q, want default %q", m.Budget.OnBudget, "stop")
	}
	if m.Budget.CheckInterval != "3m" {
		t.Errorf("Budget.CheckInterval = %q, want default %q", m.Budget.CheckInterval, "3m")
	}
	if m.Budget.MaxWait != "1h" {
		t.Errorf("Budget.MaxWait = %q, want default %q", m.Budget.MaxWait, "1h")
	}

	// Verify default durations
	ci, err := m.Budget.CheckIntervalDuration()
	if err != nil {
		t.Fatalf("CheckIntervalDuration() error = %v", err)
	}
	if ci != 3*time.Minute {
		t.Errorf("CheckIntervalDuration() = %v, want %v", ci, 3*time.Minute)
	}

	mw, err := m.Budget.MaxWaitDuration()
	if err != nil {
		t.Fatalf("MaxWaitDuration() error = %v", err)
	}
	if mw != 1*time.Hour {
		t.Errorf("MaxWaitDuration() = %v, want %v", mw, 1*time.Hour)
	}
}

func TestLoadManifestValidation(t *testing.T) {
	tests := []struct {
		name    string
		data    map[string]any
		wantErr string
	}{
		{
			name:    "empty jobs list",
			data:    map[string]any{"jobs": []map[string]any{}},
			wantErr: "must contain at least one job",
		},
		{
			name: "missing task",
			data: map[string]any{
				"jobs": []map[string]any{
					{"name": "broken", "tool": "claude"},
				},
			},
			wantErr: "task is required",
		},
		{
			name: "invalid on_budget",
			data: map[string]any{
				"budget": map[string]any{
					"on_budget": "panic",
				},
				"jobs": []map[string]any{
					{"task": "do stuff"},
				},
			},
			wantErr: "invalid on_budget",
		},
		{
			name: "invalid tool",
			data: map[string]any{
				"jobs": []map[string]any{
					{"task": "do stuff", "tool": "chatgpt"},
				},
			},
			wantErr: "invalid tool",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeManifestJSON(t, tt.data)
			_, err := LoadManifest(path)
			if err == nil {
				t.Fatalf("LoadManifest() expected error containing %q, got nil", tt.wantErr)
			}
			if got := err.Error(); !contains(got, tt.wantErr) {
				t.Errorf("error = %q, want it to contain %q", got, tt.wantErr)
			}
		})
	}
}

// writeManifestJSON writes data as JSON to a temp file and returns the path.
func writeManifestJSON(t *testing.T, data any) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if err := os.WriteFile(path, raw, 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

func contains(s, substr string) bool {
	return len(substr) > 0 && len(s) >= len(substr) && containsStr(s, substr)
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
