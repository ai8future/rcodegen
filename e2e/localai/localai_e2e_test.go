//go:build localai_e2e

package localai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rcodegen/pkg/batch"
	"rcodegen/pkg/server/pb"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const requestTimeout = 4 * time.Minute

type e2eConfig struct {
	provider        string
	model           string
	stage           string
	profile         string
	httpBase        string
	grpcAddr        string
	rbatchPath      string
	testHome        string
	expectAvailable bool
}

type modelList struct {
	Data []struct {
		ID        string `json:"id"`
		Dynamic   bool   `json:"dynamic"`
		Default   bool   `json:"default"`
		Available *bool  `json:"available"`
	} `json:"data"`
}

type completionResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
	Error *struct {
		Code string `json:"code"`
	} `json:"error"`
}

func TestLocalRuntimeE2E(t *testing.T) {
	if os.Getenv("RCODEGEN_E2E_LOCALAI") != "1" {
		t.Skip("run through make e2e-localai-smoke or make e2e-localai-full")
	}
	cfg := loadE2EConfig(t)

	t.Run("model_inventory", func(t *testing.T) {
		testModelInventory(t, cfg)
	})
	if cfg.stage == "inventory" {
		return
	}
	if cfg.stage != "runtime" {
		t.Fatalf("unsupported RCODEGEN_E2E_STAGE %q", cfg.stage)
	}

	t.Run("invalid_effort_rejected", func(t *testing.T) {
		testInvalidEffort(t, cfg)
	})
	t.Run("http_completion", func(t *testing.T) {
		testHTTPCompletion(t, cfg, "RCODEGEN_E2E_HTTP_OK")
	})
	if cfg.profile != "full" {
		return
	}
	t.Run("http_stream", func(t *testing.T) {
		testHTTPStream(t, cfg, "RCODEGEN_E2E_STREAM_OK")
	})
	t.Run("grpc_run_task", func(t *testing.T) {
		testGRPCRunTask(t, cfg, "RCODEGEN_E2E_GRPC_OK")
	})
	t.Run("rbatch_local", func(t *testing.T) {
		testRBatch(t, cfg, false, "RCODEGEN_E2E_BATCH_LOCAL_OK")
	})
	t.Run("rbatch_remote", func(t *testing.T) {
		testRBatch(t, cfg, true, "RCODEGEN_E2E_BATCH_REMOTE_OK")
	})
}

func loadE2EConfig(t *testing.T) e2eConfig {
	t.Helper()
	required := func(name string) string {
		value := strings.TrimSpace(os.Getenv(name))
		if value == "" {
			t.Fatalf("%s is required", name)
		}
		return value
	}
	provider := required("RCODEGEN_E2E_PROVIDER")
	if provider != "ollama" && provider != "lmstudio" {
		t.Fatalf("RCODEGEN_E2E_PROVIDER = %q", provider)
	}
	profile := required("RCODEGEN_E2E_PROFILE")
	if profile != "smoke" && profile != "full" {
		t.Fatalf("RCODEGEN_E2E_PROFILE = %q", profile)
	}
	expect := required("RCODEGEN_E2E_EXPECT_AVAILABLE")
	if expect != "true" && expect != "false" {
		t.Fatalf("RCODEGEN_E2E_EXPECT_AVAILABLE = %q", expect)
	}
	return e2eConfig{
		provider: provider, model: required("RCODEGEN_E2E_MODEL"),
		stage: required("RCODEGEN_E2E_STAGE"), profile: profile,
		httpBase: strings.TrimRight(required("RCODEGEN_E2E_HTTP_BASE"), "/"),
		grpcAddr: required("RCODEGEN_E2E_GRPC_ADDR"), rbatchPath: required("RCODEGEN_E2E_RBATCH"),
		testHome: required("RCODEGEN_E2E_TEST_HOME"), expectAvailable: expect == "true",
	}
}

func testModelInventory(t *testing.T, cfg e2eConfig) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.httpBase+"/v1/models", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /v1/models: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET /v1/models = %d: %s", resp.StatusCode, body)
	}
	var list modelList
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	selector := cfg.provider + ":" + cfg.model
	var bare, selected *struct {
		ID        string `json:"id"`
		Dynamic   bool   `json:"dynamic"`
		Default   bool   `json:"default"`
		Available *bool  `json:"available"`
	}
	for i := range list.Data {
		entry := &list.Data[i]
		if entry.ID == cfg.provider {
			bare = entry
		}
		if entry.ID == selector {
			selected = entry
		}
	}
	if bare == nil || !bare.Dynamic || bare.Available != nil {
		t.Fatalf("bare %s model entry = %+v", cfg.provider, bare)
	}
	if selected == nil || !selected.Default || selected.Available == nil {
		t.Fatalf("selected model entry %q = %+v", selector, selected)
	}
	if *selected.Available != cfg.expectAvailable {
		t.Fatalf("selected model availability = %v, want %v", *selected.Available, cfg.expectAvailable)
	}
}

