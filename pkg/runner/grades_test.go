package runner

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestExtractGradeFromReport_TOTAL_SCORE(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "report.md")
	content := "# Report\n\nSome analysis.\n\nTOTAL_SCORE: 85/100\n"
	if err := os.WriteFile(file, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	grade, err := ExtractGradeFromReport(file)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if grade != 85 {
		t.Errorf("expected 85, got %f", grade)
	}
}

func TestExtractGradeFromReport_OverallGrade(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "report.md")
	content := "# Code Review\n\nOverall Grade: 92/100\n"
	if err := os.WriteFile(file, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	grade, err := ExtractGradeFromReport(file)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if grade != 92 {
		t.Errorf("expected 92, got %f", grade)
	}
}

func TestExtractGradeFromReport_ScorePattern(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "report.md")
	content := "# Analysis\nscore: 65/100\n"
	if err := os.WriteFile(file, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	grade, err := ExtractGradeFromReport(file)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if grade != 65 {
		t.Errorf("expected 65, got %f", grade)
	}
}

func TestExtractGradeFromReport_ZeroGrade(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "report.md")
	content := "TOTAL_SCORE: 0/100\n"
	if err := os.WriteFile(file, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	grade, err := ExtractGradeFromReport(file)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if grade != 0 {
		t.Errorf("expected 0, got %f", grade)
	}
}

func TestExtractGradeFromReport_DecimalGrade(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "report.md")
	content := "TOTAL_SCORE: 87.5/100\n"
	if err := os.WriteFile(file, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	grade, err := ExtractGradeFromReport(file)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if grade != 87.5 {
		t.Errorf("expected 87.5, got %f", grade)
	}
}

func TestExtractGradeFromReport_NoGrade(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "report.md")
	content := "# Report\nThis report has no grade or score.\n"
	if err := os.WriteFile(file, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := ExtractGradeFromReport(file)
	if err == nil {
		t.Error("expected error for report without grade")
	}
}

func TestExtractGradeFromReport_FileNotFound(t *testing.T) {
	_, err := ExtractGradeFromReport("/nonexistent/path/report.md")
	if err == nil {
		t.Error("expected error for nonexistent file")
	}
}

func TestParseReportFilename_NewFormat(t *testing.T) {
	tests := []struct {
		filename     string
		wantTool     string
		wantCodebase string
		wantTask     string
		wantYear     int
		wantMonth    time.Month
		wantDay      int
	}{
		{
			filename:     "rcodegen-claude-audit-2026-01-20_2204.md",
			wantTool:     "claude",
			wantCodebase: "rcodegen",
			wantTask:     "audit",
			wantYear:     2026,
			wantMonth:    time.January,
			wantDay:      20,
		},
		{
			filename:     "dispatch-gemini-test-2026-01-15_1430.md",
			wantTool:     "gemini",
			wantCodebase: "dispatch",
			wantTask:     "test",
			wantYear:     2026,
			wantMonth:    time.January,
			wantDay:      15,
		},
		{
			filename:     "myapp-codex-fix-2025-12-31_2359.md",
			wantTool:     "codex",
			wantCodebase: "myapp",
			wantTask:     "fix",
			wantYear:     2025,
			wantMonth:    time.December,
			wantDay:      31,
		},
		{
			filename:     "myapp-kilocode-audit-2026-04-28_1200.md",
			wantTool:     "kilocode",
			wantCodebase: "myapp",
			wantTask:     "audit",
			wantYear:     2026,
			wantMonth:    time.April,
			wantDay:      28,
		},
	}

	for _, tc := range tests {
		t.Run(tc.filename, func(t *testing.T) {
			tool, codebase, task, date, err := ParseReportFilename(tc.filename)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tool != tc.wantTool {
				t.Errorf("tool = %q, want %q", tool, tc.wantTool)
			}
			if codebase != tc.wantCodebase {
				t.Errorf("codebase = %q, want %q", codebase, tc.wantCodebase)
			}
			if task != tc.wantTask {
				t.Errorf("task = %q, want %q", task, tc.wantTask)
			}
			if date.Year() != tc.wantYear {
				t.Errorf("year = %d, want %d", date.Year(), tc.wantYear)
			}
			if date.Month() != tc.wantMonth {
				t.Errorf("month = %v, want %v", date.Month(), tc.wantMonth)
			}
			if date.Day() != tc.wantDay {
				t.Errorf("day = %d, want %d", date.Day(), tc.wantDay)
			}
		})
	}
}

