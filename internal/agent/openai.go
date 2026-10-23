package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// OpenAI calls the Chat Completions API with tools, using plain net/http.
type OpenAI struct {
	apiKey  string
	model   string
	baseURL string
	client  *http.Client
}

func NewOpenAI(apiKey, model string) *OpenAI {
	return &OpenAI{
		apiKey:  apiKey,
		model:   model,
		baseURL: "https://api.openai.com/v1",
		client:  &http.Client{Timeout: 60 * time.Second},
	}
}

// Wire format of https://platform.openai.com/docs/api-reference/chat.
type (
	oaRequest struct {
		Model             string      `json:"model"`
		Messages          []oaMessage `json:"messages"`
		Tools             []oaTool    `json:"tools,omitempty"`
		ParallelToolCalls *bool       `json:"parallel_tool_calls,omitempty"`
	}
	oaMessage struct {
		Role       string       `json:"role"`
		Content    *string      `json:"content"` // null for assistant messages that only call tools
		ToolCalls  []oaToolCall `json:"tool_calls,omitempty"`
		ToolCallID string       `json:"tool_call_id,omitempty"`
	}
	oaToolCall struct {
		ID       string `json:"id"`
		Type     string `json:"type"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	}
	oaTool struct {
		Type     string     `json:"type"`
		Function oaFunction `json:"function"`
	}
	oaFunction struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	}
	oaResponse struct {
		Choices []struct {
			Message oaMessage `json:"message"`
		} `json:"choices"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
)

func (c *OpenAI) Chat(ctx context.Context, req ChatRequest) (Message, error) {
	body := oaRequest{Model: c.model, Messages: make([]oaMessage, len(req.Messages))}
	for i, m := range req.Messages {
		body.Messages[i] = toOpenAI(m)
	}
	for _, t := range req.Tools {
		body.Tools = append(body.Tools, oaTool{Type: "function", Function: oaFunction(t)})
	}
	if len(body.Tools) > 0 {
		parallel := true
		body.ParallelToolCalls = &parallel
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return Message{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return Message{}, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return Message{}, fmt.Errorf("openai: %w", err)
	}
	defer resp.Body.Close()

	var out oaResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&out); err != nil {
		return Message{}, fmt.Errorf("openai: status %d: decode response: %w", resp.StatusCode, err)
	}
	if resp.StatusCode != http.StatusOK {
		msg := http.StatusText(resp.StatusCode)
		if out.Error != nil {
			msg = out.Error.Message
		}
		return Message{}, fmt.Errorf("openai: status %d: %s", resp.StatusCode, msg)
	}
	if len(out.Choices) == 0 {
		return Message{}, fmt.Errorf("openai: response has no choices")
	}
	return fromOpenAI(out.Choices[0].Message), nil
}

func toOpenAI(m Message) oaMessage {
	om := oaMessage{Role: m.Role, ToolCallID: m.ToolCallID}
	if m.Content != "" || len(m.ToolCalls) == 0 {
		content := m.Content
		om.Content = &content
	}
	for _, tc := range m.ToolCalls {
		var call oaToolCall
		call.ID, call.Type = tc.ID, "function"
		call.Function.Name, call.Function.Arguments = tc.Name, tc.Arguments
		om.ToolCalls = append(om.ToolCalls, call)
	}
	return om
}

func fromOpenAI(om oaMessage) Message {
	m := Message{Role: om.Role}
	if om.Content != nil {
		m.Content = *om.Content
	}
	for _, tc := range om.ToolCalls {
		m.ToolCalls = append(m.ToolCalls, ToolCall{ID: tc.ID, Name: tc.Function.Name, Arguments: tc.Function.Arguments})
	}
	return m
}
