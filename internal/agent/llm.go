// Package agent turns natural-language requests into asset operations. A
// chat model picks tools to call; this package validates every call, checks
// the caller's permission in Go, runs it through the normal services and
// feeds the results back until the model answers in plain text.
package agent

import (
	"context"
	"encoding/json"
	"errors"
)

// Chat roles.
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"
)

// Message is one chat message. Assistant messages may carry ToolCalls; tool
// messages answer one call, identified by ToolCallID.
type Message struct {
	Role       string
	Content    string
	ToolCalls  []ToolCall
	ToolCallID string
}

// ToolCall is a model's request to run a tool. Arguments is the raw JSON text
// the model produced: untrusted, and possibly not even valid JSON.
type ToolCall struct {
	ID        string
	Name      string
	Arguments string
}

// ToolDef describes a tool to the model; Parameters is a JSON Schema.
type ToolDef struct {
	Name        string
	Description string
	Parameters  json.RawMessage
}

type ChatRequest struct {
	Messages []Message
	Tools    []ToolDef
}

// LLM is a chat model with tool calling. OpenAI talks to the real API; Fake
// is a deterministic stand-in for tests and running without an API key.
type LLM interface {
	// Chat returns the next assistant message.
	Chat(ctx context.Context, req ChatRequest) (Message, error)
}

// ErrLLMUnavailable wraps failures to reach the model.
var ErrLLMUnavailable = errors.New("language model unavailable")
