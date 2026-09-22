package jev

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"rcodegen/pkg/settings"
)

func TestClientDecideSuccess(t *testing.T) {
	t.Setenv(settings.JevAPIKeyEnv, "")
	questions := choiceQuestions()
	var gotAuth string
	var gotReq Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if r.URL.Path != systemOnePath {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotReq); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		fmt.Fprint(w, `{"model":"jev-1.13.0","answers":{"verdict":{"type":"choice","choice":"complete","probabilities":{"complete":1,"incomplete":0},"confidence":0.91}},"usage":{"input_tokens":123,"output_tokens":0}}`)
	}))
	defer srv.Close()

	c, err := NewClient(settings.JevDefaults{BaseURL: srv.URL, APIKey: "file-key"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	resp, err := c.Decide(context.Background(), "", "state", questions)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if gotAuth != "Bearer file-key" {
		t.Fatalf("Authorization header not set")
	}
	if gotReq.Model != settings.DefaultJevModel || gotReq.State != "state" {
		t.Fatalf("request = %+v", gotReq)
	}
	if resp.Answers["verdict"].Value() != "complete" || resp.Usage.InputTokens != 123 {
		t.Fatalf("response = %+v", resp)
	}
	if got := CostUSD(resp.Usage.InputTokens); got <= 0 {
		t.Fatalf("CostUSD = %v", got)
	}
}

func TestClientRejectsNullProbabilityAsInvalidResponseWithoutRetry(t *testing.T) {
	t.Setenv(settings.JevAPIKeyEnv, "")
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, `{"model":"jev-1.13.0","answers":{"verdict":{"type":"choice","choice":"incomplete","probabilities":{"complete":null,"incomplete":1},"confidence":0.5}}}`)
	}))
	defer srv.Close()

	c, err := NewClient(settings.JevDefaults{BaseURL: srv.URL, APIKey: "file-key"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	_, err = c.Decide(context.Background(), "", "state", choiceQuestions())
	if !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("err = %v, want ErrInvalidResponse", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("invalid response retried %d times", calls.Load())
	}
}

func TestClientMissingQuestionFixtureHasSingleDefect(t *testing.T) {
	t.Setenv(settings.JevAPIKeyEnv, "")
	var calls atomic.Int32
	qs := map[string]Question{
		"done":    {Type: TypeNoul, Instructions: "x"},
		"verdict": {Type: TypeChoice, Instructions: "x", Criteria: json.RawMessage(`{"complete":"c","incomplete":"i"}`)},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, `{"model":"jev-1.13.0","answers":{"verdict":{"type":"choice","choice":"complete","probabilities":{"complete":1,"incomplete":0},"confidence":0.7}}}`)
	}))
	defer srv.Close()

	c, err := NewClient(settings.JevDefaults{BaseURL: srv.URL, APIKey: "file-key"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	_, err = c.Decide(context.Background(), "", "state", qs)
	if !errors.Is(err, ErrInvalidResponse) || !strings.Contains(err.Error(), `"done"`) {
		t.Fatalf("err = %v, want missing done invalid response", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("invalid response retried %d times", calls.Load())
	}
}

func TestClientPermanentUpstreamFailuresPreserveRetryableFalseAndRedactKey(t *testing.T) {
	t.Setenv(settings.JevAPIKeyEnv, "")
	for _, status := range []int{http.StatusForbidden, http.StatusNotFound} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(status)
				fmt.Fprint(w, `{"error":{"message":"secret-key is rejected"}}`)
			}))
			defer srv.Close()

			c, err := NewClient(settings.JevDefaults{BaseURL: srv.URL, APIKey: "secret-key"})
			if err != nil {
				t.Fatalf("NewClient: %v", err)
			}
			_, err = c.Decide(context.Background(), "", "state", choiceQuestions())
			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("err = %T %v, want APIError", err, err)
			}
			if apiErr.Retryable {
				t.Fatalf("Retryable = true for status %d", status)
			}
			if strings.Contains(err.Error(), "secret-key") || strings.Contains(apiErr.Message, "secret-key") {
				t.Fatalf("API key was not redacted: %v", err)
			}
			if calls.Load() != 1 {
				t.Fatalf("permanent failure retried %d times", calls.Load())
			}
		})
	}
}

