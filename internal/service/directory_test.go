package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jenish0908/assetflow-be/internal/auth"
)

func TestLikePattern(t *testing.T) {
	tests := []struct {
		in, want string
		wantErr  bool
	}{
		{in: "", want: ""},
		{in: "   ", want: ""},
		{in: "mac", want: "%mac%"},
		{in: " L-102 ", want: "%L-102%"},
		{in: "50%", want: `%50\%%`},
		{in: "a_b", want: `%a\_b%`},
		{in: `c:\x`, want: `%c:\\x%`}, // the escape character itself is escaped
		{in: string(make([]rune, 101)), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := likePattern(tt.in)
			if tt.wantErr {
				require.ErrorIs(t, err, ErrInvalid)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestAssetListFilter(t *testing.T) {
	tests := []struct {
		name     string
		filter   AssetFilter
		want     AssetFilter
		wantKind error
	}{
		{name: "no filter", filter: AssetFilter{}, want: AssetFilter{}},
		{name: "status and search", filter: AssetFilter{Status: StatusMaintenance, Query: "dell"}, want: AssetFilter{Status: StatusMaintenance, Query: "%dell%"}},
		{name: "unknown status", filter: AssetFilter{Status: "lost"}, wantKind: ErrInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got AssetFilter
			store := &mockStore{
				listAssets: func(_ context.Context, f AssetFilter, _, _ int) ([]Asset, error) {
					got = f
					return nil, nil
				},
				countAssets: func(_ context.Context, f AssetFilter) (int64, error) {
					assert.Equal(t, got, f, "count uses the same filter as the page")
					return 0, nil
				},
			}
			_, err := NewAssetService(store, ListBatched).List(context.Background(), tt.filter, 0, 0)
			if tt.wantKind != nil {
				require.ErrorIs(t, err, tt.wantKind)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestUserDirectory(t *testing.T) {
	store := &mockStore{
		listUsers: func(_ context.Context, pattern string, limit, offset int) ([]User, error) {
			assert.Equal(t, "%pri%", pattern)
			assert.Equal(t, defaultPageSize, limit)
			return []User{{ID: 2, Name: "Priya"}}, nil
		},
		countUsers: func(context.Context, string) (int64, error) { return 1, nil },
	}
	page, err := NewUserService(store, nil).List(context.Background(), " pri ", 0, 0)
	require.NoError(t, err)
	assert.Equal(t, int64(1), page.Total)
	assert.Len(t, page.Items, 1)

	_, err = NewUserService(store, nil).GetMany(context.Background(), nil)
	require.ErrorIs(t, err, ErrInvalid, "at least one ID")
	_, err = NewUserService(store, nil).GetMany(context.Background(), make([]int64, maxPageSize+1))
	require.ErrorIs(t, err, ErrInvalid, "bounded like a page")
}

func TestNotificationsAreScopedToTheCaller(t *testing.T) {
	tests := []struct {
		name   string
		actor  auth.Claims
		wantID *int64
	}{
		{"viewer sees own", auth.Claims{UserID: 7, Role: auth.RoleViewer}, ptr(int64(7))},
		{"manager sees own", auth.Claims{UserID: 8, Role: auth.RoleManager}, ptr(int64(8))},
		{"admin sees everyone's", auth.Claims{UserID: 1, Role: auth.RoleAdmin}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &mockStore{listNotifications: func(_ context.Context, userID *int64, _ int) ([]Notification, error) {
				assert.Equal(t, tt.wantID, userID)
				return nil, nil
			}}
			_, err := NewDashboardService(store).Notifications(context.Background(), tt.actor, 0)
			require.NoError(t, err)
		})
	}
}

func ptr[T any](v T) *T { return &v }
