package localai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"rcodegen/pkg/runner"
)

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model           string        `json:"model"`
	Messages        []chatMessage `json:"messages"`
	Stream          bool          `json:"stream"`
	ReasoningEffort string        `json:"reasoning_effort,omitempty"`
}

type chatResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage,omitempty"`
}

func (t *Tool) RunDirectAPI(ctx context.Context, cfg *runner.Config, _ string, task string) int {
	if err := t.ValidateConfig(cfg); err != nil {
		t.diagnostic(cfg, err.Error())
		return 1
	}

	messages := make([]chatMessage, 0, len(cfg.Messages))
	if len(cfg.Messages) == 0 {
		if strings.TrimSpace(task) == "" {
			t.diagnostic(cfg, "task must not be empty")
			return 1
		}
		messages = append(messages, chatMessage{Role: "user", Content: task})
	} else {
		for _, message := range cfg.Messages {
			messages = append(messages, chatMessage{Role: message.Role, Content: message.Content})
		}
	}

	payload := chatRequest{Model: cfg.Model, Messages: messages, Stream: false}
	if t.flavor == FlavorOllama {
		payload.ReasoningEffort = cfg.Effort
	}
	req, cancel, err := t.newRequest(ctx, http.MethodPost, "/v1/chat/completions", payload)
	if err != nil {
		t.diagnostic(cfg, err.Error())
		return 1
	}
	defer cancel()

	resp, err := directHTTPClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			t.diagnostic(cfg, "local runtime request cancelled")
		} else if errors.Is(req.Context().Err(), context.DeadlineExceeded) {
			t.diagnostic(cfg, "local runtime request timed out")
		} else {
			t.diagnostic(cfg, "local runtime request failed: "+err.Error())
		}
		return 1
	}
	defer resp.Body.Close()

	body, err := readBoundedResponse(resp.Body)
	if err != nil {
		t.diagnostic(cfg, err.Error())
		return 1
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		detail := upstreamErrorMessage(body)
		if detail == "" {
			detail = http.StatusText(resp.StatusCode)
		}
		t.diagnostic(cfg, fmt.Sprintf("local runtime returned HTTP %d: %s", resp.StatusCode, detail))
		return 1
	}

	var result chatResponse
	if err := json.Unmarshal(body, &result); err != nil {
		t.diagnostic(cfg, "local runtime returned malformed JSON")
		return 1
	}
	if len(result.Choices) == 0 {
		t.diagnostic(cfg, "local runtime returned no choices")
		return 1
	}
	output := cfg.Output
	if output == nil {
		output = io.Discard
	}
	if _, err := io.WriteString(output, result.Choices[0].Message.Content); err != nil {
		t.diagnostic(cfg, "write local runtime output: "+err.Error())
		return 1
	}
	if result.Usage != nil {
		cfg.TokenUsage = &runner.TokenUsage{
			InputTokens: result.Usage.PromptTokens, OutputTokens: result.Usage.CompletionTokens,
		}
	}
	return 0
}
