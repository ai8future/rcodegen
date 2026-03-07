package batch

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckpointSaveAndLoad(t *testing.T) {
	// Create a temporary directory for the batch.
	tmpDir := t.TempDir()
	batchDir := filepath.Join(tmpDir, "my-batch")

	// Build a QueueSnapshot with 1 completed and 1 pending job.
	snap := &QueueSnapshot{
		Completed: []CompletedJob{
			{
				Name:      "job-1",
				Cost:      0.42,
				Duration:  "12s",
				SessionID: "sess-abc123",
			},
		},
		Pending: []JobDef{
			{
				Name: "job-2",
				Task: "do something else",
				Tool: "claude",
			},
		},
		TotalCost: 0.42,
	}

	// Create a Checkpoint and save it.
	cp := &Checkpoint{
		Batch:  "my-batch",
		Reason: "signal",
	}
	if err := cp.Save(batchDir, snap); err != nil {
		t.Fatalf("Save: unexpected error: %v", err)
	}

	// Verify that CheckpointAt was set.
	if cp.CheckpointAt == "" {
		t.Fatal("Save did not set CheckpointAt")
	}

	// Verify the state.json file was created.
	statePath := filepath.Join(batchDir, "state.json")
	if _, err := os.Stat(statePath); err != nil {
		t.Fatalf("state.json not created: %v", err)
	}

	// Load the checkpoint back.
	loaded, err := LoadCheckpoint(statePath)
	if err != nil {
		t.Fatalf("LoadCheckpoint: unexpected error: %v", err)
	}

	// Verify all fields match.
	if loaded.Batch != cp.Batch {
		t.Errorf("Batch: got %q, want %q", loaded.Batch, cp.Batch)
	}
	if loaded.CheckpointAt != cp.CheckpointAt {
		t.Errorf("CheckpointAt: got %q, want %q", loaded.CheckpointAt, cp.CheckpointAt)
	}
	if loaded.Reason != cp.Reason {
		t.Errorf("Reason: got %q, want %q", loaded.Reason, cp.Reason)
	}

	if loaded.Snapshot == nil {
		t.Fatal("Snapshot is nil after load")
	}

	// Verify completed jobs.
	if len(loaded.Snapshot.Completed) != 1 {
		t.Fatalf("Completed: got %d jobs, want 1", len(loaded.Snapshot.Completed))
	}
	cj := loaded.Snapshot.Completed[0]
	if cj.Name != "job-1" {
		t.Errorf("Completed[0].Name: got %q, want %q", cj.Name, "job-1")
	}
	if cj.SessionID != "sess-abc123" {
		t.Errorf("Completed[0].SessionID: got %q, want %q", cj.SessionID, "sess-abc123")
	}
	if cj.Cost != 0.42 {
		t.Errorf("Completed[0].Cost: got %v, want %v", cj.Cost, 0.42)
	}
	if cj.Duration != "12s" {
		t.Errorf("Completed[0].Duration: got %q, want %q", cj.Duration, "12s")
	}

	// Verify pending jobs.
	if len(loaded.Snapshot.Pending) != 1 {
		t.Fatalf("Pending: got %d jobs, want 1", len(loaded.Snapshot.Pending))
	}
	pj := loaded.Snapshot.Pending[0]
	if pj.Name != "job-2" {
		t.Errorf("Pending[0].Name: got %q, want %q", pj.Name, "job-2")
	}
	if pj.Task != "do something else" {
		t.Errorf("Pending[0].Task: got %q, want %q", pj.Task, "do something else")
	}

	// Verify total cost.
	if loaded.Snapshot.TotalCost != 0.42 {
		t.Errorf("TotalCost: got %v, want %v", loaded.Snapshot.TotalCost, 0.42)
	}
}

func TestCheckpointLatest(t *testing.T) {
	tmpDir := t.TempDir()

	// FindLatestCheckpoint should return an error for an empty directory.
	_, err := FindLatestCheckpoint(tmpDir)
	if err == nil {
		t.Fatal("FindLatestCheckpoint: expected error for empty dir, got nil")
	}

	// Create a batch subdirectory with a checkpoint.
	batchDir := filepath.Join(tmpDir, "batch-alpha")
	snap := &QueueSnapshot{
		Pending: []JobDef{
			{Name: "job-1", Task: "hello", Tool: "claude"},
		},
	}
	cp := &Checkpoint{
		Batch:  "batch-alpha",
		Reason: "manual",
	}
	if err := cp.Save(batchDir, snap); err != nil {
		t.Fatalf("Save: unexpected error: %v", err)
	}

	// Now FindLatestCheckpoint should find it.
	found, err := FindLatestCheckpoint(tmpDir)
	if err != nil {
		t.Fatalf("FindLatestCheckpoint: unexpected error: %v", err)
	}

	expected := filepath.Join(batchDir, "state.json")
	if found != expected {
		t.Errorf("FindLatestCheckpoint: got %q, want %q", found, expected)
	}
}
