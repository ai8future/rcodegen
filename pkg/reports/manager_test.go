package reports

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFindNewestReport_EmptyList(t *testing.T) {
	result := FindNewestReport([]string{})
	if result != "" {
		t.Errorf("FindNewestReport([]) = %q, want empty string", result)
	}
}

func TestFindNewestReport_SingleFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "report.md")
	if err := os.WriteFile(file, []byte("content"), 0644); err != nil {
		t.Fatal(err)
	}

	result := FindNewestReport([]string{file})
	if result != file {
		t.Errorf("FindNewestReport single file = %q, want %q", result, file)
	}
}

func TestFindNewestReport_MultipleFiles(t *testing.T) {
	dir := t.TempDir()

	old := filepath.Join(dir, "old.md")
	mid := filepath.Join(dir, "mid.md")
	newest := filepath.Join(dir, "newest.md")

	// Create all files
	for _, f := range []string{old, mid, newest} {
		if err := os.WriteFile(f, []byte("content"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	// Set modification times explicitly
	now := time.Now()
	os.Chtimes(old, now.Add(-2*time.Hour), now.Add(-2*time.Hour))
	os.Chtimes(mid, now.Add(-1*time.Hour), now.Add(-1*time.Hour))
	os.Chtimes(newest, now, now)

	result := FindNewestReport([]string{old, mid, newest})
	if result != newest {
		t.Errorf("FindNewestReport = %q, want %q", result, newest)
	}

	// Input order should not matter
	result = FindNewestReport([]string{newest, old, mid})
	if result != newest {
		t.Errorf("FindNewestReport (reverse order) = %q, want %q", result, newest)
	}
}

func TestFindNewestReport_SkipsNonexistent(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "exists.md")
	if err := os.WriteFile(existing, []byte("content"), 0644); err != nil {
		t.Fatal(err)
	}

	result := FindNewestReport([]string{"/nonexistent/missing.md", existing})
	if result != existing {
		t.Errorf("FindNewestReport with nonexistent = %q, want %q", result, existing)
	}
}

func TestIsReportReviewed_WithMarker(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "reviewed.md")
	content := "Date Created: 2026-01-28\nDate Modified: 2026-01-28\nTOTAL_SCORE: 85/100\n"
	if err := os.WriteFile(file, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	if !IsReportReviewed(file) {
		t.Error("expected true for file with Date Modified marker")
	}
}

func TestIsReportReviewed_WithoutMarker(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "unreviewed.md")
	content := "Date Created: 2026-01-28\nTOTAL_SCORE: 85/100\n# Report Content\n"
	if err := os.WriteFile(file, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	if IsReportReviewed(file) {
		t.Error("expected false for file without Date Modified marker")
	}
}

func TestIsReportReviewed_MarkerOnLine10(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "boundary.md")
	// Lines 1-9 are filler, line 10 has the marker (should be found since scan limit is 10)
	content := "1\n2\n3\n4\n5\n6\n7\n8\n9\nDate Modified: 2026-01-28"
	if err := os.WriteFile(file, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	if !IsReportReviewed(file) {
		t.Error("expected true for marker on line 10 (within scan limit)")
	}
}

func TestIsReportReviewed_MarkerBeyondScanLimit(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "beyond.md")
	// 10 filler lines, then the marker on line 11 (beyond scan limit)
	content := "1\n2\n3\n4\n5\n6\n7\n8\n9\n10\nDate Modified: 2026-01-28"
	if err := os.WriteFile(file, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	if IsReportReviewed(file) {
		t.Error("expected false for marker beyond scan limit")
	}
}

func TestIsReportReviewed_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "empty.md")
	if err := os.WriteFile(file, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}

	if IsReportReviewed(file) {
		t.Error("expected false for empty file")
	}
}

func TestIsReportReviewed_NonexistentFile(t *testing.T) {
	if IsReportReviewed("/nonexistent/path/report.md") {
		t.Error("expected false for nonexistent file")
	}
}

func TestShouldSkipTask_RequireReviewFalse(t *testing.T) {
	dir := t.TempDir()
	if ShouldSkipTask(dir, "test", "pattern-", false) {
		t.Error("expected false when requireReview is false")
	}
}

