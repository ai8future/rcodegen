package batch

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// Checkpoint captures the state of a batch run so it can be resumed later.
type Checkpoint struct {
	Batch        string         `json:"batch"`
	CheckpointAt string         `json:"checkpoint_at"`
	Reason       string         `json:"reason"`
	Snapshot     *QueueSnapshot `json:"snapshot"`
}

// Save writes the checkpoint to batchDir/state.json, creating batchDir if
// needed. It stamps CheckpointAt with the current UTC time and attaches the
// provided QueueSnapshot.
func (c *Checkpoint) Save(batchDir string, snap *QueueSnapshot) error {
	if err := os.MkdirAll(batchDir, 0o755); err != nil {
		return fmt.Errorf("creating batch dir: %w", err)
	}

	c.CheckpointAt = time.Now().UTC().Format(time.RFC3339)
	c.Snapshot = snap

	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling checkpoint: %w", err)
	}

	path := filepath.Join(batchDir, "state.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("writing checkpoint: %w", err)
	}

	return nil
}

// LoadCheckpoint reads and parses a state.json file at the given path.
func LoadCheckpoint(path string) (*Checkpoint, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading checkpoint: %w", err)
	}

	var cp Checkpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		return nil, fmt.Errorf("parsing checkpoint: %w", err)
	}

	return &cp, nil
}

// FindLatestCheckpoint scans subdirectories of baseDir for state.json files
// and returns the path to the most recently modified one.
func FindLatestCheckpoint(baseDir string) (string, error) {
	entries, err := os.ReadDir(baseDir)
	if err != nil {
		return "", fmt.Errorf("reading base dir: %w", err)
	}

	var latestPath string
	var latestMod time.Time

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		candidate := filepath.Join(baseDir, entry.Name(), "state.json")
		info, err := os.Stat(candidate)
		if err != nil {
			continue // no state.json in this subdirectory
		}
		if latestPath == "" || info.ModTime().After(latestMod) {
			latestPath = candidate
			latestMod = info.ModTime()
		}
	}

	if latestPath == "" {
		return "", fmt.Errorf("no checkpoint found in %s", baseDir)
	}

	return latestPath, nil
}

// validBatchName matches alphanumeric characters, hyphens, and underscores only.
// This prevents path traversal via names like "../../etc" or names with path separators.
var validBatchName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

// BatchDir returns the standard checkpoint directory for a named batch:
// ~/.rcodegen/batches/<batchName>
// The batch name is validated to prevent path traversal.
func BatchDir(batchName string) (string, error) {
	if !validBatchName.MatchString(batchName) {
		return "", fmt.Errorf("invalid batch name %q: must be alphanumeric with hyphens/underscores/dots only", batchName)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home dir: %w", err)
	}
	dir := filepath.Join(home, ".rcodegen", "batches", batchName)
	// Final safety check: ensure the resolved path is under the expected base.
	base := filepath.Join(home, ".rcodegen", "batches")
	if !filepath.HasPrefix(dir, base) {
		return "", fmt.Errorf("batch name %q resolves outside batches directory", batchName)
	}
	return dir, nil
}
