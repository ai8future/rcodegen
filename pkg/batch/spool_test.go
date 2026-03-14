package batch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSpoolInit(t *testing.T) {
	dir := t.TempDir()
	s := NewSpool(dir)

	if err := s.Init(); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	for _, sub := range []string{"pending", "running", "done", "failed"} {
		path := filepath.Join(dir, sub)
		info, err := os.Stat(path)
		if err != nil {
			t.Errorf("expected subdirectory %q to exist, got error: %v", sub, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("expected %q to be a directory", sub)
		}
	}
}

func TestSpoolScan(t *testing.T) {
	dir := t.TempDir()
	s := NewSpool(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	// Write two valid manifest JSON files into pending/
	manifests := []struct {
		filename string
		data     map[string]any
	}{
		{
			filename: "01-lint.json",
			data: map[string]any{
				"name": "lint-a",
				"jobs": []map[string]any{
					{"task": "lint", "tool": "claude", "dir": "/tmp"},
				},
			},
		},
		{
			filename: "02-test.json",
			data: map[string]any{
				"name": "test-b",
				"jobs": []map[string]any{
					{"task": "test", "tool": "codex", "dir": "/tmp"},
				},
			},
		},
	}

	for _, m := range manifests {
		raw, err := json.Marshal(m.data)
		if err != nil {
			t.Fatalf("json.Marshal: %v", err)
		}
		path := filepath.Join(dir, "pending", m.filename)
		if err := os.WriteFile(path, raw, 0644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}

	results, err := s.Scan()
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("Scan() returned %d manifests, want 2", len(results))
	}

	// Should be sorted by filename, so lint-a first, test-b second
	if results[0].Manifest.Name != "lint-a" {
		t.Errorf("results[0].Manifest.Name = %q, want %q", results[0].Manifest.Name, "lint-a")
	}
	if results[0].Filename != "01-lint.json" {
		t.Errorf("results[0].Filename = %q, want %q", results[0].Filename, "01-lint.json")
	}
	if results[1].Manifest.Name != "test-b" {
		t.Errorf("results[1].Manifest.Name = %q, want %q", results[1].Manifest.Name, "test-b")
	}
	if results[1].Filename != "02-test.json" {
		t.Errorf("results[1].Filename = %q, want %q", results[1].Filename, "02-test.json")
	}
}

func TestSpoolScanSkipsInvalid(t *testing.T) {
	dir := t.TempDir()
	s := NewSpool(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	// Write one valid and one invalid file
	valid := map[string]any{
		"name": "good",
		"jobs": []map[string]any{
			{"task": "lint", "tool": "claude", "dir": "/tmp"},
		},
	}
	raw, _ := json.Marshal(valid)
	os.WriteFile(filepath.Join(dir, "pending", "01-good.json"), raw, 0644)
	os.WriteFile(filepath.Join(dir, "pending", "02-bad.json"), []byte("not json"), 0644)

	results, err := s.Scan()
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("Scan() returned %d manifests, want 1 (bad file should be skipped)", len(results))
	}
	if results[0].Manifest.Name != "good" {
		t.Errorf("results[0].Manifest.Name = %q, want %q", results[0].Manifest.Name, "good")
	}
	if results[0].Filename != "01-good.json" {
		t.Errorf("results[0].Filename = %q, want %q", results[0].Filename, "01-good.json")
	}
}

func TestSpoolScanIgnoresNonJSON(t *testing.T) {
	dir := t.TempDir()
	s := NewSpool(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	// Write a .txt file — should be ignored
	os.WriteFile(filepath.Join(dir, "pending", "readme.txt"), []byte("hello"), 0644)

	results, err := s.Scan()
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("Scan() returned %d manifests, want 0 (non-JSON files should be ignored)", len(results))
	}
}

func TestSpoolMoveToRunning(t *testing.T) {
	dir := t.TempDir()
	s := NewSpool(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	// Create a file in pending/
	filename := "job1.json"
	data := map[string]any{
		"name": "job1",
		"jobs": []map[string]any{
			{"task": "lint", "tool": "claude", "dir": "/tmp"},
		},
	}
	raw, _ := json.Marshal(data)
	os.WriteFile(filepath.Join(dir, "pending", filename), raw, 0644)

	if err := s.MarkRunning(filename); err != nil {
		t.Fatalf("MarkRunning() error = %v", err)
	}

	// File should no longer be in pending/
	if _, err := os.Stat(filepath.Join(dir, "pending", filename)); !os.IsNotExist(err) {
		t.Error("file still exists in pending/ after MarkRunning")
	}

	// File should be in running/
	if _, err := os.Stat(filepath.Join(dir, "running", filename)); err != nil {
		t.Errorf("file not found in running/ after MarkRunning: %v", err)
	}
}

func TestSpoolMoveToDone(t *testing.T) {
	dir := t.TempDir()
	s := NewSpool(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	// Create a file in running/
	filename := "job1.json"
	data := map[string]any{
		"name": "job1",
		"jobs": []map[string]any{
			{"task": "lint", "tool": "claude", "dir": "/tmp"},
		},
	}
	raw, _ := json.Marshal(data)
	os.WriteFile(filepath.Join(dir, "running", filename), raw, 0644)

	if err := s.MarkDone(filename); err != nil {
		t.Fatalf("MarkDone() error = %v", err)
	}

	// File should no longer be in running/
	if _, err := os.Stat(filepath.Join(dir, "running", filename)); !os.IsNotExist(err) {
		t.Error("file still exists in running/ after MarkDone")
	}

	// File should be in done/
	if _, err := os.Stat(filepath.Join(dir, "done", filename)); err != nil {
		t.Errorf("file not found in done/ after MarkDone: %v", err)
	}
}

func TestSpoolMoveToFailed(t *testing.T) {
	dir := t.TempDir()
	s := NewSpool(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	// Create a file in running/
	filename := "job1.json"
	data := map[string]any{
		"name": "job1",
		"jobs": []map[string]any{
			{"task": "lint", "tool": "claude", "dir": "/tmp"},
		},
	}
	raw, _ := json.Marshal(data)
	os.WriteFile(filepath.Join(dir, "running", filename), raw, 0644)

	if err := s.MarkFailed(filename); err != nil {
		t.Fatalf("MarkFailed() error = %v", err)
	}

	// File should no longer be in running/
	if _, err := os.Stat(filepath.Join(dir, "running", filename)); !os.IsNotExist(err) {
		t.Error("file still exists in running/ after MarkFailed")
	}

	// File should be in failed/
	if _, err := os.Stat(filepath.Join(dir, "failed", filename)); err != nil {
		t.Errorf("file not found in failed/ after MarkFailed: %v", err)
	}
}
