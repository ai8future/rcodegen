// bundles.go implements the bundle execution HTTP API:
//
//	GET  /v1/bundles        — list available bundles with their inputs
//	POST /v1/bundles/{name} — run a bundle and return results + inline artifacts
//
// Callers may pass an X-Correlation-ID header (e.g. a Windmill job UUID); it is
// echoed in the response body and header, and attached to the run registry entry
// so GetStatus shows which external run owns each slot.
package openai

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"rcodegen/pkg/bundle"
	"rcodegen/pkg/envelope"
	"rcodegen/pkg/orchestrator"
	"rcodegen/pkg/settings"
)

// bundleRunFunc executes a loaded bundle with resolved inputs. It is a field on
// Handler so tests can substitute a fake without spawning real AI tools.
type bundleRunFunc func(b *bundle.Bundle, inputs map[string]string) (*envelope.Envelope, error)

// defaultBundleRun returns the production bundleRunFunc backed by the orchestrator.
func defaultBundleRun(s *settings.Settings) bundleRunFunc {
	return func(b *bundle.Bundle, inputs map[string]string) (*envelope.Envelope, error) {
		orch := orchestrator.New(s)
		orch.SetLiveMode(false) // no animated display for HTTP
		return orch.Run(b, inputs)
	}
}

// BundleInput describes one declared input of a bundle.
type BundleInput struct {
	Name        string `json:"name"`
	Required    bool   `json:"required"`
	Description string `json:"description,omitempty"`
	Default     string `json:"default,omitempty"`
}

// BundleInfo summarizes a bundle for the list endpoint.
type BundleInfo struct {
	Name        string        `json:"name"`
	Description string        `json:"description"`
	StepCount   int           `json:"step_count"`
	Inputs      []BundleInput `json:"inputs,omitempty"`
}

// BundleListResponse is the GET /v1/bundles response body.
type BundleListResponse struct {
	Bundles []BundleInfo `json:"bundles"`
}

// BundleRunRequest is the POST /v1/bundles/{name} request body.
// If work_dir is set it is created if missing, used as the artifact scan root,
// and injected as the "output_dir" bundle input unless one was given explicitly.
type BundleRunRequest struct {
	Inputs  map[string]string `json:"inputs,omitempty"`
	WorkDir string            `json:"work_dir,omitempty"`
}

// BundleArtifact is a text file created or modified under work_dir during the run,
// returned inline so callers need no filesystem access to the server host.
type BundleArtifact struct {
	Path      string `json:"path"` // relative to work_dir
	Content   string `json:"content,omitempty"`
	SizeBytes int64  `json:"size_bytes"`
	Truncated bool   `json:"truncated,omitempty"`
}

// BundleRunResponse is the POST /v1/bundles/{name} response body.
type BundleRunResponse struct {
	RunID         string              `json:"run_id"`
	CorrelationID string              `json:"correlation_id,omitempty"`
	Bundle        string              `json:"bundle"`
	Status        string              `json:"status"` // success, failure, partial
	JobID         string              `json:"job_id,omitempty"`
	TotalCostUSD  float64             `json:"total_cost_usd"`
	Usage         *Usage              `json:"usage,omitempty"`
	DurationMs    int64               `json:"duration_ms"`
	Error         *envelope.ErrorInfo `json:"error,omitempty"`
	Artifacts     []BundleArtifact    `json:"artifacts,omitempty"`
}

