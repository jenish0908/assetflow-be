package agent

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	"github.com/jenish0908/assetflow-be/internal/auth"
	"github.com/jenish0908/assetflow-be/internal/service"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

var (
	viewer  = auth.Claims{UserID: 1, Role: auth.RoleViewer}
	manager = auth.Claims{UserID: 2, Role: auth.RoleManager}
	admin   = auth.Claims{UserID: 3, Role: auth.RoleAdmin}
)

func priya() *fakeUsers {
	return &fakeUsers{users: []service.User{{ID: 7, Name: "Priya Shah", Email: "priya@example.com"}}}
}

func TestPermissionIsCheckedBeforeExecution(t *testing.T) {
	allocate := call("allocateAsset", map[string]string{"asset_tag": "L-102", "user": "priya@example.com"})
	addUser := call("addUser", map[string]string{"email": "new@example.com", "name": "New", "role": "viewer"})
	lookup := call("getAssetInfo", map[string]string{"asset_tag": "L-102"})

	tests := []struct {
		name    string
		actor   auth.Claims
		call    ToolCall
		allowed bool
	}{
		{"viewer can look up", viewer, lookup, true},
		{"viewer cannot allocate", viewer, allocate, false},
		{"manager can allocate", manager, allocate, true},
		{"manager cannot add users", manager, addUser, false},
		{"admin can add users", admin, addUser, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assets, users := newFakeAssets("L-102"), priya()
			a := New(callsThenDone(tt.call), assets, users, DefaultOptions(), discardLogger())

			reply, err := a.Chat(context.Background(), tt.actor, "do it")
			require.NoError(t, err)
			require.Len(t, reply.ToolCalls, 1)
			trace := reply.ToolCalls[0]

			assert.Equal(t, tt.allowed, trace.OK, trace.Error)
			if !tt.allowed {
				assert.Contains(t, trace.Error, "permission denied")
				assert.Empty(t, assets.Events(), "denied call must not touch the asset service")
				assert.Empty(t, users.created, "denied call must not touch the user service")
			}
		})
	}
}

func TestOfferedToolsMatchRole(t *testing.T) {
	a := New(&scriptedLLM{}, newFakeAssets(), priya(), DefaultOptions(), discardLogger())
	names := func(role auth.Role) []string {
		var out []string
		for _, d := range a.toolDefsFor(role) {
			out = append(out, d.Name)
		}
		return out
	}
	assert.Equal(t, []string{"getAssetInfo"}, names(auth.RoleViewer))
	assert.Len(t, names(auth.RoleManager), 5)
	assert.Len(t, names(auth.RoleAdmin), 6)
	assert.NotContains(t, names(auth.RoleManager), "addUser")
}