func TestShouldSkipTask_EmptyPattern(t *testing.T) {
	dir := t.TempDir()
	if ShouldSkipTask(dir, "test", "", true) {
		t.Error("expected false when pattern is empty")
	}
}

func TestShouldSkipTask_NonexistentDir(t *testing.T) {
	if ShouldSkipTask("/nonexistent/dir", "test", "pattern-", true) {
		t.Error("expected false when report directory does not exist")
	}
}

func TestShouldSkipTask_NoMatchingReports(t *testing.T) {
	dir := t.TempDir()
	if ShouldSkipTask(dir, "test", "nonexistent-pattern-", true) {
		t.Error("expected false when no reports match pattern")
	}
}

func TestShouldSkipTask_UnreviewedReport(t *testing.T) {
	dir := t.TempDir()
	report := filepath.Join(dir, "test-pattern-2026-01-28.md")
	content := "Date Created: 2026-01-28\nTOTAL_SCORE: 80/100\n# No Date Modified\n"
	if err := os.WriteFile(report, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	if !ShouldSkipTask(dir, "test", "test-pattern-", true) {
		t.Error("expected true (skip) when report is unreviewed")
	}
}

func TestShouldSkipTask_ReviewedReport(t *testing.T) {
	dir := t.TempDir()
	report := filepath.Join(dir, "test-pattern-2026-01-28.md")
	content := "Date Created: 2026-01-28\nDate Modified: 2026-01-28\nTOTAL_SCORE: 80/100\n"
	if err := os.WriteFile(report, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	if ShouldSkipTask(dir, "test", "test-pattern-", true) {
		t.Error("expected false (run) when report has been reviewed")
	}
}

func TestDeleteOldReports_NonexistentDir(t *testing.T) {
	// Should not panic
	patterns := map[string]string{"audit": "audit-"}
	DeleteOldReports("/nonexistent/dir", []string{"audit"}, patterns)
}

func TestDeleteOldReports_UnknownShortcut(t *testing.T) {
	dir := t.TempDir()
	report := filepath.Join(dir, "audit-2026-01-28.md")
	os.WriteFile(report, []byte("content"), 0644)

	patterns := map[string]string{"audit": "audit-"}
	DeleteOldReports(dir, []string{"unknown"}, patterns)

	// Report should still exist since shortcut didn't match
	if _, err := os.Stat(report); os.IsNotExist(err) {
		t.Error("report should not be deleted for unknown shortcut")
	}
}

func TestDeleteOldReports_SingleReport(t *testing.T) {
	dir := t.TempDir()
	report := filepath.Join(dir, "audit-2026-01-28.md")
	os.WriteFile(report, []byte("content"), 0644)

	patterns := map[string]string{"audit": "audit-"}
	DeleteOldReports(dir, []string{"audit"}, patterns)

	// Single report should NOT be deleted
	if _, err := os.Stat(report); os.IsNotExist(err) {
		t.Error("single report should not be deleted")
	}
}

func TestDeleteOldReports_MultipleReports(t *testing.T) {
	dir := t.TempDir()

	old := filepath.Join(dir, "audit-2026-01-26.md")
	mid := filepath.Join(dir, "audit-2026-01-27.md")
	newest := filepath.Join(dir, "audit-2026-01-28.md")

	// Create files with explicit modification times
	for _, f := range []string{old, mid, newest} {
		os.WriteFile(f, []byte("content"), 0644)
	}
	now := time.Now()
	os.Chtimes(old, now.Add(-2*time.Hour), now.Add(-2*time.Hour))
	os.Chtimes(mid, now.Add(-1*time.Hour), now.Add(-1*time.Hour))
	os.Chtimes(newest, now, now)

	patterns := map[string]string{"audit": "audit-"}
	DeleteOldReports(dir, []string{"audit"}, patterns)

	// Newest should remain
	if _, err := os.Stat(newest); os.IsNotExist(err) {
		t.Error("newest report should not be deleted")
	}

	// Older ones should be deleted
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("old report should be deleted")
	}
	if _, err := os.Stat(mid); !os.IsNotExist(err) {
		t.Error("middle report should be deleted")
	}
}
