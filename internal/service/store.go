package service

import "context"

// Store is the persistence port the services depend on. It is defined here,
// by the consumer, and implemented by internal/store. Implementations return
// ErrNotFound for missing rows and ErrConflict for unique/foreign-key
// violations (wrapped, so errors.Is works).
type Store interface {
	CreateUser(ctx context.Context, u NewUser) (User, error)
	GetUser(ctx context.Context, id int64) (User, error)
	GetUserCredentials(ctx context.Context, email string) (UserCredentials, error)
	FindUsers(ctx context.Context, query string) ([]User, error)

	CreateAsset(ctx context.Context, a NewAsset) (Asset, error)
	GetAsset(ctx context.Context, id int64) (Asset, error)
	GetAssetByTag(ctx context.Context, tag string) (Asset, error)
	GetAssetForUpdate(ctx context.Context, id int64) (Asset, error)
	ListAssets(ctx context.Context, limit, offset int) ([]Asset, error)
	ListAssetsWithAllocations(ctx context.Context, limit, offset int) ([]AssetWithAllocations, error)
	CountAssets(ctx context.Context) (int64, error)
	UpdateAsset(ctx context.Context, id int64, p AssetPatch) (Asset, error)
	SetAssetStatus(ctx context.Context, id int64, status AssetStatus) (Asset, error)
	DeleteAsset(ctx context.Context, id int64) error

	CreateAllocation(ctx context.Context, assetID, userID int64) (Allocation, error)
	CloseActiveAllocation(ctx context.Context, assetID int64) (Allocation, error)
	ListAllocationsByAsset(ctx context.Context, assetID int64) ([]Allocation, error)
	ListAllocationsByAssetIDs(ctx context.Context, assetIDs []int64) ([]Allocation, error)

	// EnqueueNotification adds a notification job. Called inside InTx, the
	// job commits or rolls back together with the change it announces.
	EnqueueNotification(ctx context.Context, kind string, payload []byte) error

	// InTx runs fn inside a database transaction. fn receives a Store bound to
	// that transaction; returning an error rolls it back.
	InTx(ctx context.Context, fn func(Store) error) error
}
