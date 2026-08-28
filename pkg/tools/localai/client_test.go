package localai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"rcodegen/pkg/settings"
)

func TestValidateOriginPolicy(t *testing.T) {
	tests := []struct {
		name, raw string
		allow     bool
		wantErr   bool
	}{
		{"localhost", "http://localhost:11434", false, false},
		{"IPv4 loopback", "http://127.0.0.1:11434", false, false},
		{"IPv6 loopback", "http://[::1]:11434", false, false},
		{"private", "http://192.168.1.2:11434", false, false},
		{"public IP", "https://8.8.8.8", false, true},
		{"DNS", "https://models.example", false, true},
		{"remote allowed", "https://models.example", true, false},
		{"unspecified IPv4", "http://0.0.0.0:11434", true, true},
		{"unspecified IPv6", "http://[::]:11434", true, true},
		{"credentials", "http://user:pass@localhost:11434", false, true},
		{"path", "http://localhost:11434/v1", false, true},
		{"query", "http://localhost:11434?x=1", false, true},
		{"empty query", "http://localhost:11434?", false, true},
		{"fragment", "http://localhost:11434/#x", false, true},
		{"bad scheme", "ftp://localhost:11434", false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := validateOrigin(settings.LocalAIDefaults{BaseURL: tc.raw, AllowRemote: tc.allow})
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateOrigin(%q) error = %v, wantErr %v", tc.raw, err, tc.wantErr)
			}
		})
	}
}

func TestHTTPClientDisablesProxyAndRedirects(t *testing.T) {
	client := newHTTPClient()
	if client.Transport == nil {
		t.Fatal("missing transport")
	}
	if err := client.CheckRedirect(nil, nil); err == nil {
		t.Fatal("redirect accepted")
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok || transport.Proxy != nil {
		t.Fatal("ambient proxy support was not disabled")
	}
}

func TestHTTPClientRejectsActualRedirect(t *testing.T) {
	targetReached := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/target" {
			targetReached = true
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.Redirect(w, r, "/target", http.StatusFound)
	}))
	defer server.Close()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := newHTTPClient().Do(req)
	if err == nil || !strings.Contains(err.Error(), "redirects are not allowed") {
		if resp != nil {
			_ = resp.Body.Close()
		}
		t.Fatalf("redirect error = %v", err)
	}
	if targetReached {
		t.Fatal("redirect target was reached")
	}
}