func TestParseReportFilename_OldFormat(t *testing.T) {
	tool, codebase, task, _, err := ParseReportFilename("claude-dispatch-audit-2026-01-16_2331.md")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tool != "claude" {
		t.Errorf("tool = %q, want 'claude'", tool)
	}
	if codebase != "dispatch" {
		t.Errorf("codebase = %q, want 'dispatch'", codebase)
	}
	if task != "audit" {
		t.Errorf("task = %q, want 'audit'", task)
	}
}

func TestParseReportFilename_InvalidFormat(t *testing.T) {
	invalids := []string{
		"invalid.md",
		"no-date.md",
		"one-two.md",
		"readme.md",
		"",
	}

	for _, fn := range invalids {
		t.Run(fn, func(t *testing.T) {
			_, _, _, _, err := ParseReportFilename(fn)
			if err == nil {
				t.Errorf("expected error for invalid filename %q", fn)
			}
		})
	}
}

func TestParseFlexibleDate(t *testing.T) {
	tests := []struct {
		input    string
		wantYear int
		wantErr  bool
	}{
		{"2026-01-20_150405", 2026, false},
		{"2026-01-20_1504", 2026, false},
		{"20260120-150405", 2026, false},
		{"20260120-1504", 2026, false},
		{"2026-01-20", 2026, false},
		{"20260120", 2026, false},
		{"invalid", 0, true},
		{"2026/01/20", 0, true},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			date, err := parseFlexibleDate(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Errorf("expected error for %q", tc.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if date.Year() != tc.wantYear {
				t.Errorf("year = %d, want %d", date.Year(), tc.wantYear)
			}
		})
	}
}

