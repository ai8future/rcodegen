package jev

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"rcodegen/pkg/settings"
)

const (
	systemOnePath        = "/v1/systemone"
	maxUpstreamBodyBytes = 1 << 20
	pricePerInputToken   = 0.042 / 1_000_000
	defaultRetryAttempts = 3
)

var (
	ErrNotConfigured = errors.New("jev: not configured")
	ErrStateTooLarge = errors.New("jev: state too large")
)

// APIError reports a non-200 upstream response or exhausted transport failure.
type APIError struct {
	Status    int
	Message   string
	Retryable bool
}

func (e *APIError) Error() string {
	if e == nil {
		return ""
	}
	if e.Status > 0 {
		return fmt.Sprintf("jev upstream status %d: %s", e.Status, e.Message)
	}
	return "jev upstream error: " + e.Message
}

// Sleeper lets tests synchronize retry backoff and cancellation.
type Sleeper func(context.Context, time.Duration) error

// Sleep waits for d or context cancellation.
func Sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Client calls the TypeSafe System One API.
type Client struct {
	baseURL    string
	model      string
	apiKey     string
	httpClient *http.Client
	attempts   int
}

// NewClient builds a Jev client from settings. TYPESAFE_API_KEY is resolved
// here and never copied into settings.
func NewClient(defaults settings.JevDefaults) (*Client, error) {
	fillJevDefaults(&defaults)
	key := defaults.ResolveAPIKey()
	if key == "" {
		return nil, ErrNotConfigured
	}
	baseURL, err := validateBaseURL(defaults.BaseURL)
	if err != nil {
		return nil, err
	}
	timeout := time.Duration(defaults.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = time.Duration(settings.DefaultJevTimeoutSeconds) * time.Second
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &Client{
		baseURL: baseURL,
		model:   defaults.Model,
		apiKey:  key,
		httpClient: &http.Client{
			Timeout:   timeout,
			Transport: transport,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		attempts: defaultRetryAttempts,
	}, nil
}

func validateBaseURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("jev: invalid base_url: %w", err)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return "", fmt.Errorf("jev: base_url must use https, or http on loopback")
	}
	if u.Host == "" {
		return "", errors.New("jev: base_url must include a host")
	}
	if u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("jev: base_url must not include userinfo, path, query, or fragment")
	}
	if u.Scheme == "http" && !isLoopbackHost(u.Hostname()) {
		return "", errors.New("jev: http base_url is only allowed for loopback hosts")
	}
	return strings.TrimRight(u.String(), "/"), nil
}

