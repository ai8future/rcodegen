package openai

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	maxUploadSize = 50 << 20 // 50 MB
	fileTTL       = 24 * time.Hour
	purgeInterval = 1 * time.Hour
)

// fileMeta tracks an uploaded file's metadata in memory.
type fileMeta struct {
	ID        string
	Filename  string
	Path      string
	Bytes     int64
	CreatedAt time.Time
	Purpose   string
}

// FileStore manages uploaded files on disk with automatic 24h expiry.
type FileStore struct {
	baseDir string
	mu      sync.RWMutex
	files   map[string]*fileMeta // id -> meta
	stopCh  chan struct{}
	stopped atomic.Bool
}

// NewFileStore creates a FileStore rooted at baseDir and starts a background
// purge goroutine. Call Stop() to terminate the goroutine.
func NewFileStore(baseDir string) (*FileStore, error) {
	if err := os.MkdirAll(baseDir, 0o700); err != nil {
		return nil, fmt.Errorf("create file store dir: %w", err)
	}
	fs := &FileStore{
		baseDir: baseDir,
		files:   make(map[string]*fileMeta),
		stopCh:  make(chan struct{}),
	}
	// Recover any files already on disk (e.g. after restart).
	fs.recoverFromDisk()
	go fs.purgeLoop()
	return fs, nil
}

// Stop terminates the background purge goroutine. Safe to call multiple times.
func (fs *FileStore) Stop() {
	if fs.stopped.CompareAndSwap(false, true) {
		close(fs.stopCh)
	}
}

// sanitizeFilename strips path separators and non-printable characters.
var safeFilenameRe = regexp.MustCompile(`[^a-zA-Z0-9._-]`)

func sanitizeFilename(name string) string {
	name = filepath.Base(name) // strip directory components
	name = safeFilenameRe.ReplaceAllString(name, "_")
	if name == "" || name == "." || name == ".." {
		name = "upload"
	}
	return name
}

func newFileID() string {
	b := make([]byte, 12)
	rand.Read(b)
	return "file-" + hex.EncodeToString(b)
}

// Save writes the contents of r to disk and registers the file.
func (fs *FileStore) Save(filename, purpose string, r io.Reader) (*fileMeta, error) {
	id := newFileID()
	safe := sanitizeFilename(filename)
	diskName := id + "-" + safe
	path := filepath.Join(fs.baseDir, diskName)

	f, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("create file: %w", err)
	}
	n, err := io.Copy(f, r)
	if closeErr := f.Close(); closeErr != nil && err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(path)
		return nil, fmt.Errorf("write file: %w", err)
	}

	meta := &fileMeta{
		ID:        id,
		Filename:  safe,
		Path:      path,
		Bytes:     n,
		CreatedAt: time.Now(),
		Purpose:   purpose,
	}
	fs.mu.Lock()
	fs.files[id] = meta
	fs.mu.Unlock()
	return meta, nil
}

// Get returns the metadata for a file by ID.
func (fs *FileStore) Get(id string) (*fileMeta, bool) {
	fs.mu.RLock()
	defer fs.mu.RUnlock()
	m, ok := fs.files[id]
	return m, ok
}

// List returns all tracked files sorted by creation time (newest first).
func (fs *FileStore) List() []*fileMeta {
	fs.mu.RLock()
	defer fs.mu.RUnlock()
	out := make([]*fileMeta, 0, len(fs.files))
	for _, m := range fs.files {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out
}

// Delete removes a file from disk and the registry.
func (fs *FileStore) Delete(id string) bool {
	fs.mu.Lock()
	m, ok := fs.files[id]
	if ok {
		delete(fs.files, id)
	}
	fs.mu.Unlock()
	if ok {
		os.Remove(m.Path)
	}
	return ok
}

// purgeLoop removes files older than fileTTL every purgeInterval.
func (fs *FileStore) purgeLoop() {
	ticker := time.NewTicker(purgeInterval)
	defer ticker.Stop()
	for {
		select {
		case <-fs.stopCh:
			return
		case <-ticker.C:
			fs.purgeExpired()
		}
	}
}

func (fs *FileStore) purgeExpired() {
	cutoff := time.Now().Add(-fileTTL)
	fs.mu.Lock()
	var expired []*fileMeta
	for id, m := range fs.files {
		if m.CreatedAt.Before(cutoff) {
			expired = append(expired, m)
			delete(fs.files, id)
		}
	}
	fs.mu.Unlock()
	for _, m := range expired {
		os.Remove(m.Path)
	}
}

// recoverFromDisk scans baseDir for files matching the file-<hex>-<name> pattern
// and re-registers them so they survive server restarts within the TTL window.
func (fs *FileStore) recoverFromDisk() {
	entries, err := os.ReadDir(fs.baseDir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-fileTTL)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, "file-") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		// Purge files that are already expired.
		if info.ModTime().Before(cutoff) {
			os.Remove(filepath.Join(fs.baseDir, name))
			continue
		}
		// Extract ID (file-<24 hex chars>) and original filename.
		// Format: file-<24hex>-<sanitized_name>
		parts := strings.SplitN(name, "-", 3)
		if len(parts) < 3 || len(parts[1]) != 24 {
			continue
		}
		id := parts[0] + "-" + parts[1]
		origName := parts[2]
		fs.files[id] = &fileMeta{
			ID:        id,
			Filename:  origName,
			Path:      filepath.Join(fs.baseDir, name),
			Bytes:     info.Size(),
			CreatedAt: info.ModTime(),
			Purpose:   "user_data",
		}
	}
}