func TestLoadGrades_NonexistentFile(t *testing.T) {
	dir := t.TempDir()

	grades, err := LoadGrades(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(grades.Grades) != 0 {
		t.Errorf("expected empty grades, got %d entries", len(grades.Grades))
	}
}

func TestLoadGrades_ValidFile(t *testing.T) {
	dir := t.TempDir()
	content := `{"grades":[{"date":"2026-01-20T10:00:00Z","tool":"claude","task":"audit","grade":85,"reportFile":"test.md"}]}`
	if err := os.WriteFile(filepath.Join(dir, ".grades.json"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	grades, err := LoadGrades(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(grades.Grades) != 1 {
		t.Fatalf("expected 1 grade, got %d", len(grades.Grades))
	}
	if grades.Grades[0].Grade != 85 {
		t.Errorf("expected grade 85, got %f", grades.Grades[0].Grade)
	}
	if grades.Grades[0].Tool != "claude" {
		t.Errorf("expected tool 'claude', got %q", grades.Grades[0].Tool)
	}
}

func TestLoadGrades_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".grades.json"), []byte("invalid json"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadGrades(dir)
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestLoadGrades_NullGradesArray(t *testing.T) {
	dir := t.TempDir()
	content := `{"grades":null}`
	if err := os.WriteFile(filepath.Join(dir, ".grades.json"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	grades, err := LoadGrades(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should normalize null to empty slice
	if grades.Grades == nil {
		t.Error("expected Grades to be non-nil (normalized from null)")
	}
}

func TestSaveGrades_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	original := &GradesFile{
		Grades: []GradeEntry{
			{Date: "2026-01-20T10:00:00Z", Tool: "claude", Task: "audit", Grade: 90, ReportFile: "report.md"},
			{Date: "2026-01-21T12:00:00Z", Tool: "gemini", Task: "test", Grade: 75, ReportFile: "test.md"},
		},
	}

	if err := SaveGrades(dir, original); err != nil {
		t.Fatalf("SaveGrades failed: %v", err)
	}

	loaded, err := LoadGrades(dir)
	if err != nil {
		t.Fatalf("LoadGrades failed: %v", err)
	}

	if len(loaded.Grades) != 2 {
		t.Fatalf("expected 2 grades, got %d", len(loaded.Grades))
	}
	if loaded.Grades[0].Grade != 90 {
		t.Errorf("first grade = %f, want 90", loaded.Grades[0].Grade)
	}
	if loaded.Grades[1].Tool != "gemini" {
		t.Errorf("second tool = %q, want 'gemini'", loaded.Grades[1].Tool)
	}
}

func TestAppendGrade_NewEntry(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()

	err := AppendGrade(dir, "report1.md", "Claude", "Audit", 85, now)
	if err != nil {
		t.Fatalf("AppendGrade failed: %v", err)
	}

	grades, err := LoadGrades(dir)
	if err != nil {
		t.Fatalf("LoadGrades failed: %v", err)
	}

	if len(grades.Grades) != 1 {
		t.Fatalf("expected 1 grade, got %d", len(grades.Grades))
	}
	// Should normalize to lowercase
	if grades.Grades[0].Tool != "claude" {
		t.Errorf("tool = %q, want 'claude' (lowercase)", grades.Grades[0].Tool)
	}
	if grades.Grades[0].Task != "audit" {
		t.Errorf("task = %q, want 'audit' (lowercase)", grades.Grades[0].Task)
	}
}

func TestAppendGrade_DuplicateSkipped(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()

	// First append
	if err := AppendGrade(dir, "report1.md", "claude", "audit", 85, now); err != nil {
		t.Fatalf("first AppendGrade failed: %v", err)
	}

	// Duplicate append (same reportFile)
	if err := AppendGrade(dir, "report1.md", "claude", "audit", 90, now); err != nil {
		t.Fatalf("duplicate AppendGrade failed: %v", err)
	}

	grades, err := LoadGrades(dir)
	if err != nil {
		t.Fatalf("LoadGrades failed: %v", err)
	}

	if len(grades.Grades) != 1 {
		t.Errorf("expected 1 grade (duplicate skipped), got %d", len(grades.Grades))
	}
	// Should keep original grade, not the duplicate
	if grades.Grades[0].Grade != 85 {
		t.Errorf("expected original grade 85, got %f", grades.Grades[0].Grade)
	}
}

func TestAppendGrade_MultipleEntries(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()

	AppendGrade(dir, "report1.md", "claude", "audit", 85, now)
	AppendGrade(dir, "report2.md", "gemini", "test", 70, now)
	AppendGrade(dir, "report3.md", "codex", "fix", 95, now)

	grades, err := LoadGrades(dir)
	if err != nil {
		t.Fatalf("LoadGrades failed: %v", err)
	}

	if len(grades.Grades) != 3 {
		t.Errorf("expected 3 grades, got %d", len(grades.Grades))
	}
}

func TestEscapeGlobPattern(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"normal", "normal"},
		{"file*.md", "file\\*.md"},
		{"test?", "test\\?"},
		{"[abc]", "\\[abc\\]"},
		{"a*b?c[d]", "a\\*b\\?c\\[d\\]"},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			result := escapeGlobPattern(tc.input)
			if result != tc.expected {
				t.Errorf("escapeGlobPattern(%q) = %q, want %q", tc.input, result, tc.expected)
			}
		})
	}
}

func TestFindNewestReport_NoMatches(t *testing.T) {
	dir := t.TempDir()

	_, err := FindNewestReport(dir, "claude", "audit")
	if err == nil {
		t.Error("expected error when no reports match")
	}
}

func TestFindNewestReport_FindsNewest(t *testing.T) {
	dir := t.TempDir()

	old := filepath.Join(dir, "myapp-claude-audit-2026-01-25.md")
	newest := filepath.Join(dir, "myapp-claude-audit-2026-01-28.md")

	os.WriteFile(old, []byte("old"), 0644)
	os.WriteFile(newest, []byte("newest"), 0644)

	now := time.Now()
	os.Chtimes(old, now.Add(-1*time.Hour), now.Add(-1*time.Hour))
	os.Chtimes(newest, now, now)

	result, err := FindNewestReport(dir, "claude", "audit")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != newest {
		t.Errorf("FindNewestReport = %q, want %q", result, newest)
	}
}