func isLoopbackHost(host string) bool {
	host = strings.Trim(strings.ToLower(host), "[]")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func fillJevDefaults(defaults *settings.JevDefaults) {
	if defaults.BaseURL == "" {
		defaults.BaseURL = settings.DefaultJevBaseURL
	}
	if defaults.Model == "" {
		defaults.Model = settings.DefaultJevModel
	}
	if defaults.TimeoutSeconds <= 0 {
		defaults.TimeoutSeconds = settings.DefaultJevTimeoutSeconds
	}
}

// Model returns the configured default model.
func (c *Client) Model() string {
	if c == nil || c.model == "" {
		return settings.DefaultJevModel
	}
	return c.model
}

// Decide validates and submits a decision request. Callers own state scrubbing
// and optional capping so their byte counts stay stable.
func (c *Client) Decide(ctx context.Context, model, state string, questions map[string]Question) (*Response, error) {
	return c.DecideWithSleep(ctx, model, state, questions, Sleep)
}

// DecideWithSleep is Decide with injectable retry sleeping for deterministic tests.
func (c *Client) DecideWithSleep(ctx context.Context, model, state string, questions map[string]Question, sleeper Sleeper) (*Response, error) {
	if c == nil || c.apiKey == "" {
		return nil, ErrNotConfigured
	}
	if sleeper == nil {
		sleeper = Sleep
	}
	if err := ValidateQuestions(questions); err != nil {
		return nil, c.redactErr(err)
	}
	model = strings.TrimSpace(model)
	if model == "" {
		model = c.Model()
	}
	if model != c.Model() && !strings.HasPrefix(model, "jev-") {
		return nil, fmt.Errorf("jev: model %q is not allowed", model)
	}
	if strings.TrimSpace(state) == "" {
		return nil, errors.New("jev: state is required")
	}
	if budget := StateBudget(questions); budget <= 0 || len(state) > budget {
		return nil, fmt.Errorf("%w: %d bytes exceeds budget %d", ErrStateTooLarge, len(state), budget)
	}

	body, err := json.Marshal(Request{Model: model, State: state, Questions: questions})
	if err != nil {
		return nil, c.redactErr(err)
	}
	var last error
	for attempt := 0; attempt < c.attempts; attempt++ {
		resp, err := c.do(ctx, body, questions)
		if err == nil {
			return resp, nil
		}
		err = c.redactErr(err)
		if errors.Is(err, ErrInvalidResponse) || !isRetryable(err) || attempt == c.attempts-1 {
			return nil, err
		}
		last = err
		if sleepErr := sleeper(ctx, time.Duration(100*(1<<attempt))*time.Millisecond); sleepErr != nil {
			return nil, sleepErr
		}
	}
	if last != nil {
		return nil, last
	}
	return nil, context.Canceled
}

func (c *Client) do(ctx context.Context, body []byte, questions map[string]Question) (*Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+systemOnePath, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	res, err := c.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &APIError{Message: err.Error(), Retryable: true}
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, maxUpstreamBodyBytes+1))
	if err != nil {
		return nil, &APIError{Status: res.StatusCode, Message: err.Error(), Retryable: shouldRetryStatus(res.StatusCode)}
	}
	if len(data) > maxUpstreamBodyBytes {
		if res.StatusCode >= 200 && res.StatusCode < 300 {
			return nil, fmt.Errorf("%w: upstream response exceeds %d bytes", ErrInvalidResponse, maxUpstreamBodyBytes)
		}
		data = data[:maxUpstreamBodyBytes]
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, &APIError{Status: res.StatusCode, Message: upstreamMessage(data), Retryable: shouldRetryStatus(res.StatusCode)}
	}
	var out Response
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("%w: malformed JSON: %v", ErrInvalidResponse, err)
	}
	if err := ValidateResponse(questions, &out); err != nil {
		return nil, err
	}
	if out.Model == "" {
		out.Model = c.Model()
	}
	return &out, nil
}

func shouldRetryStatus(status int) bool {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusUnprocessableEntity:
		return false
	case http.StatusTooManyRequests, 529:
		return true
	}
	return status >= 500
}

func upstreamMessage(data []byte) string {
	var payload struct {
		Error   any    `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(data, &payload) == nil {
		switch e := payload.Error.(type) {
		case string:
			if e != "" {
				return e
			}
		case map[string]any:
			if msg, _ := e["message"].(string); msg != "" {
				return msg
			}
		}
		if payload.Message != "" {
			return payload.Message
		}
	}
	msg := strings.TrimSpace(string(data))
	if msg == "" {
		return "upstream returned no error body"
	}
	if len(msg) > 512 {
		msg = msg[:512]
	}
	return msg
}

func isRetryable(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Retryable
}

func (c *Client) redactErr(err error) error {
	if err == nil || c == nil || c.apiKey == "" {
		return err
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrNotConfigured) {
		return err
	}
	redacted := strings.ReplaceAll(err.Error(), c.apiKey, "[redacted]")
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return &APIError{Status: apiErr.Status, Message: strings.ReplaceAll(apiErr.Message, c.apiKey, "[redacted]"), Retryable: apiErr.Retryable}
	}
	if errors.Is(err, ErrInvalidResponse) {
		return fmt.Errorf("%w: %s", ErrInvalidResponse, strings.TrimPrefix(redacted, ErrInvalidResponse.Error()+": "))
	}
	if errors.Is(err, ErrStateTooLarge) {
		return fmt.Errorf("%w: %s", ErrStateTooLarge, strings.TrimPrefix(redacted, ErrStateTooLarge.Error()+": "))
	}
	return errors.New(redacted)
}

// CostUSD prices Jev input tokens. Output tokens are free.
func CostUSD(inputTokens int) float64 {
	if inputTokens <= 0 {
		return 0
	}
	return float64(inputTokens) * pricePerInputToken
}
