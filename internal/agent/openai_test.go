package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOpenAIWireFormat checks the request we send and the response we parse
// against a local server, so no API key is needed.
func TestOpenAIWireFormat(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/chat/completions", r.URL.Path)
		assert.Equal(t, "Bearer sk-test", r.Header.Get("Authorization"))
		body, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(body, &got))

		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":null,
			"tool_calls":[{"id":"call_1","type":"function","function":{"name":"getAssetInfo","arguments":"{\"asset_tag\":\"L-1\"}"}}]}}]}`)
	}))
	defer srv.Close()

	c := NewOpenAI("sk-test", "gpt-test")
	c.baseURL = srv.URL
	msg, err := c.Chat(context.Background(), ChatRequest{
		Messages: []Message{
			{Role: RoleSystem, Content: "sys"},
			{Role: RoleUser, Content: "show L-1"},
			{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "call_0", Name: "getAssetInfo", Arguments: `{"asset_tag":"L-0"}`}}},
			{Role: RoleTool, ToolCallID: "call_0", Content: `{"ok":true}`},
		},
		Tools: []ToolDef{{Name: "getAssetInfo", Description: "d", Parameters: schema(`{"asset_tag":{"type":"string"}}`, "asset_tag")}},
	})
	require.NoError(t, err)

	// Response decoded into our types.
	require.Len(t, msg.ToolCalls, 1)
	assert.Equal(t, ToolCall{ID: "call_1", Name: "getAssetInfo", Arguments: `{"asset_tag":"L-1"}`}, msg.ToolCalls[0])

	// Request encoded the way the API expects.
	assert.Equal(t, "gpt-test", got["model"])
	assert.Equal(t, true, got["parallel_tool_calls"])
	msgs := got["messages"].([]any)
	assistant := msgs[2].(map[string]any)
	assert.Nil(t, assistant["content"], "tool-call-only assistant message has null content")
	assert.Equal(t, "getAssetInfo", assistant["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)["name"])
	assert.Equal(t, "call_0", msgs[3].(map[string]any)["tool_call_id"])
	tool := got["tools"].([]any)[0].(map[string]any)
	assert.Equal(t, "function", tool["type"])
	assert.Equal(t, "object", tool["function"].(map[string]any)["parameters"].(map[string]any)["type"])
}

func TestOpenAIErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"Incorrect API key provided"}}`)
	}))
	defer srv.Close()

	c := NewOpenAI("bad", "gpt-test")
	c.baseURL = srv.URL
	_, err := c.Chat(context.Background(), ChatRequest{Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	require.ErrorContains(t, err, "status 401: Incorrect API key provided")
}
