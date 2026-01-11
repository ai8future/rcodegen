package reports

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ANSI color codes
const (
	Dim    = "\033[2m"
	Yellow = "\033[33m"
	Reset  = "\033[0m"
)

// reviewScanLines is the number of lines to scan for the review marker
const reviewScanLines = 10

// ShouldSkipTask checks if we should skip a task because the previous report wasn't reviewed
// Returns true if we should SKIP (previous report exists but has no "Date Modified:")
// Returns false if we should RUN (no previous report, or previous report was reviewed)
func ShouldSkipTask(reportDir string, shortcut string, pattern string, requireReview bool) bool {
	if !requireReview || pattern == "" {
		return false
	}

	// Check if report directory exists
	if _, err := os.Stat(reportDir); os.IsNotExist(err) {
		return false // No report dir, first run, so run
	}

	// Find reports matching pattern
	globPattern := filepath.Join(reportDir, pattern+"*.md")

	matches, err := filepath.Glob(globPattern)
	if err != nil || len(matches) == 0 {
		return false // No previous reports, so run
	}

	// Find the newest report
	newestFile := FindNewestReport(matches)
	if newestFile == "" {
		return false // Couldn't find newest, so run
	}

	// Check if report was reviewed
	if IsReportReviewed(newestFile) {
		return false // Report was reviewed, so RUN
	}

	// No "Date Modified:" found in first 10 lines - report unreviewed, SKIP
	fmt.Printf("%sSkipping %s:%s previous report unreviewed (%s)\n", Yellow, shortcut, Reset, filepath.Base(newestFile))
	return true
}

// FindNewestReport finds the most recent file from a list of file paths
func FindNewestReport(files []string) string {
	var newestFile string
	var newestTime time.Time

	for _, file := range files {
		if info, err := os.Stat(file); err == nil {
			if info.ModTime().After(newestTime) {
				newestTime = info.ModTime()
				newestFile = file
			}
		}
	}

	return newestFile
}

// IsReportReviewed checks if a report has been reviewed (contains "Date Modified:" in first 10 lines)
func IsReportReviewed(filepath string) bool {
	file, err := os.Open(filepath)
	if err != nil {
		return false // Can't open, assume unreviewed
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	lineCount := 0
	for scanner.Scan() && lineCount < reviewScanLines {
		line := scanner.Text()
		if strings.Contains(line, "Date Modified:") {
			return true
		}
		lineCount++
	}

	// Check for scanner errors (I/O errors are treated as unreviewed)
	if err := scanner.Err(); err != nil {
		return false
	}

	return false
}

// DeleteOldReports removes previous reports matching the patterns, keeping only the newest for each
func DeleteOldReports(reportDir string, shortcuts []string, reportPatterns map[string]string) {
	// Check if report directory exists
	if _, err := os.Stat(reportDir); os.IsNotExist(err) {
		return
	}

	for _, shortcut := range shortcuts {
		pattern, ok := reportPatterns[shortcut]
		if !ok {
			continue
		}

		// Build glob pattern
		globPattern := filepath.Join(reportDir, pattern+"*.md")

		matches, err := filepath.Glob(globPattern)
		if err != nil || len(matches) <= 1 {
			continue
		}

		// Sort by modification time (newest first)
		type fileInfo struct {
			path    string
			modTime time.Time
		}
		var files []fileInfo
		for _, match := range matches {
			if info, err := os.Stat(match); err == nil {
				files = append(files, fileInfo{path: match, modTime: info.ModTime()})
			}
		}

		// Sort newest first (O(n log n) instead of O(n²) bubble sort)
		sort.Slice(files, func(i, j int) bool {
			return files[i].modTime.After(files[j].modTime)
		})

		// Delete all but the newest
		for i := 1; i < len(files); i++ {
			fmt.Printf("%sDeleting old report:%s %s\n", Dim, Reset, filepath.Base(files[i].path))
			os.Remove(files[i].path)
		}
	}
}
