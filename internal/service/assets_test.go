package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAssetCreate(t *testing.T) {
	tests := []struct {
		name     string
		in       NewAsset
		storeErr error
		wantKind error
	}{
		{name: "ok", in: NewAsset{Tag: " L-102 ", Name: "MacBook", Category: "laptop"}},
		{name: "missing tag", in: NewAsset{Name: "MacBook", Category: "laptop"}, wantKind: ErrInvalid},
		{name: "blank name", in: NewAsset{Tag: "L-1", Name: "   ", Category: "laptop"}, wantKind: ErrInvalid},
		{name: "tag too long", in: NewAsset{Tag: strings.Repeat("x", 65), Name: "n", Category: "c"}, wantKind: ErrInvalid},
		{
			name:     "duplicate tag",
			in:       NewAsset{Tag: "L-1", Name: "n", Category: "c"},
			storeErr: fmt.Errorf("unique violation: %w", ErrConflict),
			wantKind: ErrConflict,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &mockStore{
				createAsset: func(_ context.Context, a NewAsset) (Asset, error) {
					assert.Equal(t, strings.TrimSpace(a.Tag), a.Tag, "tag should be trimmed")
					return Asset{ID: 1, Tag: a.Tag}, tt.storeErr
				},
			}

			got, err := NewAssetService(store, ListBatched).Create(context.Background(), tt.in)
			if tt.wantKind != nil {
				require.ErrorIs(t, err, tt.wantKind)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, int64(1), got.ID)
		})
	}
}

func TestAssetAllocate(t *testing.T) {
	available := Asset{ID: 7, Tag: "L-102", Status: StatusAvailable}

	tests := []struct {
		name       string
		asset      Asset
		assetErr   error
		userErr    error
		allocErr   error
		wantKind   error
		wantStatus bool // whether the status is set and the notification enqueued
	}{
		{name: "ok", asset: available, wantStatus: true},
		{name: "asset not found", assetErr: ErrNotFound, wantKind: ErrNotFound},
		{name: "asset already allocated", asset: Asset{ID: 7, Status: StatusAllocated}, wantKind: ErrConflict},
		{name: "asset in maintenance", asset: Asset{ID: 7, Status: StatusMaintenance}, wantKind: ErrConflict},
		{name: "user not found", asset: available, userErr: ErrNotFound, wantKind: ErrNotFound},
		{name: "lost race on unique index", asset: available, allocErr: ErrConflict, wantKind: ErrConflict},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			statusSet := false
			var enqueued []string
			store := &mockStore{
				enqueueNotification: func(_ context.Context, kind string, payload []byte) error {
					enqueued = append(enqueued, kind)
					assert.JSONEq(t, `{"asset_id":7,"asset_tag":"L-102","user_id":3}`, string(payload))
					return nil
				},
				getAssetForUpdate: func(context.Context, int64) (Asset, error) { return tt.asset, tt.assetErr },
				getUser:           func(context.Context, int64) (User, error) { return User{ID: 3}, tt.userErr },
				createAllocation: func(_ context.Context, assetID, userID int64) (Allocation, error) {
					return Allocation{ID: 1, AssetID: assetID, UserID: userID}, tt.allocErr
				},
				setAssetStatus: func(_ context.Context, _ int64, s AssetStatus) (Asset, error) {
					assert.Equal(t, StatusAllocated, s)
					statusSet = true
					return Asset{}, nil
				},
			}

			got, err := NewAssetService(store, ListBatched).Allocate(context.Background(), 7, 3)

			assert.Equal(t, tt.wantStatus, statusSet)
			if tt.wantStatus {
				assert.Equal(t, []string{"asset_allocated"}, enqueued, "outbox job in the same transaction")
			} else {
				assert.Empty(t, enqueued, "no notification for a failed allocation")
			}
			if tt.wantKind != nil {
				require.ErrorIs(t, err, tt.wantKind)
				var svcErr *Error
				require.ErrorAs(t, err, &svcErr, "client should get a readable message")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, Allocation{ID: 1, AssetID: 7, UserID: 3}, got)
		})
	}
}

func TestAssetUpdateStatus(t *testing.T) {
	tests := []struct {
		name     string
		current  AssetStatus
		next     AssetStatus
		wantKind error
	}{
		{name: "available to maintenance", current: StatusAvailable, next: StatusMaintenance},
		{name: "maintenance to available", current: StatusMaintenance, next: StatusAvailable},
		{name: "available to retired", current: StatusAvailable, next: StatusRetired},
		{name: "cannot set allocated directly", current: StatusAvailable, next: StatusAllocated, wantKind: ErrInvalid},
		{name: "unknown status", current: StatusAvailable, next: "lost", wantKind: ErrInvalid},
		{name: "allocated must be returned first", current: StatusAllocated, next: StatusMaintenance, wantKind: ErrConflict},
		{name: "retired is terminal", current: StatusRetired, next: StatusAvailable, wantKind: ErrConflict},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &mockStore{
				getAssetForUpdate: func(context.Context, int64) (Asset, error) {
					return Asset{ID: 1, Status: tt.current}, nil
				},
				setAssetStatus: func(_ context.Context, id int64, s AssetStatus) (Asset, error) {
					return Asset{ID: id, Status: s}, nil
				},
			}

			got, err := NewAssetService(store, ListBatched).UpdateStatus(context.Background(), 1, tt.next)
			if tt.wantKind != nil {
				require.ErrorIs(t, err, tt.wantKind)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.next, got.Status)
		})
	}
}

