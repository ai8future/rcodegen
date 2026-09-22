package gemini

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"rcodegen/pkg/runner"
)

// imageModel returns a model name that routes through the direct API path.
func imageModel(t *testing.T) string {
	t.Helper()
	for name := range imageModels {
		return name
	}
	t.Fatal("no image models registered")
	return ""
}

// useTestEndpoint points the direct API path at a local server for one test.
func useTestEndpoint(t *testing.T, url string) {
	t.Helper()
	restore := apiBaseURL
	apiBaseURL = url
	t.Cleanup(func() { apiBaseURL = restore })
}

// TestRunDirectAPI_CancellationAbortsRequest pins the behaviour a server run
// depends on: an unanswered API call must end when the client's context does,
// not when the API eventually replies.
func TestRunDirectAPI_CancellationAbortsRequest(t *testing.T) {
	released := make(chan struct{})
	reached := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(reached)
		<-released // never answer until the test says so
	}))
	defer srv.Close()
	defer close(released)

	useTestEndpoint(t, srv.URL)
	t.Setenv("GEMINI_API_KEY", "test-key")

	cfg := runner.NewConfig()
	cfg.Model = imageModel(t)
	var out bytes.Buffer
	cfg.Output = &out

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	workDir := t.TempDir()
	exit := make(chan int, 1)
	go func() {
		exit <- New().RunDirectAPI(ctx, cfg, workDir, "draw a cat")
	}()

	select {
	case <-reached:
	case <-time.After(10 * time.Second):
		t.Fatal("request never reached the server")
	}

	cancel()
	select {
	case code := <-exit:
		if code == 0 {
			t.Errorf("exit code = 0, want non-zero for an aborted request")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("RunDirectAPI outlived its cancelled context")
	}
}

// TestRunDirectAPI_UsesConfiguredEndpoint covers the ordinary path: the request
// is built with a context and carries the JSON body the API expects.
func TestRunDirectAPI_UsesConfiguredEndpoint(t *testing.T) {
	var gotMethod, gotContentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotContentType = r.Header.Get("Content-Type")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"a cat"}]}}]}`))
	}))
	defer srv.Close()

	useTestEndpoint(t, srv.URL)
	t.Setenv("GEMINI_API_KEY", "test-key")

	cfg := runner.NewConfig()
	cfg.Model = imageModel(t)
	var out bytes.Buffer
	cfg.Output = &out

	if code := New().RunDirectAPI(context.Background(), cfg, t.TempDir(), "draw a cat"); code != 0 {
		t.Fatalf("exit code = %d, want 0; output: %s", code, out.String())
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotContentType != "application/json" {
		t.Errorf("content-type = %q, want application/json", gotContentType)
	}
	if !strings.Contains(out.String(), "a cat") {
		t.Errorf("output %q does not contain the model's text", out.String())
	}
}

func TestRedactKey(t *testing.T) {
	msg := `Post "http://example/models/x:generateContent?key=super-secret": dial error`
	got := redactKey(msg, "super-secret")
	if strings.Contains(got, "super-secret") {
		t.Errorf("redactKey left the key in %q", got)
	}
	if !strings.Contains(got, "REDACTED") {
		t.Errorf("redactKey did not mark the redaction in %q", got)
	}
	if got := redactKey(msg, ""); got != msg {
		t.Errorf("redactKey with no key changed the message")
	}
}
