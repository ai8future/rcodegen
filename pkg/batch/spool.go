package batch

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Spool manages a directory with pending/running/done/failed subdirectories
// for job file lifecycle management.
type Spool struct {
	Dir string
}

// NewSpool creates a new Spool rooted at the given directory.
func NewSpool(dir string) *Spool {
	return &Spool{Dir: dir}
}

// Init creates the four subdirectories (pending, running, done, failed)
// under the spool directory.
func (s *Spool) Init() error {
	for _, sub := range []string{"pending", "running", "done", "failed"} {
		path := filepath.Join(s.Dir, sub)
		if err := os.MkdirAll(path, 0755); err != nil {
			return fmt.Errorf("creating spool subdirectory %s: %w", sub, err)
		}
	}
	return nil
}

// SpoolEntry pairs a manifest with the filename it was loaded from,
// so callers can reliably track which file corresponds to which manifest.
type SpoolEntry struct {
	Filename string
	Manifest *Manifest
}

// Scan reads all .json files from the pending/ subdirectory, sorted by
// filename for deterministic session chain ordering. Each file is parsed
// with LoadManifest. Files that fail to parse are warned about and skipped.
// Returns SpoolEntry pairs so callers can track filenames reliably even
// when some files are skipped due to parse errors.
func (s *Spool) Scan() ([]SpoolEntry, error) {
	pendingDir := filepath.Join(s.Dir, "pending")
	entries, err := os.ReadDir(pendingDir)
	if err != nil {
		return nil, fmt.Errorf("reading pending directory: %w", err)
	}

	// Filter to .json files and sort by name
	var jsonFiles []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasSuffix(strings.ToLower(e.Name()), ".json") {
			jsonFiles = append(jsonFiles, e.Name())
		}
	}
	sort.Strings(jsonFiles)

	var results []SpoolEntry
	for _, name := range jsonFiles {
		path := filepath.Join(pendingDir, name)
		m, err := LoadManifest(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "spool: skipping %s: %v\n", name, err)
			continue
		}
		results = append(results, SpoolEntry{Filename: name, Manifest: m})
	}

	return results, nil
}

// MarkRunning moves a file from pending/ to running/.
func (s *Spool) MarkRunning(filename string) error {
	return s.moveFile("pending", "running", filename)
}

// MarkDone moves a file from running/ to done/.
func (s *Spool) MarkDone(filename string) error {
	return s.moveFile("running", "done", filename)
}

// MarkFailed moves a file from running/ to failed/.
func (s *Spool) MarkFailed(filename string) error {
	return s.moveFile("running", "failed", filename)
}

// moveFile moves a file from one subdirectory to another, creating the
// destination directory if it does not exist.
func (s *Spool) moveFile(from, to, filename string) error {
	destDir := filepath.Join(s.Dir, to)
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return fmt.Errorf("creating destination directory %s: %w", to, err)
	}

	src := filepath.Join(s.Dir, from, filename)
	dst := filepath.Join(destDir, filename)

	if err := os.Rename(src, dst); err != nil {
		return fmt.Errorf("moving %s from %s to %s: %w", filename, from, to, err)
	}

	return nil
}
