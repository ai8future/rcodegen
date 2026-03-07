package batch

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteSummary(t *testing.T) {
	dir := t.TempDir()

	result := &BatchResult{
		Name:          "test-batch",
		Status:        "completed",
		JobsTotal:     5,
		JobsSucceeded: 4,
		JobsFailed:    1,
		TotalCost:     1.23,
		TotalDuration: "2m30s",
		StopReason:    "",
	}

	if err := WriteSummary(dir, result); err != nil {
		t.Fatalf("WriteSummary returned error: %v", err)
	}

	path := filepath.Join(dir, "summary.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("summary.json not found: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("summary.json is empty")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading summary.json: %v", err)
	}
	// Quick sanity check on content
	if got := string(data); !reporterContains(got, `"name": "test-batch"`) {
		t.Errorf("summary.json missing expected name field; got:\n%s", got)
	}
}

func TestWriteJobResult(t *testing.T) {
	dir := t.TempDir()

	result := &JobResult{
		ExitCode:  0,
		Cost:      0.42,
		Duration:  "1m15s",
		SessionID: "sess-abc123",
		Error:     "",
	}

	jobName := "fix-readme"
	if err := WriteJobResult(dir, jobName, result); err != nil {
		t.Fatalf("WriteJobResult returned error: %v", err)
	}

	path := filepath.Join(dir, "results", jobName+".json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("job result file not found: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("job result file is empty")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading job result file: %v", err)
	}
	if got := string(data); !reporterContains(got, `"session_id": "sess-abc123"`) {
		t.Errorf("job result file missing expected session_id; got:\n%s", got)
	}
}

func TestWriteSummaryCreatesDir(t *testing.T) {
	base := t.TempDir()
	nested := filepath.Join(base, "nested", "output")

	result := &BatchResult{
		Name:   "nested-test",
		Status: "completed",
	}

	if err := WriteSummary(nested, result); err != nil {
		t.Fatalf("WriteSummary with nested dir returned error: %v", err)
	}

	if _, err := os.Stat(filepath.Join(nested, "summary.json")); err != nil {
		t.Fatalf("summary.json not created in nested dir: %v", err)
	}
}

func TestProgressBar(t *testing.T) {
	tests := []struct {
		pct, width int
		want       string
	}{
		{0, 10, "[..........]"},
		{50, 10, "[=====.....]"},
		{100, 10, "[==========]"},
		{-5, 10, "[..........]"},
		{200, 10, "[==========]"},
	}
	for _, tc := range tests {
		got := progressBar(tc.pct, tc.width)
		if got != tc.want {
			t.Errorf("progressBar(%d, %d) = %q; want %q", tc.pct, tc.width, got, tc.want)
		}
	}
}

func TestColorizeStatus(t *testing.T) {
	// Just verify the function runs without panic and returns non-empty.
	statuses := []string{"completed", "completed_with_failures", "stopped", "cancelled", "unknown"}
	for _, s := range statuses {
		got := colorizeStatus(s)
		if got == "" {
			t.Errorf("colorizeStatus(%q) returned empty string", s)
		}
	}
}

// reporterContains is a small helper to check substring presence.
func reporterContains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