func TestInvalidToolCallsAreReportedToTheModel(t *testing.T) {
	tests := []struct {
		name    string
		call    ToolCall
		wantErr string
	}{
		{"malformed JSON", ToolCall{Name: "allocateAsset", Arguments: `{"asset_tag": "L-102",`}, "invalid arguments"},
		{"unknown field", ToolCall{Name: "allocateAsset", Arguments: `{"asset_tag":"L-102","user":"priya@example.com","force":true}`}, `unknown field "force"`},
		{"wrong type", ToolCall{Name: "getAssetInfo", Arguments: `{"asset_tag": 102}`}, "invalid arguments"},
		{"missing field", ToolCall{Name: "allocateAsset", Arguments: `{"user":"priya@example.com"}`}, "asset_tag is required"},
		{"unknown asset", call("allocateAsset", map[string]string{"asset_tag": "L-999", "user": "priya@example.com"}), `asset with tag "L-999" not found`},
		{"unknown user", call("allocateAsset", map[string]string{"asset_tag": "L-102", "user": "Ghost"}), `no user matches "Ghost"`},
		{"ambiguous user", call("allocateAsset", map[string]string{"asset_tag": "L-102", "user": "Sam"}), `"Sam" matches 2 users`},
		{"unknown tool", ToolCall{Name: "dropDatabase", Arguments: `{}`}, `unknown tool "dropDatabase"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assets := newFakeAssets("L-102")
			users := &fakeUsers{users: []service.User{
				{ID: 7, Name: "Priya Shah", Email: "priya@example.com"},
				{ID: 8, Name: "Sam", Email: "sam.a@example.com"},
				{ID: 9, Name: "Sam", Email: "sam.b@example.com"},
			}}
			llm := callsThenDone(tt.call)
			a := New(llm, assets, users, DefaultOptions(), discardLogger())

			reply, err := a.Chat(context.Background(), admin, "do it")
			require.NoError(t, err)
			require.Len(t, reply.ToolCalls, 1)
			assert.False(t, reply.ToolCalls[0].OK)
			assert.Contains(t, reply.ToolCalls[0].Error, tt.wantErr)
			assert.NotContains(t, assets.Events(), "allocate:start", "nothing is written")

			// The error went back to the model as a tool message.
			second := llm.requests[1].Messages
			last := second[len(second)-1]
			assert.Equal(t, RoleTool, last.Role)
			assert.Contains(t, last.Content, `"ok":false`)
		})
	}
}

// TestReadsRunConcurrentlyWritesInOrder sends one turn with
// [read R1, read R2, read R3, write (allocate W1), read R4]. R1-R3 must run
// at the same time; the write must start after they finish; R4 must start
// after the write finishes.
func TestReadsRunConcurrentlyWritesInOrder(t *testing.T) {
	assets := newFakeAssets("R1", "R2", "R3", "R4", "W1")

	// R1-R3 each wait until all three have started. Run sequentially, the
	// first would wait forever, so the timeout turns that into a failure.
	var arrived sync.WaitGroup
	arrived.Add(3)
	allStarted := make(chan struct{})
	go func() { arrived.Wait(); close(allStarted) }()
	assets.onGet = func(_ context.Context, tag string) error {
		if tag != "R1" && tag != "R2" && tag != "R3" {
			return nil
		}
		arrived.Done()
		select {
		case <-allStarted:
			return nil
		case <-time.After(2 * time.Second):
			return errors.New("reads did not run concurrently")
		}
	}

	get := func(tag string) ToolCall { return call("getAssetInfo", map[string]string{"asset_tag": tag}) }
	llm := callsThenDone(get("R1"), get("R2"), get("R3"),
		call("allocateAsset", map[string]string{"asset_tag": "W1", "user": "priya@example.com"}),
		get("R4"))
	a := New(llm, assets, priya(), DefaultOptions(), discardLogger())

	reply, err := a.Chat(context.Background(), manager, "go")
	require.NoError(t, err)
	for _, tr := range reply.ToolCalls {
		require.True(t, tr.OK, "%s: %s", tr.Name, tr.Error)
	}

	ev := assets.Events()
	at := func(e string) int {
		i := slices.Index(ev, e)
		require.NotEqual(t, -1, i, "missing event %s in %v", e, ev)
		return i
	}
	writeStart := at("get:W1:start") // the write begins by resolving its tag
	for _, r := range []string{"R1", "R2", "R3"} {
		assert.Less(t, at("get:"+r+":end"), writeStart, "%s finished before the write began", r)
	}
	assert.Greater(t, at("get:R4:start"), at("allocate:end"), "read after the write waits for it")

	// Results are reported in the order the model asked for them.
	var order []string
	for _, tr := range reply.ToolCalls {
		var args map[string]string
		require.NoError(t, json.Unmarshal([]byte(tr.Arguments), &args))
		order = append(order, args["asset_tag"])
	}
	assert.Equal(t, []string{"R1", "R2", "R3", "W1", "R4"}, order)
}

func TestToolTimeout(t *testing.T) {
	assets := newFakeAssets("L-1")
	assets.onGet = func(ctx context.Context, _ string) error {
		<-ctx.Done() // a dependency that hangs until cancelled
		return ctx.Err()
	}
	opts := DefaultOptions()
	opts.ToolTimeout = 50 * time.Millisecond
	a := New(callsThenDone(call("getAssetInfo", map[string]string{"asset_tag": "L-1"})), assets, priya(), opts, discardLogger())

	start := time.Now()
	reply, err := a.Chat(context.Background(), viewer, "show L-1")
	require.NoError(t, err)

	assert.Less(t, time.Since(start), time.Second)
	assert.Contains(t, reply.ToolCalls[0].Error, "timed out after 50ms")
}

func TestStopsAfterMaxSteps(t *testing.T) {
	loop := Message{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c", Name: "getAssetInfo", Arguments: `{"asset_tag":"L-1"}`}}}
	llm := &scriptedLLM{}
	for range 10 {
		llm.replies = append(llm.replies, loop) // the model never stops calling tools
	}
	opts := DefaultOptions()
	opts.MaxSteps = 3

	reply, err := New(llm, newFakeAssets("L-1"), priya(), opts, discardLogger()).Chat(context.Background(), viewer, "loop")
	require.NoError(t, err)
	assert.Len(t, reply.ToolCalls, 3)
	assert.Contains(t, reply.Text, "stopped after 3 rounds")
}

type failingLLM struct{}

func (failingLLM) Chat(context.Context, ChatRequest) (Message, error) {
	return Message{}, errors.New("connection reset")
}

func TestLLMFailure(t *testing.T) {
	_, err := New(failingLLM{}, newFakeAssets(), priya(), DefaultOptions(), discardLogger()).Chat(context.Background(), viewer, "hi")
	require.ErrorIs(t, err, ErrLLMUnavailable)
}

func TestMessageValidation(t *testing.T) {
	a := New(&scriptedLLM{}, newFakeAssets(), priya(), DefaultOptions(), discardLogger())
	for _, msg := range []string{"", "   ", string(make([]byte, maxMessageLen+1))} {
		_, err := a.Chat(context.Background(), viewer, msg)
		require.ErrorIs(t, err, service.ErrInvalid)
	}
}

func TestAddUserNeverShowsPasswordToModel(t *testing.T) {
	users := priya()
	llm := callsThenDone(call("addUser", map[string]string{"email": "n@example.com", "name": "N", "role": "viewer"}))
	_, err := New(llm, newFakeAssets(), users, DefaultOptions(), discardLogger()).Chat(context.Background(), admin, "add")
	require.NoError(t, err)

	require.Len(t, users.created, 1)
	password := users.created[0].Password
	assert.Len(t, password, 36, "a strong random password was generated")
	for _, req := range llm.requests {
		for _, m := range req.Messages {
			assert.NotContains(t, m.Content, password)
		}
	}
}