func TestAssetListPagination(t *testing.T) {
	tests := []struct {
		name      string
		limit     int
		offset    int
		wantLimit int
		wantKind  error
	}{
		{name: "default limit", limit: 0, wantLimit: defaultPageSize},
		{name: "explicit limit", limit: 10, offset: 20, wantLimit: 10},
		{name: "limit too large", limit: maxPageSize + 1, wantKind: ErrInvalid},
		{name: "negative limit", limit: -1, wantKind: ErrInvalid},
		{name: "negative offset", limit: 10, offset: -1, wantKind: ErrInvalid},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &mockStore{
				listAssets: func(_ context.Context, limit, offset int) ([]Asset, error) {
					assert.Equal(t, tt.wantLimit, limit)
					assert.Equal(t, tt.offset, offset)
					return nil, nil
				},
				countAssets: func(context.Context) (int64, error) { return 0, nil },
			}

			page, err := NewAssetService(store, ListBatched).List(context.Background(), tt.limit, tt.offset)
			if tt.wantKind != nil {
				require.ErrorIs(t, err, tt.wantKind)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantLimit, page.Limit)
		})
	}
}

// TestAssetListStrategies checks that every strategy returns the same page and
// counts the store calls each one makes: N+1 issues one allocations query per
// asset, the fixes issue one in total.
func TestAssetListStrategies(t *testing.T) {
	assets := []Asset{{ID: 1}, {ID: 2}, {ID: 3}}
	// Asset 1 has two allocations (newest first), asset 2 none, asset 3 one.
	allocs := map[int64][]Allocation{
		1: {{ID: 11, AssetID: 1}, {ID: 10, AssetID: 1}},
		3: {{ID: 30, AssetID: 3}},
	}
	want := []AssetWithAllocations{
		{Asset: assets[0], Allocations: allocs[1]},
		{Asset: assets[1], Allocations: []Allocation{}},
		{Asset: assets[2], Allocations: allocs[3]},
	}

	tests := []struct {
		strategy      ListStrategy
		wantAllocCall int // store calls that load allocations
	}{
		{ListNPlusOne, len(assets)},
		{ListBatched, 1},
		{ListJoin, 1},
	}

	for _, tt := range tests {
		t.Run(string(tt.strategy), func(t *testing.T) {
			calls := 0
			store := &mockStore{
				listAssets:  func(context.Context, int, int) ([]Asset, error) { return assets, nil },
				countAssets: func(context.Context) (int64, error) { return 3, nil },
				listAllocationsByAsset: func(_ context.Context, id int64) ([]Allocation, error) {
					calls++
					if a := allocs[id]; a != nil {
						return a, nil
					}
					return []Allocation{}, nil
				},
				listAllocationsByAssetIDs: func(_ context.Context, ids []int64) ([]Allocation, error) {
					calls++
					assert.Equal(t, []int64{1, 2, 3}, ids)
					// Flat, newest-first across assets, as the SQL returns it.
					return []Allocation{allocs[3][0], allocs[1][0], allocs[1][1]}, nil
				},
				listAssetsWithAllocations: func(context.Context, int, int) ([]AssetWithAllocations, error) {
					calls++
					return want, nil
				},
			}

			page, err := NewAssetService(store, tt.strategy).List(context.Background(), 0, 0)
			require.NoError(t, err)

			assert.Equal(t, tt.wantAllocCall, calls)
			assert.Equal(t, want, page.Items)
		})
	}
}

func TestAssetDelete(t *testing.T) {
	tests := []struct {
		name     string
		storeErr error
		wantKind error
	}{
		{name: "ok"},
		{name: "not found", storeErr: ErrNotFound, wantKind: ErrNotFound},
		{name: "has allocation history", storeErr: ErrConflict, wantKind: ErrConflict},
		{name: "db down", storeErr: errors.New("connection refused")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &mockStore{deleteAsset: func(context.Context, int64) error { return tt.storeErr }}

			err := NewAssetService(store, ListBatched).Delete(context.Background(), 5)
			switch {
			case tt.wantKind != nil:
				require.ErrorIs(t, err, tt.wantKind)
			case tt.storeErr != nil:
				require.Error(t, err)
				var svcErr *Error
				assert.False(t, errors.As(err, &svcErr), "internal errors must not be client-facing")
			default:
				require.NoError(t, err)
			}
		})
	}
}
