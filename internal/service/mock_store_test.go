package service

import "context"

// mockStore implements Store with per-test function fields. The embedded nil
// Store means any method a test did not stub panics, so an unexpected call
// fails the test loudly instead of silently returning zero values.
type mockStore struct {
	Store

	createUser             func(context.Context, NewUser) (User, error)
	getUser                func(context.Context, int64) (User, error)
	getUserCredentials     func(context.Context, string) (UserCredentials, error)
	createAsset            func(context.Context, NewAsset) (Asset, error)
	getAssetForUpdate      func(context.Context, int64) (Asset, error)
	listAssets             func(context.Context, AssetFilter, int, int) ([]Asset, error)
	countAssets            func(context.Context, AssetFilter) (int64, error)
	setAssetStatus         func(context.Context, int64, AssetStatus) (Asset, error)
	deleteAsset            func(context.Context, int64) error
	createAllocation       func(context.Context, int64, int64) (Allocation, error)
	listAllocationsByAsset func(context.Context, int64) ([]Allocation, error)

	listAllocationsByAssetIDs func(context.Context, []int64) ([]Allocation, error)
	listAssetsWithAllocations func(context.Context, AssetFilter, int, int) ([]AssetWithAllocations, error)
	enqueueNotification       func(context.Context, string, []byte) error
	listUsers                 func(context.Context, string, int, int) ([]User, error)
	countUsers                func(context.Context, string) (int64, error)
	listNotifications         func(context.Context, *int64, int) ([]Notification, error)
}

func (m *mockStore) EnqueueNotification(ctx context.Context, kind string, payload []byte) error {
	return m.enqueueNotification(ctx, kind, payload)
}

func (m *mockStore) ListAllocationsByAssetIDs(ctx context.Context, ids []int64) ([]Allocation, error) {
	return m.listAllocationsByAssetIDs(ctx, ids)
}

func (m *mockStore) ListAssetsWithAllocations(ctx context.Context, f AssetFilter, limit, offset int) ([]AssetWithAllocations, error) {
	return m.listAssetsWithAllocations(ctx, f, limit, offset)
}

func (m *mockStore) CreateUser(ctx context.Context, u NewUser) (User, error) {
	return m.createUser(ctx, u)
}

func (m *mockStore) GetUser(ctx context.Context, id int64) (User, error) {
	return m.getUser(ctx, id)
}

func (m *mockStore) GetUserCredentials(ctx context.Context, email string) (UserCredentials, error) {
	return m.getUserCredentials(ctx, email)
}

func (m *mockStore) CreateAsset(ctx context.Context, a NewAsset) (Asset, error) {
	return m.createAsset(ctx, a)
}

func (m *mockStore) GetAssetForUpdate(ctx context.Context, id int64) (Asset, error) {
	return m.getAssetForUpdate(ctx, id)
}

func (m *mockStore) ListAssets(ctx context.Context, f AssetFilter, limit, offset int) ([]Asset, error) {
	return m.listAssets(ctx, f, limit, offset)
}

func (m *mockStore) CountAssets(ctx context.Context, f AssetFilter) (int64, error) {
	return m.countAssets(ctx, f)
}

func (m *mockStore) SetAssetStatus(ctx context.Context, id int64, s AssetStatus) (Asset, error) {
	return m.setAssetStatus(ctx, id, s)
}

func (m *mockStore) DeleteAsset(ctx context.Context, id int64) error {
	return m.deleteAsset(ctx, id)
}

func (m *mockStore) CreateAllocation(ctx context.Context, assetID, userID int64) (Allocation, error) {
	return m.createAllocation(ctx, assetID, userID)
}

func (m *mockStore) ListAllocationsByAsset(ctx context.Context, assetID int64) ([]Allocation, error) {
	return m.listAllocationsByAsset(ctx, assetID)
}

func (m *mockStore) InTx(_ context.Context, fn func(Store) error) error {
	return fn(m)
}

func (m *mockStore) ListUsers(ctx context.Context, pattern string, limit, offset int) ([]User, error) {
	return m.listUsers(ctx, pattern, limit, offset)
}

func (m *mockStore) CountUsers(ctx context.Context, pattern string) (int64, error) {
	return m.countUsers(ctx, pattern)
}

func (m *mockStore) ListNotifications(ctx context.Context, userID *int64, limit int) ([]Notification, error) {
	return m.listNotifications(ctx, userID, limit)
}