// toFileObject converts internal metadata to the API response type.
func (m *fileMeta) toFileObject() FileObject {
	return FileObject{
		ID:        m.ID,
		Object:    "file",
		Bytes:     m.Bytes,
		CreatedAt: m.CreatedAt.Unix(),
		Filename:  m.Filename,
		Purpose:   m.Purpose,
		Path:      m.Path,
	}
}

// ---------------------------------------------------------------------------
// HTTP handlers (called from Handler)
// ---------------------------------------------------------------------------

// handleUploadFile handles POST /v1/files (multipart form).
func (h *Handler) handleUploadFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, NewErrorResponse(
			"method not allowed", "invalid_request_error", codeMethodNotAllowed,
		))
		return
	}
	if h.fileStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, NewErrorResponse(
			"file storage not configured", "server_error", codeNoFileStore,
		))
		return
	}

	// Limit total multipart size.
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize+1024) // extra for form fields
	if err := r.ParseMultipartForm(maxUploadSize); err != nil {
		writeJSON(w, http.StatusBadRequest, NewErrorResponse(
			"file too large or invalid multipart form: "+err.Error(),
			"invalid_request_error", codeInvalidUpload,
		))
		return
	}
	defer r.MultipartForm.RemoveAll() // clean up temp files from multipart parsing

	file, header, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, NewErrorResponse(
			"missing 'file' field: "+err.Error(),
			"invalid_request_error", codeMissingFile,
		))
		return
	}
	defer file.Close()

	purpose := r.FormValue("purpose")
	if purpose == "" {
		purpose = "user_data"
	}

	meta, err := h.fileStore.Save(header.Filename, purpose, file)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, NewErrorResponse(
			"failed to save file: "+err.Error(), "server_error", codeSaveFailed,
		))
		return
	}

	writeJSON(w, http.StatusOK, meta.toFileObject())
}

// handleListFiles handles GET /v1/files.
func (h *Handler) handleListFiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, NewErrorResponse(
			"method not allowed", "invalid_request_error", codeMethodNotAllowed,
		))
		return
	}
	files := h.fileStore.List()
	data := make([]FileObject, len(files))
	for i, m := range files {
		data[i] = m.toFileObject()
	}
	writeJSON(w, http.StatusOK, FileList{Object: "list", Data: data})
}

// handleFileByID handles GET /v1/files/{id} and DELETE /v1/files/{id}.
func (h *Handler) handleFileByID(w http.ResponseWriter, r *http.Request) {
	// Extract file ID from path: /v1/files/{id}
	id := strings.TrimPrefix(r.URL.Path, "/v1/files/")
	if id == "" || strings.Contains(id, "/") {
		writeJSON(w, http.StatusBadRequest, NewErrorResponse(
			"invalid file id", "invalid_request_error", codeInvalidID,
		))
		return
	}

	switch r.Method {
	case http.MethodGet:
		meta, ok := h.fileStore.Get(id)
		if !ok {
			writeJSON(w, http.StatusNotFound, NewErrorResponse(
				"file not found", "invalid_request_error", codeNotFound,
			))
			return
		}
		writeJSON(w, http.StatusOK, meta.toFileObject())

	case http.MethodDelete:
		if h.fileStore.Delete(id) {
			writeJSON(w, http.StatusOK, FileDeleteResponse{
				ID: id, Object: "file", Deleted: true,
			})
		} else {
			writeJSON(w, http.StatusNotFound, NewErrorResponse(
				"file not found", "invalid_request_error", codeNotFound,
			))
		}

	default:
		writeJSON(w, http.StatusMethodNotAllowed, NewErrorResponse(
			"method not allowed", "invalid_request_error", codeMethodNotAllowed,
		))
	}
}
