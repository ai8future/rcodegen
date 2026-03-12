# OpenAI-Compatible HTTP Endpoint for rserve

**Date:** March 9, 2026
**Status:** Proposed

## Overview

Add an OpenAI-compatible REST/HTTP API to `rserve` so any app that speaks the OpenAI `/v1/chat/completions` format (Cursor, Continue, aider, custom apps) can point at rserve and use Claude/Codex/Gemini through rcodegen's runner pipeline.

## Architecture

`rserve` starts two listeners in the same process:
- **gRPC** on port 26147 (existing, unchanged)
- **HTTP** on port 26148 (new, OpenAI-compatible REST API)

Both share the same `RunRegistry` for concurrency control and respect the same `--max-concurrent` limit.

## Endpoints

| Endpoint | Method | Description |
|---|---|---|
| `/v1/chat/completions` | POST | Run a task via a tool, stream or return result |
| `/v1/models` | GET | List available tools (only those with CLIs detected) |
| `/health` | GET | Server status (mirrors gRPC `GetStatus`) |

## Model Naming: Prefixed Format

The `model` field in the OpenAI request maps to rcodegen's tool + model:

- `claude` / `claude:opus-4` / `claude:sonnet-4`
- `codex` / `codex:o3-pro`
- `gemini` / `gemini:2.5-pro`

Part before `:` selects the tool, part after overrides the model. No colon = tool's default model.

Examples:
```json
{"model": "claude"}           // → tool=claude, model=default
{"model": "claude:opus-4"}    // → tool=claude, model=opus-4
{"model": "gemini:2.5-flash"} // → tool=gemini, model=2.5-flash
```

## Message Handling

rcodegen tools take a single task prompt string, not a conversation history. The `messages` array is collapsed:

- `system` messages → concatenated as task preamble
- Last `user` message → the task prompt
- Earlier conversation turns → ignored

Final prompt sent to tool: `"{system_content}\n\n{last_user_message}"`

If no system message is present, just the last user message is used.

## Streaming (`stream: true`)

SSE format following the OpenAI spec:

```
data: {"id":"run-abc123","object":"chat.completion.chunk","created":1710000000,"model":"claude","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}

data: {"id":"run-abc123","object":"chat.completion.chunk","created":1710000000,"model":"claude","choices":[{"index":0,"delta":{"content":"Here is"},"finish_reason":null}]}

data: {"id":"run-abc123","object":"chat.completion.chunk","created":1710000000,"model":"claude","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: [DONE]
```

### Tool Use Visibility

- **Default**: text chunks only as `delta.content`. Tool use events are silently consumed.
- **With header `X-Show-Tool-Use: true`**: tool use events injected as text content like `[Reading file: src/main.go]\n` so the client can see what's happening.

## Non-Streaming (`stream: false` or omitted)

Returns complete response:

```json
{
  "id": "run-abc123",
  "object": "chat.completion",
  "created": 1710000000,
  "model": "claude",
  "choices": [{
    "index": 0,
    "message": {
      "role": "assistant",
      "content": "The full response text..."
    },
    "finish_reason": "stop"
  }],
  "usage": {
    "prompt_tokens": 150,
    "completion_tokens": 500,
    "total_tokens": 650
  }
}
```

## `/v1/models` Response

Only lists tools whose CLIs are detected on `$PATH` at startup:

```json
{
  "object": "list",
  "data": [
    {"id": "claude", "object": "model", "created": 1710000000, "owned_by": "rcodegen"},
    {"id": "codex", "object": "model", "created": 1710000000, "owned_by": "rcodegen"},
    {"id": "gemini", "object": "model", "created": 1710000000, "owned_by": "rcodegen"}
  ]
}
```

## `/health` Response

```json
{
  "status": "ok",
  "version": "2.3.0",
  "active_runs": 1,
  "max_concurrent": 3
}
```

## Implementation Scope

### New files
- `pkg/server/openai/handler.go` — HTTP handlers for all endpoints
- `pkg/server/openai/types.go` — OpenAI request/response structs
- `pkg/server/openai/sse.go` — SSE streaming writer
- `pkg/server/openai/models.go` — CLI detection and model listing

### Modified files
- `cmd/rserve/main.go` — start HTTP listener alongside gRPC on port+1

### Unchanged
- All existing gRPC, runner, and tool packages remain untouched
- Uses stdlib `net/http` (no framework dependency)

## Design Decisions

1. **Separate port, not multiplexed** — Keeps gRPC and HTTP cleanly isolated. Port+1 convention is simple and predictable.
2. **Prefixed model format** — Explicit tool routing without needing a config file for aliases. Natural to read.
3. **System + last user message** — Clean mapping to rcodegen's single-prompt model. System message gives preamble context without trying to fake multi-turn conversation.
4. **CLI detection at startup** — Avoids advertising tools that will fail. Fail-fast over fail-at-request.
5. **Tool use opt-in via header** — Most OpenAI clients don't expect tool annotations in content. Header keeps default behavior clean while allowing power users to see what's happening.
