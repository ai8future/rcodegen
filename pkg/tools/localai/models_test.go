package localai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestListAvailableModels(t *testing.T) {
	tests := []struct {
		name, path, body string
		tool             *Tool
		want             []string
	}{
		{"ollama", "/api/tags", `{"models":[{"name":"z"},{"name":"a"},{"name":"a"},{"name":""}]}`, NewOllama(), []string{"a", "z"}},
		{"lmstudio", "/v1/models", `{"data":[{"id":"b"},{"id":"a"},{"id":"b"}]}`, NewLMStudio(), []string{"a", "b"}},
	}
	for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != tc.path {
						t.Errorf("path = %q, want %q", r.URL.Path, tc.path)
					}
					if auth := r.Header.Get("Authorization"); auth != "" {
						t.Errorf("unexpected Authorization header %q", auth)
					}
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			configureTool(tc.tool, server.URL, "")
			got, err := tc.tool.ListAvailableModels(context.Background())
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("models = %v, err = %v, want %v", got, err, tc.want)
			}
		})
	}
}

func TestListAvailableModelsEmptyAndAuthenticated(t *testing.T) {
	for _, tc := range []struct {
		name, path, body string
		tool             *Tool
	}{
		{"ollama", "/api/tags", `{"models":[]}`, NewOllama()},
		{"lmstudio", "/v1/models", `{"data":[]}`, NewLMStudio()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != tc.path || r.Header.Get("Authorization") != "Bearer inventory-secret" {
					t.Errorf("request path/auth = %q %q", r.URL.Path, r.Header.Get("Authorization"))
				}
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			configureTool(tc.tool, server.URL, "")
			if tc.tool.flavor == FlavorOllama {
				tc.tool.settings.Defaults.Ollama.APIKey = "inventory-secret"
			} else {
				tc.tool.settings.Defaults.LMStudio.APIKey = "inventory-secret"
			}
			got, err := tc.tool.ListAvailableModels(context.Background())
			if err != nil || len(got) != 0 {
				t.Fatalf("models = %v, err = %v", got, err)
			}
		})
	}
}

func TestListAvailableModelsErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		code int
		body string
	}{{"non-200", 500, `{}`}, {"malformed", 200, `{`}} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.code)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			tool := NewOllama()
			configureTool(tool, server.URL, "")
			if _, err := tool.ListAvailableModels(context.Background()); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestListAvailableModelsTransportCancellationAndOversize(t *testing.T) {
	for _, newTool := range []func() *Tool{NewOllama, NewLMStudio} {
		tool := newTool()
		configureTool(tool, "http://127.0.0.1:1", "")
		if _, err := tool.ListAvailableModels(context.Background()); err == nil {
			t.Fatal("unreachable runtime returned success")
		}

		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := tool.ListAvailableModels(ctx); err == nil {
			t.Fatal("cancelled inventory returned success")
		}
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", maxResponseBytes+1)))
	}))
	defer server.Close()
	tool := NewOllama()
	configureTool(tool, server.URL, "")
	if _, err := tool.ListAvailableModels(context.Background()); err == nil || !strings.Contains(err.Error(), "32 MiB") {
		t.Fatalf("oversize error = %v", err)
	}
}
