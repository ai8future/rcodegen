// Package runner provides the stream output parser for Claude's stream-json format.
package runner

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// StreamEvent represents a parsed stream-json event from Claude
type StreamEvent struct {
	Type         string          `json:"type"`
	Subtype      string          `json:"subtype,omitempty"`
	Message      *AssistantMsg   `json:"message,omitempty"`
	Result       string          `json:"result,omitempty"`
	IsError      bool            `json:"is_error,omitempty"`
	Usage        *TokenUsage     `json:"usage,omitempty"`
	TotalCostUSD float64         `json:"total_cost_usd,omitempty"`
}

// TokenUsage represents token usage from a Claude run
type TokenUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
}

// AssistantMsg represents a message from the assistant
type AssistantMsg struct {
	Content []ContentBlock `json:"content,omitempty"`
}

// ContentBlock represents a content block in an assistant message
type ContentBlock struct {
	Type  string    `json:"type"`
	Text  string    `json:"text,omitempty"`
	Name  string    `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
}

// StreamParser processes stream-json output and formats it nicely
type StreamParser struct {
	writer       io.Writer
	lastType     string
	inToolUse    bool
	initialized  bool
	Usage        *TokenUsage // Captured from result event
	TotalCostUSD float64     // Captured from result event
}

// NewStreamParser creates a new stream parser
func NewStreamParser(w io.Writer) *StreamParser {
	return &StreamParser{
		writer: w,
	}
}

// ProcessLine processes a single JSON line from stream output
func (p *StreamParser) ProcessLine(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}

	var event StreamEvent
	if err := json.Unmarshal([]byte(line), &event); err != nil {
		// Not valid JSON, just print as-is
		fmt.Fprintln(p.writer, line)
		return
	}

	switch event.Type {
	case "system":
		p.handleSystem(event)
	case "assistant":
		p.handleAssistant(event)
	case "user":
		// Tool results - we can mostly skip these
		p.handleUser(event)
	case "result":
		p.handleResult(event)
	default:
		// Unknown type, skip
	}
}

// handleSystem handles system events (init, hooks, etc.)
func (p *StreamParser) handleSystem(event StreamEvent) {
	switch event.Subtype {
	case "init":
		// Show a brief initialization message
		if !p.initialized {
			fmt.Fprintf(p.writer, "%s%s⚡ Claude initialized%s\n", Dim, Cyan, Reset)
			p.initialized = true
		}
	case "hook_response":
		// Skip hook responses - they're verbose
	default:
		// Skip other system events
	}
}

// handleAssistant handles assistant messages
func (p *StreamParser) handleAssistant(event StreamEvent) {
	if event.Message == nil {
		return
	}

	for _, content := range event.Message.Content {
		switch content.Type {
		case "text":
			if content.Text != "" {
				// Add newline before text if we were in a tool use
				if p.inToolUse {
					fmt.Fprintln(p.writer)
					p.inToolUse = false
				}
				// Print assistant text with color
				fmt.Fprintf(p.writer, "%s%s%s\n", White, content.Text, Reset)
			}
		case "tool_use":
			p.handleToolUse(content)
		}
	}
	p.lastType = "assistant"
}

// handleToolUse formats a tool use nicely
func (p *StreamParser) handleToolUse(content ContentBlock) {
	toolName := content.Name

	// Map tool names to nice display names and icons
	icon := "🔧"
	displayName := toolName

	switch {
	case toolName == "Read":
		icon = "📖"
		displayName = "Reading file"
	case toolName == "Write":
		icon = "✏️"
		displayName = "Writing file"
	case toolName == "Edit":
		icon = "📝"
		displayName = "Editing file"
	case toolName == "Bash":
		icon = "💻"
		displayName = "Running command"
	case toolName == "Glob":
		icon = "🔍"
		displayName = "Finding files"
	case toolName == "Grep":
		icon = "🔎"
		displayName = "Searching"
	case toolName == "TodoWrite":
		icon = "📋"
		displayName = "Updating todos"
	case toolName == "Task":
		icon = "🚀"
		displayName = "Launching agent"
	case toolName == "WebFetch":
		icon = "🌐"
		displayName = "Fetching URL"
	case toolName == "WebSearch":
		icon = "🔍"
		displayName = "Web search"
	}

	// Try to extract useful info from input
	var inputInfo string
	if len(content.Input) > 0 {
		var inputMap map[string]interface{}
		if err := json.Unmarshal(content.Input, &inputMap); err == nil {
			inputInfo = extractToolInfo(toolName, inputMap)
		}
	}

	// Format: icon name: info
	if inputInfo != "" {
		fmt.Fprintf(p.writer, "%s%s %s:%s %s%s%s\n", Dim, icon, displayName, Reset, Yellow, inputInfo, Reset)
	} else {
		fmt.Fprintf(p.writer, "%s%s %s%s\n", Dim, icon, displayName, Reset)
	}
	p.inToolUse = true
}

// extractToolInfo extracts useful display info from tool input
func extractToolInfo(toolName string, input map[string]interface{}) string {
	switch toolName {
	case "Read":
		if path, ok := input["file_path"].(string); ok {
			return shortenPath(path)
		}
	case "Write":
		if path, ok := input["file_path"].(string); ok {
			return shortenPath(path)
		}
	case "Edit":
		if path, ok := input["file_path"].(string); ok {
			return shortenPath(path)
		}
	case "Bash":
		if cmd, ok := input["command"].(string); ok {
			// Truncate long commands
			if len(cmd) > 60 {
				cmd = cmd[:57] + "..."
			}
			return cmd
		}
		if desc, ok := input["description"].(string); ok {
			return desc
		}
	case "Glob":
		if pattern, ok := input["pattern"].(string); ok {
			return pattern
		}
	case "Grep":
		if pattern, ok := input["pattern"].(string); ok {
			if len(pattern) > 40 {
				pattern = pattern[:37] + "..."
			}
			return pattern
		}
	case "Task":
		if desc, ok := input["description"].(string); ok {
			return desc
		}
	case "TodoWrite":
		if todos, ok := input["todos"].([]interface{}); ok {
			return fmt.Sprintf("%d items", len(todos))
		}
	}
	return ""
}

// shortenPath shortens a file path for display
func shortenPath(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}

	// Handle exact match (just the home directory)
	if path == home {
		return "~"
	}

	// Remove home directory prefix
	if strings.HasPrefix(path, home+"/") {
		return "~" + path[len(home):]
	}

	return path
}

// handleUser handles user messages (typically tool results)
func (p *StreamParser) handleUser(event StreamEvent) {
	// Mostly skip tool results, they're verbose
	// Could add a flag to show them
}

// handleResult handles final result events
func (p *StreamParser) handleResult(event StreamEvent) {
	// Capture usage data from the result event
	if event.Usage != nil {
		p.Usage = event.Usage
	}
	if event.TotalCostUSD > 0 {
		p.TotalCostUSD = event.TotalCostUSD
	}

	// The result usually contains the final assistant output
	// which we've already shown incrementally
	if event.IsError {
		fmt.Fprintf(p.writer, "\n%s%s⚠️  Task failed%s\n", Bold, Red, Reset)
	}
}

// ProcessReader processes a stream of JSON lines from a reader
func (p *StreamParser) ProcessReader(r io.Reader) error {
	scanner := bufio.NewScanner(r)
	// Handle very long lines from stream output
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024) // 1MB max line size

	for scanner.Scan() {
		p.ProcessLine(scanner.Text())
	}

	return scanner.Err()
}