// handleBundles serves GET /v1/bundles.
func (h *Handler) handleBundles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, NewErrorResponse(
			"method not allowed", "invalid_request_error", "method_not_allowed",
		))
		return
	}

	names, err := bundle.List()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, NewErrorResponse(
			"failed to list bundles: "+err.Error(), "server_error", "bundle_list_failed",
		))
		return
	}

	resp := BundleListResponse{Bundles: []BundleInfo{}}
	for _, name := range names {
		b, err := bundle.Load(name)
		if err != nil {
			continue
		}
		info := BundleInfo{
			Name:        b.Name,
			Description: b.Description,
			StepCount:   len(b.Steps),
		}
		for _, in := range b.Inputs {
			info.Inputs = append(info.Inputs, BundleInput{
				Name:        in.Name,
				Required:    in.Required,
				Description: in.Description,
				Default:     in.Default,
			})
		}
		resp.Bundles = append(resp.Bundles, info)
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleBundleByName serves POST /v1/bundles/{name}.
func (h *Handler) handleBundleByName(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, NewErrorResponse(
			"method not allowed", "invalid_request_error", "method_not_allowed",
		))
		return
	}

	name := strings.TrimPrefix(r.URL.Path, "/v1/bundles/")
	if name == "" || strings.Contains(name, "/") {
		writeJSON(w, http.StatusNotFound, NewErrorResponse(
			"bundle not found", "invalid_request_error", "unknown_bundle",
		))
		return
	}

	// Limit request body to 1MB — bundle inputs are small.
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req BundleRunRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeJSON(w, http.StatusBadRequest, NewErrorResponse(
			"invalid JSON: "+err.Error(), "invalid_request_error", "invalid_json",
		))
		return
	}

	b, err := bundle.Load(name)
	if err != nil {
		writeJSON(w, http.StatusNotFound, NewErrorResponse(
			"bundle not found: "+err.Error(), "invalid_request_error", "unknown_bundle",
		))
		return
	}

	inputs := make(map[string]string, len(req.Inputs))
	for k, v := range req.Inputs {
		inputs[k] = v
	}

	// Resolve and prepare work_dir; it doubles as the default output_dir input.
	workDir := ""
	if req.WorkDir != "" {
		if !filepath.IsAbs(req.WorkDir) {
			writeJSON(w, http.StatusBadRequest, NewErrorResponse(
				"work_dir must be an absolute path", "invalid_request_error", "invalid_work_dir",
			))
			return
		}
		workDir = filepath.Clean(req.WorkDir)
		if err := os.MkdirAll(workDir, 0o755); err != nil {
			writeJSON(w, http.StatusInternalServerError, NewErrorResponse(
				"failed to create work_dir: "+err.Error(), "server_error", "work_dir_failed",
			))
			return
		}
		if _, ok := inputs["output_dir"]; !ok {
			inputs["output_dir"] = workDir
		}
	}

	corrID := sanitizeCorrelationID(r.Header.Get("X-Correlation-ID"))
	task := b.Name
	if corrID != "" {
		task = b.Name + " corr=" + corrID
	}

	runID, _, cancel, err := h.registry.Acquire(r.Context(), "bundle", task)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, NewErrorResponse(
			"failed to acquire run slot: "+err.Error(), "server_error", "concurrency_limit",
		))
		return
	}
	defer cancel()
	defer h.registry.Release(runID)

	before := snapshotWorkDir(workDir)
	start := time.Now()
	env, runErr := h.runBundleFn(b, inputs)

	if env == nil {
		msg := "bundle execution failed"
		if runErr != nil {
			msg = runErr.Error()
		}
		writeJSON(w, http.StatusInternalServerError, NewErrorResponse(
			msg, "server_error", "bundle_failed",
		))
		return
	}

	// Missing required input is a caller error, not a run failure.
	if env.Error != nil && env.Error.Code == "MISSING_INPUT" {
		writeJSON(w, http.StatusBadRequest, NewErrorResponse(
			env.Error.Message, "invalid_request_error", "missing_input",
		))
		return
	}

	resp := BundleRunResponse{
		RunID:         runID,
		CorrelationID: corrID,
		Bundle:        b.Name,
		Status:        string(env.Status),
		Error:         env.Error,
		Artifacts:     collectBundleArtifacts(workDir, before),
	}
	if jobID, ok := env.Result["job_id"].(string); ok {
		resp.JobID = jobID
	}
	if cost, ok := env.Result["total_cost_usd"].(float64); ok {
		resp.TotalCostUSD = cost
	}
	in := resultInt(env.Result["input_tokens"])
	out := resultInt(env.Result["output_tokens"])
	if in > 0 || out > 0 {
		resp.Usage = &Usage{
			PromptTokens:     in,
			CompletionTokens: out,
			TotalTokens:      in + out,
		}
	}
	if env.Metrics != nil && env.Metrics.DurationMs > 0 {
		resp.DurationMs = env.Metrics.DurationMs
	} else {
		resp.DurationMs = time.Since(start).Milliseconds()
	}

	if corrID != "" {
		w.Header().Set("X-Correlation-ID", corrID)
	}
	writeJSON(w, http.StatusOK, resp)
}