func testInvalidEffort(t *testing.T, cfg e2eConfig) {
	t.Helper()
	body := map[string]interface{}{
		"model":            cfg.provider + ":" + cfg.model,
		"messages":         []map[string]string{{"role": "user", "content": "This request must be rejected before inference."}},
		"reasoning_effort": "ultra",
	}
	resp, payload := postJSON(t, cfg.httpBase+"/v1/chat/completions", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || !bytes.Contains(payload, []byte(`"code":"invalid_effort"`)) {
		t.Fatalf("invalid effort response = %d: %s", resp.StatusCode, payload)
	}
}

func testHTTPCompletion(t *testing.T, cfg e2eConfig, sentinel string) {
	t.Helper()
	body := completionRequest(cfg, sentinel, false)
	resp, payload := postJSON(t, cfg.httpBase+"/v1/chat/completions", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("chat completion = %d: %s", resp.StatusCode, payload)
	}
	var result completionResponse
	if err := json.Unmarshal(payload, &result); err != nil {
		t.Fatal(err)
	}
	if result.Error != nil || result.Model != cfg.provider+":"+cfg.model || len(result.Choices) != 1 {
		t.Fatalf("completion response = %+v", result)
	}
	content := result.Choices[0].Message.Content
	if !strings.Contains(content, sentinel) {
		t.Fatalf("completion %q does not contain sentinel %q", content, sentinel)
	}
	if result.Usage == nil || result.Usage.PromptTokens <= 0 || result.Usage.CompletionTokens <= 0 || result.Usage.TotalTokens <= 0 {
		t.Fatalf("completion usage = %+v", result.Usage)
	}
}

func testHTTPStream(t *testing.T, cfg e2eConfig, sentinel string) {
	t.Helper()
	body := completionRequest(cfg, sentinel, true)
	resp, payload := postJSON(t, cfg.httpBase+"/v1/chat/completions", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream completion = %d: %s", resp.StatusCode, payload)
	}
	if contentType := resp.Header.Get("Content-Type"); !strings.Contains(contentType, "text/event-stream") {
		t.Fatalf("stream Content-Type = %q", contentType)
	}
	text := string(payload)
	if !strings.Contains(text, sentinel) || !strings.Contains(text, "data: [DONE]") || strings.Contains(text, `"error"`) {
		t.Fatalf("unexpected stream response: %s", text)
	}
}

func testGRPCRunTask(t *testing.T, cfg e2eConfig, sentinel string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	conn, err := grpc.NewClient(cfg.grpcAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	effort := ""
	if cfg.provider == "ollama" {
		effort = "max"
	}
	stream, err := pb.NewRServeClient(conn).RunTask(ctx, &pb.RunTaskRequest{
		Tool: cfg.provider, Model: cfg.model, Effort: effort, Task: sentinelPrompt(sentinel),
	})
	if err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	var result *pb.ResultEvent
	for {
		event, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("receive RunTask: %v", err)
		}
		if failure := event.GetError(); failure != nil {
			t.Fatalf("RunTask error event: %+v", failure)
		}
		if text := event.GetText(); text != nil {
			output.WriteString(text.GetContent())
		}
		if final := event.GetResult(); final != nil {
			result = final
			output.WriteString(final.GetOutput())
		}
	}
	if result == nil || result.GetExitCode() != 0 || result.GetOutputTruncated() {
		t.Fatalf("RunTask result = %+v", result)
	}
	if !strings.Contains(output.String(), sentinel) {
		t.Fatalf("RunTask output %q does not contain sentinel %q", output.String(), sentinel)
	}
	usage := result.GetUsage()
	if usage == nil || usage.GetInputTokens() <= 0 || usage.GetOutputTokens() <= 0 {
		t.Fatalf("RunTask usage = %+v", usage)
	}
}

func testRBatch(t *testing.T, cfg e2eConfig, remote bool, sentinel string) {
	t.Helper()
	mode := "local"
	if remote {
		mode = "remote"
	}
	batchName := fmt.Sprintf("localai-e2e-%s-%s-%d", cfg.provider, mode, time.Now().UnixNano())
	manifest := map[string]interface{}{
		"name": batchName, "concurrency": 1,
		"jobs": []map[string]string{{
			"name": "completion", "tool": cfg.provider, "model": cfg.model,
			"effort": providerEffort(cfg.provider), "task": sentinelPrompt(sentinel),
		}},
	}
	manifestPath := filepath.Join(t.TempDir(), "manifest.json")
	payload, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{"run", manifestPath}
	if remote {
		args = append(args, "-server", cfg.grpcAddr)
	}
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, cfg.rbatchPath, args...)
	cmd.Env = replaceEnv(os.Environ(), "HOME", cfg.testHome)
	combined, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("rbatch %s timed out: %s", mode, combined)
	}
	if err != nil {
		t.Fatalf("rbatch %s: %v\n%s", mode, err, combined)
	}
	resultPath := filepath.Join(cfg.testHome, ".rcodegen", "batches", batchName, "results", "completion.json")
	data, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatalf("read persisted rbatch result: %v\n%s", err, combined)
	}
	var result batch.JobResult
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 || result.OutputTruncated || result.Error != "" || !strings.Contains(result.Output, sentinel) {
		t.Fatalf("rbatch %s result = %+v", mode, result)
	}
}

func completionRequest(cfg e2eConfig, sentinel string, stream bool) map[string]interface{} {
	body := map[string]interface{}{
		"model": cfg.provider + ":" + cfg.model, "stream": stream,
		"messages": []map[string]string{
			{"role": "system", "content": "Follow the user's output-format instruction exactly."},
			{"role": "user", "content": sentinelPrompt(sentinel)},
		},
	}
	if effort := providerEffort(cfg.provider); effort != "" {
		body["reasoning_effort"] = effort
	}
	return body
}

func sentinelPrompt(sentinel string) string {
	return "Reply with exactly " + sentinel + " and nothing else."
}

func providerEffort(provider string) string {
	if provider == "ollama" {
		return "max"
	}
	return ""
}

func postJSON(t *testing.T, url string, body interface{}) (*http.Response, []byte) {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		resp.Body.Close()
		t.Fatal(err)
	}
	resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(responseBody))
	return resp, responseBody
}

func replaceEnv(environ []string, key, value string) []string {
	prefix := key + "="
	result := make([]string, 0, len(environ)+1)
	for _, entry := range environ {
		if !strings.HasPrefix(entry, prefix) {
			result = append(result, entry)
		}
	}
	return append(result, prefix+value)
}
