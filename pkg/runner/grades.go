package runner

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// GradeEntry represents a single grade record
type GradeEntry struct {
	Date       string  `json:"date"`
	Tool       string  `json:"tool"`
	Task       string  `json:"task"`
	Grade      float64 `json:"grade"`
	ReportFile string  `json:"reportFile"`
}

// GradesFile represents the .grades.json file structure
type GradesFile struct {
	Grades []GradeEntry `json:"grades"`
}

// Grade extraction regex patterns (same as dashboard)
var gradePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)TOTAL_SCORE:\s*(\d+(?:\.\d+)?)\s*/\s*100`),
	regexp.MustCompile(`(?i)Overall Grade[^:]*:\s*(\d+(?:\.\d+)?)\s*/\s*100`),
	regexp.MustCompile(`(?i)Grade[^:]*:\s*(\d+(?:\.\d+)?)\s*/\s*100`),
	// More specific pattern to avoid matching things like "5/100 users"
	regexp.MustCompile(`(?i)(?:score|rating|points?)[:=]?\s*(\d+(?:\.\d+)?)\s*/\s*100`),
}

// Report filename pattern: {codebase}-{tool}-{task}-{YYYY-MM-DD_HHMM}.md
// Case-insensitive for tool and task
var reportFilenamePattern = regexp.MustCompile(`(?i)^(.+)-([a-z]+)-([a-z]+)-(\d{4}-\d{2}-\d{2}_\d{4})\.md$`)

// File lock for grades.json operations
var gradesFileMutex sync.Mutex

// ExtractGradeFromReport reads a report file and extracts the grade
func ExtractGradeFromReport(reportPath string) (float64, error) {
	content, err := os.ReadFile(reportPath)
	if err != nil {
		return 0, fmt.Errorf("failed to read report: %w", err)
	}

	text := string(content)
	for _, pattern := range gradePatterns {
		matches := pattern.FindStringSubmatch(text)
		if len(matches) >= 2 {
			var grade float64
			_, err := fmt.Sscanf(matches[1], "%f", &grade)
			// Allow grade of 0 (>= 0 instead of > 0)
			if err == nil && grade >= 0 && grade <= 100 {
				return grade, nil
			}
		}
	}

	return 0, fmt.Errorf("no grade found in report")
}

// ParseReportFilename extracts tool, codebase, task, and date from filename
func ParseReportFilename(filename string) (tool, codebase, task string, date time.Time, err error) {
	matches := reportFilenamePattern.FindStringSubmatch(filename)
	if len(matches) < 5 {
		return "", "", "", time.Time{}, fmt.Errorf("filename does not match expected pattern: %s", filename)
	}

	// Normalize to lowercase for consistency
	// Pattern: {codebase}-{tool}-{task}-{timestamp}.md
	codebase = matches[1]
	tool = strings.ToLower(matches[2])
	task = strings.ToLower(matches[3])
	dateStr := matches[4]

	// Parse date: 2026-01-16_2336 -> 2026-01-16T23:36:00Z
	date, err = time.Parse("2006-01-02_1504", dateStr)
	if err != nil {
		return "", "", "", time.Time{}, fmt.Errorf("failed to parse date from filename: %w", err)
	}

	return tool, codebase, task, date, nil
}

// LoadGrades reads the .grades.json file from a report directory
func LoadGrades(reportDir string) (*GradesFile, error) {
	gradesPath := filepath.Join(reportDir, ".grades.json")

	data, err := os.ReadFile(gradesPath)
	if err != nil {
		if os.IsNotExist(err) {
			// Return empty grades file if it doesn't exist
			return &GradesFile{Grades: []GradeEntry{}}, nil
		}
		return nil, fmt.Errorf("failed to read grades file: %w", err)
	}

	var grades GradesFile
	if err := json.Unmarshal(data, &grades); err != nil {
		return nil, fmt.Errorf("failed to parse grades file: %w", err)
	}

	// Validate the structure
	if grades.Grades == nil {
		grades.Grades = []GradeEntry{}
	}

	return &grades, nil
}

// SaveGrades writes the .grades.json file to a report directory atomically
func SaveGrades(reportDir string, grades *GradesFile) error {
	gradesPath := filepath.Join(reportDir, ".grades.json")
	tempPath := gradesPath + ".tmp"

	data, err := json.MarshalIndent(grades, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal grades: %w", err)
	}

	// Write to temp file first
	if err := os.WriteFile(tempPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write temp grades file: %w", err)
	}

	// Atomic rename
	if err := os.Rename(tempPath, gradesPath); err != nil {
		// Clean up temp file on failure
		os.Remove(tempPath)
		return fmt.Errorf("failed to rename grades file: %w", err)
	}

	return nil
}

// AppendGrade adds a new grade entry to .grades.json, avoiding duplicates
// Thread-safe with file locking
func AppendGrade(reportDir, reportFile, tool, task string, grade float64, date time.Time) error {
	// Lock to prevent race conditions
	gradesFileMutex.Lock()
	defer gradesFileMutex.Unlock()

	grades, err := LoadGrades(reportDir)
	if err != nil {
		return err
	}

	// Check for duplicate (same reportFile)
	for _, entry := range grades.Grades {
		if entry.ReportFile == reportFile {
			// Already exists, skip
			return nil
		}
	}

	// Append new entry with RFC3339 format for consistency
	grades.Grades = append(grades.Grades, GradeEntry{
		Date:       date.UTC().Format(time.RFC3339),
		Tool:       strings.ToLower(tool),
		Task:       strings.ToLower(task),
		Grade:      grade,
		ReportFile: reportFile,
	})

	return SaveGrades(reportDir, grades)
}

// escapeGlobPattern escapes special glob characters in a string
func escapeGlobPattern(s string) string {
	// Escape glob metacharacters: * ? [ ] \
	replacer := strings.NewReplacer(
		"*", "\\*",
		"?", "\\?",
		"[", "\\[",
		"]", "\\]",
		"\\", "\\\\",
	)
	return replacer.Replace(s)
}

// FindNewestReport finds the most recent report file matching the tool and task
func FindNewestReport(reportDir, tool, task string) (string, error) {
	// Sanitize tool and task to prevent glob injection
	safeTool := escapeGlobPattern(strings.ToLower(tool))
	safeTask := escapeGlobPattern(strings.ToLower(task))

	// Pattern: {codebase}-{tool}-{task}-{timestamp}.md
	pattern := filepath.Join(reportDir, fmt.Sprintf("*-%s-%s-*.md", safeTool, safeTask))
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return "", fmt.Errorf("failed to glob reports: %w", err)
	}

	if len(matches) == 0 {
		return "", fmt.Errorf("no matching report found")
	}

	// Find the newest by modification time
	var newest string
	var newestTime time.Time

	for _, match := range matches {
		info, err := os.Stat(match)
		if err != nil {
			continue
		}
		if info.ModTime().After(newestTime) {
			newestTime = info.ModTime()
			newest = match
		}
	}

	if newest == "" {
		return "", fmt.Errorf("could not determine newest report")
	}

	return newest, nil
}