// sanitizeCorrelationID keeps [A-Za-z0-9._-] and caps length at 128 so external
// identifiers cannot inject control characters into logs or status output.
func sanitizeCorrelationID(raw string) string {
	var sb strings.Builder
	for _, r := range raw {
		if sb.Len() >= 128 {
			break
		}
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '_', r == '-':
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

// resultInt converts an envelope result value to int, handling both in-process
// int values and float64 values from JSON round-trips.
func resultInt(v interface{}) int {
	switch n := v.(type) {
	case int:
		return n
	case float64:
		return int(n)
	}
	return 0
}

// Artifact collection: text files created or modified under work_dir during the
// run are returned inline, size-capped, so callers (e.g. Windmill flows) can
// review and publish reports without filesystem access to this host.
const (
	artifactFileCap  = 512 << 10 // max bytes of content per artifact
	artifactTotalCap = 2 << 20   // max total bytes of content per response
)

var artifactTextExts = map[string]bool{
	".md": true, ".txt": true, ".json": true, ".csv": true,
	".html": true, ".htm": true, ".xml": true, ".yaml": true, ".yml": true, ".log": true,
}

type artifactMeta struct {
	size    int64
	modNano int64
}

// snapshotWorkDir records size+mtime for every visible file under root.
// Returns nil for an empty root. Hidden files and directories are skipped.
func snapshotWorkDir(root string) map[string]artifactMeta {
	if root == "" {
		return nil
	}
	snap := make(map[string]artifactMeta)
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // best-effort: unreadable entries are simply not tracked
		}
		if d.IsDir() {
			if path != root && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		snap[rel] = artifactMeta{size: info.Size(), modNano: info.ModTime().UnixNano()}
		return nil
	})
	return snap
}

// collectBundleArtifacts returns text files that are new or changed relative to
// the before snapshot, sorted by path, with content inlined up to the caps.
func collectBundleArtifacts(root string, before map[string]artifactMeta) []BundleArtifact {
	if root == "" {
		return nil
	}
	after := snapshotWorkDir(root)

	var paths []string
	for rel, meta := range after {
		if !artifactTextExts[strings.ToLower(filepath.Ext(rel))] {
			continue
		}
		if prev, ok := before[rel]; ok && prev == meta {
			continue
		}
		paths = append(paths, rel)
	}
	sort.Strings(paths)

	var artifacts []BundleArtifact
	budget := artifactTotalCap
	for _, rel := range paths {
		meta := after[rel]
		a := BundleArtifact{Path: rel, SizeBytes: meta.size}
		cap := artifactFileCap
		if budget < cap {
			cap = budget
		}
		if cap > 0 {
			content, err := readFileCapped(filepath.Join(root, rel), cap)
			if err == nil {
				a.Content = content
				a.Truncated = int64(len(content)) < meta.size
				budget -= len(content)
			}
		} else {
			a.Truncated = true
		}
		artifacts = append(artifacts, a)
	}
	return artifacts
}

// readFileCapped reads at most max bytes of a file.
func readFileCapped(path string, max int) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, int64(max)))
	if err != nil {
		return "", err
	}
	return string(data), nil
}
