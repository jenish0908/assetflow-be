package store_test

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jenish0908/assetflow-be/internal/auth"
	"github.com/jenish0908/assetflow-be/internal/service"
	"github.com/jenish0908/assetflow-be/internal/store"
)

// These tests need a real, migrated Postgres:
//
//	TEST_DATABASE_URL=postgres://assetflow:assetflow@localhost:5432/assetflow_test?sslmode=disable
//
// They create their own uniquely named rows, so they can share a database.
func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping integration test")
	}
	pool, err := store.NewPool(context.Background(), url)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return store.New(pool)
}

func uniqueSuffix() string {
	return fmt.Sprintf("%d", time.Now().UnixNano())
}

// TestListStrategiesAgree is the safety net for the N+1 fix: the batched and
// JOIN implementations must return exactly what the naive one returns, for
// every page of the table.
func TestListStrategiesAgree(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	sfx := uniqueSuffix()
	assets := service.NewAssetService(st, service.ListBatched)

	user, err := st.CreateUser(ctx, service.NewUser{
		Email: "list-" + sfx + "@test.dev", Name: "List", PasswordHash: "x", Role: auth.RoleViewer,
	})
	require.NoError(t, err)

	// Asset 0: no allocations. Asset 1: two returned + one active.
	// Asset 2: one active. Asset 3: one returned.
	created := make([]service.Asset, 4)
	for i := range created {
		created[i], err = assets.Create(ctx, service.NewAsset{
			Tag: fmt.Sprintf("T-%s-%d", sfx, i), Name: "test", Category: "test",
		})
		require.NoError(t, err)
	}
	allocate := func(a service.Asset) {
		_, err := assets.Allocate(ctx, a.ID, user.ID)
		require.NoError(t, err)
	}
	giveBack := func(a service.Asset) {
		_, err := assets.Return(ctx, a.ID)
		require.NoError(t, err)
	}
	allocate(created[1])
	giveBack(created[1])
	allocate(created[1])
	giveBack(created[1])
	allocate(created[1])
	allocate(created[2])
	allocate(created[3])
	giveBack(created[3])

	strategies := []service.ListStrategy{service.ListNPlusOne, service.ListBatched, service.ListJoin}
	const pageSize = 3 // small pages so the test crosses page boundaries
	for offset := 0; ; offset += pageSize {
		pages := make([]service.AssetPage, len(strategies))
		for i, s := range strategies {
			pages[i], err = service.NewAssetService(st, s).List(ctx, service.AssetFilter{}, pageSize, offset)
			require.NoError(t, err, "strategy %s offset %d", s, offset)
		}
		for i := 1; i < len(pages); i++ {
			require.Equal(t, pages[0], pages[i], "%s differs from %s at offset %d", strategies[i], strategies[0], offset)
		}
		if len(pages[0].Items) < pageSize {
			break
		}
	}

	// Spot-check the asset with history: newest allocation first and active.
	got, err := assets.Get(ctx, created[1].ID)
	require.NoError(t, err)
	require.Len(t, got.Allocations, 3)
	assert.Nil(t, got.Allocations[0].ReturnedAt)
	assert.NotNil(t, got.Allocations[1].ReturnedAt)
}

// TestConcurrentAllocateOnlyOneWins fires many allocations of the same asset
// at once. The row lock plus the partial unique index must let exactly one
// succeed; every other caller gets ErrConflict.
func TestConcurrentAllocateOnlyOneWins(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	sfx := uniqueSuffix()
	assets := service.NewAssetService(st, service.ListBatched)

	user, err := st.CreateUser(ctx, service.NewUser{
		Email: "race-" + sfx + "@test.dev", Name: "Race", PasswordHash: "x", Role: auth.RoleViewer,
	})
	require.NoError(t, err)
	asset, err := assets.Create(ctx, service.NewAsset{Tag: "RACE-" + sfx, Name: "race", Category: "test"})
	require.NoError(t, err)

	const callers = 10
	var wg sync.WaitGroup
	errs := make([]error, callers)
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = assets.Allocate(ctx, asset.ID, user.ID)
		}()
	}
	wg.Wait()

	wins := 0
	for _, err := range errs {
		if err == nil {
			wins++
			continue
		}
		assert.ErrorIs(t, err, service.ErrConflict)
	}
	assert.Equal(t, 1, wins)
}

// TestDirectoryQueries checks the SQL behind filtering and lookups.
func TestDirectoryQueries(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	sfx := uniqueSuffix()
	assets := service.NewAssetService(st, service.ListBatched)
	users := service.NewUserService(st, nil)

	u, err := st.CreateUser(ctx, service.NewUser{Email: "dir-" + sfx + "@test.dev", Name: "Dir " + sfx, PasswordHash: "x", Role: auth.RoleViewer})
	require.NoError(t, err)
	a1, err := assets.Create(ctx, service.NewAsset{Tag: "DIR-" + sfx + "-1", Name: "Zeta 50% off " + sfx, Category: "laptop"})
	require.NoError(t, err)
	_, err = assets.Create(ctx, service.NewAsset{Tag: "DIR-" + sfx + "-2", Name: "Zeta 500 " + sfx, Category: "laptop"})
	require.NoError(t, err)
	_, err = assets.UpdateStatus(ctx, a1.ID, service.StatusMaintenance)
	require.NoError(t, err)

	page, err := assets.List(ctx, service.AssetFilter{Query: "DIR-" + sfx}, 100, 0)
	require.NoError(t, err)
	assert.Equal(t, int64(2), page.Total, "search matches tags")

	page, err = assets.List(ctx, service.AssetFilter{Query: "50% off " + sfx}, 100, 0)
	require.NoError(t, err)
	assert.Equal(t, int64(1), page.Total, "%% in the search is literal, not a wildcard")

	page, err = assets.List(ctx, service.AssetFilter{Query: "dir-" + sfx, Status: service.StatusMaintenance}, 100, 0)
	require.NoError(t, err)
	require.Equal(t, int64(1), page.Total, "case-insensitive search combined with status")
	assert.Equal(t, a1.ID, page.Items[0].ID)

	// Every list strategy, including the filtered JOIN query, returns the same page.
	filter := service.AssetFilter{Query: "DIR-" + sfx}
	var want service.AssetPage
	for i, s := range []service.ListStrategy{service.ListNPlusOne, service.ListBatched, service.ListJoin} {
		got, err := service.NewAssetService(st, s).List(ctx, filter, 100, 0)
		require.NoError(t, err)
		if i == 0 {
			want = got
			continue
		}
		require.Equal(t, want, got, "strategy %s with a filter", s)
	}

	found, err := users.List(ctx, "dir "+sfx, 10, 0)
	require.NoError(t, err)
	require.Equal(t, int64(1), found.Total)

	many, err := users.GetMany(ctx, []int64{u.ID, 1 << 40})
	require.NoError(t, err)
	require.Len(t, many, 1, "unknown IDs are skipped")
	assert.Equal(t, u.ID, many[0].ID)

	stats, err := st.Stats(ctx)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, stats.AssetsByStatus[service.StatusMaintenance], int64(1))
	assert.GreaterOrEqual(t, stats.Users, int64(1))
}
