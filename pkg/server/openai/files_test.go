package openai

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"rcodegen/pkg/server"
)

// newTestFileStore creates a FileStore in a temp directory that is cleaned up
// when the test finishes.
func newTestFileStore(t *testing.T) *FileStore {
	t.Helper()
	dir := t.TempDir()
	fs, err := NewFileStore(dir)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	t.Cleanup(fs.Stop)
	return fs
}

// newTestHandler returns a Handler wired to a temporary FileStore.
func newTestHandler(t *testing.T) (*Handler, *FileStore) {
	t.Helper()
	fs := newTestFileStore(t)
	h := NewHandler(nil, nil, server.NewRunRegistry(5), nil, fs, nil)
	return h, fs
}

// multipartUpload builds a multipart request body with the given filename and content.
func multipartUpload(t *testing.T, filename, purpose string, content []byte) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, err := w.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	part.Write(content)
	if purpose != "" {
		w.WriteField("purpose", purpose)
	}
	w.Close()
	return &buf, w.FormDataContentType()
}

func TestUploadFile(t *testing.T) {
	h, _ := newTestHandler(t)

	body, ct := multipartUpload(t, "hello.txt", "assistants", []byte("hello world"))
	req := httptest.NewRequest(http.MethodPost, "/v1/files", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var fo FileObject
	if err := json.NewDecoder(rec.Body).Decode(&fo); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if fo.Object != "file" {
		t.Errorf("expected Object='file', got %q", fo.Object)
	}
	if fo.Filename != "hello.txt" {
		t.Errorf("expected Filename='hello.txt', got %q", fo.Filename)
	}
	if fo.Bytes != 11 {
		t.Errorf("expected Bytes=11, got %d", fo.Bytes)
	}
	if fo.Purpose != "assistants" {
		t.Errorf("expected Purpose='assistants', got %q", fo.Purpose)
	}
	if fo.Path == "" {
		t.Error("expected non-empty Path")
	}
	// Verify file exists on disk
	if _, err := os.Stat(fo.Path); err != nil {
		t.Errorf("file not on disk: %v", err)
	}
}

func TestUploadFile_DefaultPurpose(t *testing.T) {
	h, _ := newTestHandler(t)

	body, ct := multipartUpload(t, "data.csv", "", []byte("a,b,c"))
	req := httptest.NewRequest(http.MethodPost, "/v1/files", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var fo FileObject
	json.NewDecoder(rec.Body).Decode(&fo)
	if fo.Purpose != "user_data" {
		t.Errorf("expected default purpose 'user_data', got %q", fo.Purpose)
	}
}

func TestListFiles(t *testing.T) {
	h, fs := newTestHandler(t)

	// Upload two files
	fs.Save("a.txt", "user_data", bytes.NewReader([]byte("aaa")))
	fs.Save("b.txt", "user_data", bytes.NewReader([]byte("bbb")))

	req := httptest.NewRequest(http.MethodGet, "/v1/files", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var fl FileList
	json.NewDecoder(rec.Body).Decode(&fl)
	if fl.Object != "list" {
		t.Errorf("expected Object='list', got %q", fl.Object)
	}
	if len(fl.Data) != 2 {
		t.Errorf("expected 2 files, got %d", len(fl.Data))
	}
}

func TestGetFileByID(t *testing.T) {
	h, fs := newTestHandler(t)

	meta, _ := fs.Save("test.txt", "user_data", bytes.NewReader([]byte("content")))

	req := httptest.NewRequest(http.MethodGet, "/v1/files/"+meta.ID, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var fo FileObject
	json.NewDecoder(rec.Body).Decode(&fo)
	if fo.ID != meta.ID {
		t.Errorf("expected ID=%q, got %q", meta.ID, fo.ID)
	}
}

func TestGetFileByID_NotFound(t *testing.T) {
	h, _ := newTestHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/files/file-nonexistent", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestDeleteFile(t *testing.T) {
	h, fs := newTestHandler(t)

	meta, _ := fs.Save("todelete.txt", "user_data", bytes.NewReader([]byte("bye")))
	diskPath := meta.Path

	req := httptest.NewRequest(http.MethodDelete, "/v1/files/"+meta.ID, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var dr FileDeleteResponse
	json.NewDecoder(rec.Body).Decode(&dr)
	if !dr.Deleted {
		t.Error("expected Deleted=true")
	}
	// Verify file is gone from disk
	if _, err := os.Stat(diskPath); !os.IsNotExist(err) {
		t.Error("expected file to be removed from disk")
	}
	// Verify file is gone from store
	if _, ok := fs.Get(meta.ID); ok {
		t.Error("expected file to be removed from store")
	}
}

func TestDeleteFile_NotFound(t *testing.T) {
	h, _ := newTestHandler(t)

	req := httptest.NewRequest(http.MethodDelete, "/v1/files/file-nonexistent", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestUploadFile_MissingFileField(t *testing.T) {
	h, _ := newTestHandler(t)

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	w.WriteField("purpose", "assistants")
	w.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/files", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestSanitizeFilename(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"hello.txt", "hello.txt"},
		{"../../etc/passwd", "passwd"},
		{"my file (1).txt", "my_file__1_.txt"},
		{"", "upload"},
		{"..", "upload"},
	}
	for _, tc := range tests {
		got := sanitizeFilename(tc.input)
		if got != tc.want {
			t.Errorf("sanitizeFilename(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestPurgeExpired(t *testing.T) {
	dir := t.TempDir()
	fs, err := NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Stop()

	meta, _ := fs.Save("old.txt", "user_data", bytes.NewReader([]byte("old data")))

	// Backdate the file's creation time
	fs.mu.Lock()
	fs.files[meta.ID].CreatedAt = time.Now().Add(-25 * time.Hour)
	fs.mu.Unlock()

	fs.purgeExpired()

	if _, ok := fs.Get(meta.ID); ok {
		t.Error("expected expired file to be purged")
	}
	if _, err := os.Stat(meta.Path); !os.IsNotExist(err) {
		t.Error("expected expired file to be removed from disk")
	}
}

func TestRecoverFromDisk(t *testing.T) {
	dir := t.TempDir()

	// Manually create a file matching the expected naming convention.
	id := "file-aabbccddeeff00112233aabb"
	name := id + "-recovered.txt"
	os.WriteFile(filepath.Join(dir, name), []byte("recovered"), 0o644)

	fs, err := NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Stop()

	meta, ok := fs.Get(id)
	if !ok {
		t.Fatal("expected recovered file to be in store")
	}
	if meta.Filename != "recovered.txt" {
		t.Errorf("expected Filename='recovered.txt', got %q", meta.Filename)
	}
}
