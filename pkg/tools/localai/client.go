package localai

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
	"unicode"

	"rcodegen/pkg/runner"
	"rcodegen/pkg/settings"
)

const (
	maxResponseBytes   = 32 << 20
	maxDiagnosticBytes = 64 << 10
)

var errResponseTooLarge = errors.New("local runtime response exceeds 32 MiB limit")

func validateOrigin(cfg settings.LocalAIDefaults) (*url.URL, error) {
	u, err := url.Parse(cfg.BaseURL)
	if err != nil {
		return nil, errors.New("invalid local runtime base URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, errors.New("local runtime base URL must use http or https")
	}
	if u.Host == "" || u.Hostname() == "" {
		return nil, errors.New("local runtime base URL must include a host")
	}
	if u.User != nil {
		return nil, errors.New("local runtime base URL must not include credentials")
	}
	if u.Path != "" && u.Path != "/" {
		return nil, errors.New("local runtime base URL must be an origin without a path")
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return nil, errors.New("local runtime base URL must not include a query or fragment")
	}

	host := u.Hostname()
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsUnspecified() {
			return nil, errors.New("local runtime base URL must use a connectable address")
		}
		if !cfg.AllowRemote && !ip.IsLoopback() && !ip.IsPrivate() {
			return nil, errors.New("remote local-runtime address requires allow_remote")
		}
	} else if !strings.EqualFold(host, "localhost") && !cfg.AllowRemote {
		return nil, errors.New("remote local-runtime hostname requires allow_remote")
	}

	u.Path = ""
	u.RawPath = ""
	return u, nil
}

func newHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("local runtime redirects are not allowed")
		},
	}
}

var directHTTPClient = newHTTPClient()

func (t *Tool) newRequest(ctx context.Context, method, path string, body interface{}) (*http.Request, context.CancelFunc, error) {
	cfg := t.runtimeSettings()
	origin, err := validateOrigin(cfg)
	if err != nil {
		return nil, nil, err
	}
	u := *origin
	u.Path = path

	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return nil, nil, fmt.Errorf("encode local runtime request: %w", err)
		}
		reader = bytes.NewReader(payload)
	}
	requestCtx, cancel := context.WithTimeout(ctx, time.Duration(cfg.TimeoutSeconds)*time.Second)
	req, err := http.NewRequestWithContext(requestCtx, method, u.String(), reader)
	if err != nil {
		cancel()
		return nil, nil, errors.New("create local runtime request")
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}
	return req, cancel, nil
}

func readBoundedResponse(body io.Reader) ([]byte, error) {
	limited := io.LimitReader(body, maxResponseBytes+1)
	payload, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if len(payload) > maxResponseBytes {
		return nil, errResponseTooLarge
	}
	return payload, nil
}

func (t *Tool) diagnostic(cfg *runner.Config, message string) {
	message = sanitizeDiagnostic(message)
	if key := t.runtimeSettings().APIKey; key != "" {
		message = strings.ReplaceAll(message, key, "[redacted]")
	}
	b := runner.NewBoundedBuffer(maxDiagnosticBytes)
	_, _ = b.Write([]byte(message))
	w := cfg.Stderr
	if w == nil {
		w = io.Discard
	}
	_, _ = fmt.Fprintln(w, b.String())
}

func sanitizeDiagnostic(message string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, message)
}

type upstreamError struct {
	Error json.RawMessage `json:"error"`
}

func upstreamErrorMessage(payload []byte) string {
	var response upstreamError
	if json.Unmarshal(payload, &response) != nil || len(response.Error) == 0 {
		return ""
	}
	var text string
	if json.Unmarshal(response.Error, &text) == nil {
		return text
	}
	var detail struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(response.Error, &detail) == nil {
		return detail.Message
	}
	return ""
}
