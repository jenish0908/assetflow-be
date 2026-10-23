package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jenish0908/assetflow-be/internal/service"
)

func TestFakeParses(t *testing.T) {
	tests := []struct {
		text string
		want []string // "name args-json"
	}{
		{"allocate laptop L-102 to Priya", []string{`allocateAsset {"asset_tag":"L-102","user":"Priya"}`}},
		{"Please allocate the L-7 to priya@example.com.", []string{`allocateAsset {"asset_tag":"L-7","user":"priya@example.com"}`}},
		{"create laptop L-200 named ThinkPad X1", []string{`createAsset {"category":"laptop","name":"ThinkPad X1","tag":"L-200"}`}},
		{"add Priya Shah priya@example.com as manager", []string{`addUser {"email":"priya@example.com","name":"Priya Shah","role":"manager"}`}},
		{"mark M-7 as maintenance", []string{`updateAssetStatus {"asset_tag":"M-7","status":"maintenance"}`}},
		{"retire L-1", []string{`updateAssetStatus {"asset_tag":"L-1","status":"retired"}`}},
		{"rename L-1 to Old MacBook", []string{`updateAsset {"asset_tag":"L-1","name":"Old MacBook"}`}},
		{"show L-1, L-2 and M-3", []string{
			`getAssetInfo {"asset_tag":"L-1"}`, `getAssetInfo {"asset_tag":"L-2"}`, `getAssetInfo {"asset_tag":"M-3"}`,
		}},
		{"create laptop L-9 named Spare; allocate L-9 to Priya then show L-9", []string{
			`createAsset {"category":"laptop","name":"Spare","tag":"L-9"}`,
			`allocateAsset {"asset_tag":"L-9","user":"Priya"}`,
			`getAssetInfo {"asset_tag":"L-9"}`,
		}},
		{"what's the weather?", nil},
	}

	for _, tt := range tests {
		t.Run(tt.text, func(t *testing.T) {
			msg, err := Fake{}.Chat(context.Background(), ChatRequest{Messages: []Message{{Role: RoleUser, Content: tt.text}}})
			require.NoError(t, err)

			var got []string
			for _, c := range msg.ToolCalls {
				var args map[string]string
				require.NoError(t, json.Unmarshal([]byte(c.Arguments), &args))
				canonical, _ := json.Marshal(args) // sorted keys
				got = append(got, c.Name+" "+string(canonical))
			}
			assert.Equal(t, tt.want, got)
			if tt.want == nil {
				assert.NotEmpty(t, msg.Content, "falls back to a help message")
			}
		})
	}
}

// TestFakeEndToEnd runs the agent with the fake model, as the API does when
// OPENAI_API_KEY is unset.
func TestFakeEndToEnd(t *testing.T) {
	tests := []struct {
		name  string
		text  string
		want  string
		calls int
	}{
		{
			name:  "allocate by name",
			text:  "allocate laptop L-102 to Priya Shah",
			want:  "Allocated L-102 to Priya Shah (priya@example.com).",
			calls: 1,
		},
		{
			name:  "write then read sees the write",
			text:  "create laptop L-200 named ThinkPad X1; show L-200",
			want:  "Created laptop L-200. L-200 (ThinkPad X1) is available.",
			calls: 2,
		},
		{
			name:  "errors are explained",
			text:  "allocate L-404 to Priya Shah",
			want:  `Could not allocate L-404 to Priya Shah: asset with tag "L-404" not found.`,
			calls: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := New(Fake{}, newFakeAssets("L-102"), priya(), DefaultOptions(), discardLogger())
			reply, err := a.Chat(context.Background(), manager, tt.text)
			require.NoError(t, err)
			assert.Equal(t, tt.want, reply.Text)
			assert.Len(t, reply.ToolCalls, tt.calls)
		})
	}
}

func TestFakeCannotBypassPermissions(t *testing.T) {
	// The fake model calls allocateAsset even though a viewer is not offered it.
	assets := newFakeAssets("L-102")
	a := New(Fake{}, assets, priya(), DefaultOptions(), discardLogger())

	reply, err := a.Chat(context.Background(), viewer, "allocate laptop L-102 to Priya Shah")
	require.NoError(t, err)
	assert.Contains(t, reply.Text, "permission denied")
	assert.Empty(t, assets.Events())
}

func TestGetAssetInfoNamesTheHolder(t *testing.T) {
	assets := newFakeAssets("L-102")
	a := assets.byTag["L-102"]
	a.Status = service.StatusAllocated
	a.Allocations = []service.Allocation{{ID: 1, AssetID: a.ID, UserID: 7}}
	assets.byTag["L-102"] = a

	reply, err := New(Fake{}, assets, priya(), DefaultOptions(), discardLogger()).Chat(context.Background(), viewer, "show L-102")
	require.NoError(t, err)
	assert.Equal(t, "L-102 (Asset L-102) is allocated, with Priya Shah.", reply.Text)
}