func TestClientRedactsValidationErrors(t *testing.T) {
	t.Setenv(settings.JevAPIKeyEnv, "")
	c, err := NewClient(settings.JevDefaults{BaseURL: "http://127.0.0.1", APIKey: "secret-key"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	_, err = c.Decide(context.Background(), "", "state", map[string]Question{
		"secret-key": {Type: "essay", Instructions: "x"},
	})
	if err == nil {
		t.Fatal("expected validation error")
	}
	if strings.Contains(err.Error(), "secret-key") {
		t.Fatalf("validation error leaked key: %v", err)
	}
}

func TestClientRetriesTransientThenSucceeds(t *testing.T) {
	t.Setenv(settings.JevAPIKeyEnv, "")
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"error":{"message":"slow down"}}`)
			return
		}
		fmt.Fprint(w, `{"model":"jev-1.13.0","answers":{"verdict":{"type":"choice","choice":"complete","probabilities":{"complete":1,"incomplete":0},"confidence":0.91}}}`)
	}))
	defer srv.Close()

	c, err := NewClient(settings.JevDefaults{BaseURL: srv.URL, APIKey: "file-key"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	resp, err := c.DecideWithSleep(context.Background(), "", "state", choiceQuestions(), func(context.Context, time.Duration) error { return nil })
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if resp.Answers["verdict"].Choice != "complete" || calls.Load() != 2 {
		t.Fatalf("response/calls = %+v/%d", resp, calls.Load())
	}
}

func TestClientCancellationDuringBackoff(t *testing.T) {
	t.Setenv(settings.JevAPIKeyEnv, "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"error":{"message":"retry later"}}`)
	}))
	defer srv.Close()

	c, err := NewClient(settings.JevDefaults{BaseURL: srv.URL, APIKey: "file-key"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	entered := make(chan struct{})
	sleeper := func(ctx context.Context, d time.Duration) error {
		close(entered)
		<-ctx.Done()
		return ctx.Err()
	}
	errCh := make(chan error, 1)
	go func() {
		_, err := c.DecideWithSleep(ctx, "", "state", choiceQuestions(), sleeper)
		errCh <- err
	}()
	<-entered
	cancel()
	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestClientCancellationDuringInFlightRequest(t *testing.T) {
	t.Setenv(settings.JevAPIKeyEnv, "")
	entered := make(chan struct{})
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
	}))
	defer srv.Close()
	defer close(release)

	c, err := NewClient(settings.JevDefaults{BaseURL: srv.URL, APIKey: "file-key"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, err := c.Decide(ctx, "", "state", choiceQuestions())
		errCh <- err
	}()
	<-entered
	cancel()
	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestClientRejectsStateTooLarge(t *testing.T) {
	t.Setenv(settings.JevAPIKeyEnv, "")
	c, err := NewClient(settings.JevDefaults{BaseURL: "http://127.0.0.1", APIKey: "file-key"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	_, err = c.Decide(context.Background(), "", strings.Repeat("x", MaxStateBytes), choiceQuestions())
	if !errors.Is(err, ErrStateTooLarge) {
		t.Fatalf("err = %v, want ErrStateTooLarge", err)
	}
}

func TestNewClientUsesEnvKeyWithoutMutatingSettings(t *testing.T) {
	t.Setenv(settings.JevAPIKeyEnv, "env-key")
	defaults := settings.JevDefaults{BaseURL: "http://127.0.0.1", APIKey: "file-key"}
	c, err := NewClient(defaults)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if c.apiKey != "env-key" {
		t.Fatalf("api key = %q, want env", c.apiKey)
	}
	if defaults.APIKey != "file-key" {
		t.Fatalf("settings mutated: %+v", defaults)
	}
}

func TestNewClientValidatesBaseURLAndDisablesProxy(t *testing.T) {
	t.Setenv(settings.JevAPIKeyEnv, "")
	valid := []string{
		"https://api.typesafe.ai",
		"http://127.0.0.1:8080",
		"http://localhost:8080",
		"http://[::1]:8080",
	}
	for _, baseURL := range valid {
		t.Run(baseURL, func(t *testing.T) {
			c, err := NewClient(settings.JevDefaults{BaseURL: baseURL, APIKey: "file-key"})
			if err != nil {
				t.Fatalf("NewClient(%q): %v", baseURL, err)
			}
			tr, ok := c.httpClient.Transport.(*http.Transport)
			if !ok || tr.Proxy != nil {
				t.Fatalf("transport proxy was not disabled: %#v", c.httpClient.Transport)
			}
		})
	}
	invalid := []string{
		"http://example.com",
		"https://api.typesafe.ai/v1",
		"https://user:pass@api.typesafe.ai",
		"https://api.typesafe.ai?x=1",
		"https://api.typesafe.ai#frag",
		"ftp://api.typesafe.ai",
	}
	for _, baseURL := range invalid {
		t.Run(baseURL, func(t *testing.T) {
			if _, err := NewClient(settings.JevDefaults{BaseURL: baseURL, APIKey: "file-key"}); err == nil {
				t.Fatalf("NewClient(%q) succeeded, want error", baseURL)
			}
		})
	}
}

func TestClientRejectsRedirects(t *testing.T) {
	t.Setenv(settings.JevAPIKeyEnv, "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/elsewhere", http.StatusFound)
	}))
	defer srv.Close()

	c, err := NewClient(settings.JevDefaults{BaseURL: srv.URL, APIKey: "file-key"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	_, err = c.Decide(context.Background(), "", "state", choiceQuestions())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusFound || apiErr.Retryable {
		t.Fatalf("err = %#v, want nonretryable 302 APIError", err)
	}
}

func TestClientRejectsOversizedSuccessBody(t *testing.T) {
	t.Setenv(settings.JevAPIKeyEnv, "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"model":"jev-1.13.0","answers":{"verdict":{"type":"choice","choice":"complete","probabilities":{"complete":1,"incomplete":0},"confidence":0.91}}}`))
		w.Write([]byte(strings.Repeat(" ", maxUpstreamBodyBytes)))
	}))
	defer srv.Close()

	c, err := NewClient(settings.JevDefaults{BaseURL: srv.URL, APIKey: "file-key"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	_, err = c.Decide(context.Background(), "", "state", choiceQuestions())
	if !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("err = %v, want ErrInvalidResponse", err)
	}
}

func choiceQuestions() map[string]Question {
	return map[string]Question{
		"verdict": {Type: TypeChoice, Instructions: "x", Criteria: json.RawMessage(`{"complete":"c","incomplete":"i"}`)},
	}
}
