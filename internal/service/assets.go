package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/jenish0908/assetflow/internal/notify"
)

const (
	defaultPageSize = 50
	maxPageSize     = 100
)

// ListStrategy selects how List loads each asset's allocations. All three
// return identical results; they live side by side so scripts/perf_list_assets.sh
// can re-measure the "before" (N+1) as well as the fixes.
type ListStrategy string

const (
	ListNPlusOne ListStrategy = "nplus1"  // one allocations query per asset
	ListBatched  ListStrategy = "batched" // one query: asset_id = ANY($1)
	ListJoin     ListStrategy = "join"    // one query: assets LEFT JOIN allocations
)

func (s ListStrategy) Valid() bool {
	switch s {
	case ListNPlusOne, ListBatched, ListJoin:
		return true
	}
	return false
}

type AssetService struct {
	store        Store
	listStrategy ListStrategy
}

func NewAssetService(store Store, listStrategy ListStrategy) *AssetService {
	return &AssetService{store: store, listStrategy: listStrategy}
}

func (in *NewAsset) normalize() {
	in.Tag = strings.TrimSpace(in.Tag)
	in.Name = strings.TrimSpace(in.Name)
	in.Category = strings.TrimSpace(in.Category)
}

func (in NewAsset) validate() error {
	if err := requireText("tag", in.Tag, 64); err != nil {
		return err
	}
	if err := requireText("name", in.Name, 200); err != nil {
		return err
	}
	return requireText("category", in.Category, 100)
}

func (s *AssetService) Create(ctx context.Context, in NewAsset) (Asset, error) {
	in.normalize()
	if err := in.validate(); err != nil {
		return Asset{}, err
	}

	a, err := s.store.CreateAsset(ctx, in)
	if errors.Is(err, ErrConflict) {
		return Asset{}, NewError(ErrConflict, "asset tag %q already exists", in.Tag)
	}
	if err != nil {
		return Asset{}, fmt.Errorf("create asset: %w", err)
	}
	return a, nil
}

// Get returns one asset with its full allocation history.
func (s *AssetService) Get(ctx context.Context, id int64) (AssetWithAllocations, error) {
	a, err := s.store.GetAsset(ctx, id)
	if err != nil {
		return AssetWithAllocations{}, fmt.Errorf("get asset: %w", notFound(err, "asset", id))
	}
	allocs, err := s.store.ListAllocationsByAsset(ctx, id)
	if err != nil {
		return AssetWithAllocations{}, fmt.Errorf("get asset allocations: %w", err)
	}
	return AssetWithAllocations{Asset: a, Allocations: allocs}, nil
}

// GetByTag returns the asset with the given tag (e.g. "L-102") and its
// allocation history.
func (s *AssetService) GetByTag(ctx context.Context, tag string) (AssetWithAllocations, error) {
	tag = strings.TrimSpace(tag)
	if err := requireText("tag", tag, 64); err != nil {
		return AssetWithAllocations{}, err
	}
	a, err := s.store.GetAssetByTag(ctx, tag)
	if errors.Is(err, ErrNotFound) {
		return AssetWithAllocations{}, NewError(ErrNotFound, "asset with tag %q not found", tag)
	}
	if err != nil {
		return AssetWithAllocations{}, fmt.Errorf("get asset by tag: %w", err)
	}
	return s.Get(ctx, a.ID)
}

// List returns a page of assets, each with its allocation history, plus the
// total asset count. See ListStrategy for how allocations are loaded.
func (s *AssetService) List(ctx context.Context, limit, offset int) (AssetPage, error) {
	if limit == 0 {
		limit = defaultPageSize
	}
	if limit < 1 || limit > maxPageSize {
		return AssetPage{}, NewError(ErrInvalid, "limit must be between 1 and %d", maxPageSize)
	}
	if offset < 0 || offset > math.MaxInt32 {
		return AssetPage{}, NewError(ErrInvalid, "offset must be between 0 and %d", math.MaxInt32)
	}

	var items []AssetWithAllocations
	var err error
	switch s.listStrategy {
	case ListJoin:
		items, err = s.store.ListAssetsWithAllocations(ctx, limit, offset)
	case ListNPlusOne:
		items, err = s.listNPlusOne(ctx, limit, offset)
	default:
		items, err = s.listBatched(ctx, limit, offset)
	}
	if err != nil {
		return AssetPage{}, fmt.Errorf("list assets: %w", err)
	}

	total, err := s.store.CountAssets(ctx)
	if err != nil {
		return AssetPage{}, fmt.Errorf("count assets: %w", err)
	}
	return AssetPage{Items: items, Limit: limit, Offset: offset, Total: total}, nil
}

// listNPlusOne is the naive version kept for measurement: 1 query for the
// page, then 1 query per asset. With a page of 50 that is 51 round trips.
func (s *AssetService) listNPlusOne(ctx context.Context, limit, offset int) ([]AssetWithAllocations, error) {
	assets, err := s.store.ListAssets(ctx, limit, offset)
	if err != nil {
		return nil, err
	}
	items := make([]AssetWithAllocations, 0, len(assets))
	for _, a := range assets {
		allocs, err := s.store.ListAllocationsByAsset(ctx, a.ID)
		if err != nil {
			return nil, fmt.Errorf("allocations for asset %d: %w", a.ID, err)
		}
		items = append(items, AssetWithAllocations{Asset: a, Allocations: allocs})
	}
	return items, nil
}

// listBatched fetches the page, then all of its allocations in one query,
// and groups them by asset ID in a map: 2 round trips for any page size.
func (s *AssetService) listBatched(ctx context.Context, limit, offset int) ([]AssetWithAllocations, error) {
	assets, err := s.store.ListAssets(ctx, limit, offset)
	if err != nil {
		return nil, err
	}
	if len(assets) == 0 {
		return []AssetWithAllocations{}, nil
	}

	ids := make([]int64, len(assets))
	for i, a := range assets {
		ids[i] = a.ID
	}
	allocs, err := s.store.ListAllocationsByAssetIDs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("allocations for page: %w", err)
	}

	// The query returns allocations newest first; appending keeps that order
	// within each asset's slice.
	byAsset := make(map[int64][]Allocation, len(assets))
	for _, al := range allocs {
		byAsset[al.AssetID] = append(byAsset[al.AssetID], al)
	}

	items := make([]AssetWithAllocations, len(assets))
	for i, a := range assets {
		list := byAsset[a.ID]
		if list == nil {
			list = []Allocation{} // encode as [] rather than null
		}
		items[i] = AssetWithAllocations{Asset: a, Allocations: list}
	}
	return items, nil
}

func (s *AssetService) Update(ctx context.Context, id int64, p AssetPatch) (Asset, error) {
	if p.Name == nil && p.Category == nil {
		return Asset{}, NewError(ErrInvalid, "nothing to update: set name and/or category")
	}
	if p.Name != nil {
		*p.Name = strings.TrimSpace(*p.Name)
		if err := requireText("name", *p.Name, 200); err != nil {
			return Asset{}, err
		}
	}
	if p.Category != nil {
		*p.Category = strings.TrimSpace(*p.Category)
		if err := requireText("category", *p.Category, 100); err != nil {
			return Asset{}, err
		}
	}

	a, err := s.store.UpdateAsset(ctx, id, p)
	if err != nil {
		return Asset{}, fmt.Errorf("update asset: %w", notFound(err, "asset", id))
	}
	return a, nil
}

func (s *AssetService) Delete(ctx context.Context, id int64) error {
	err := s.store.DeleteAsset(ctx, id)
	if errors.Is(err, ErrConflict) {
		return NewError(ErrConflict, "asset %d has allocation history; retire it instead", id)
	}
	if err != nil {
		return fmt.Errorf("delete asset: %w", notFound(err, "asset", id))
	}
	return nil
}

// Allocate assigns an available asset to a user.
//
// The asset row is locked (SELECT ... FOR UPDATE) for the whole transaction,
// so concurrent requests for the same asset run one after another and the
// second one sees status "allocated". The partial unique index on
// allocations is a second line of defence.
//
// The "asset allocated" notification job is inserted in the same
// transaction (transactional outbox): it exists if and only if the
// allocation committed, and the worker delivers it later.
func (s *AssetService) Allocate(ctx context.Context, assetID, userID int64) (Allocation, error) {
	var alloc Allocation
	err := s.store.InTx(ctx, func(tx Store) error {
		a, err := tx.GetAssetForUpdate(ctx, assetID)
		if err != nil {
			return notFound(err, "asset", assetID)
		}
		if a.Status != StatusAvailable {
			return NewError(ErrConflict, "asset %d is %s, not available", assetID, a.Status)
		}
		if _, err := tx.GetUser(ctx, userID); err != nil {
			return notFound(err, "user", userID)
		}

		alloc, err = tx.CreateAllocation(ctx, assetID, userID)
		if errors.Is(err, ErrConflict) {
			return NewError(ErrConflict, "asset %d is already allocated", assetID)
		}
		if err != nil {
			return err
		}
		if _, err := tx.SetAssetStatus(ctx, assetID, StatusAllocated); err != nil {
			return err
		}

		payload, err := json.Marshal(map[string]any{"asset_id": a.ID, "asset_tag": a.Tag, "user_id": userID})
		if err != nil {
			return err
		}
		return tx.EnqueueNotification(ctx, notify.KindAssetAllocated, payload)
	})
	if err != nil {
		return Allocation{}, fmt.Errorf("allocate asset: %w", err)
	}
	return alloc, nil
}

// Return ends the asset's active allocation and makes it available again.
func (s *AssetService) Return(ctx context.Context, assetID int64) (Allocation, error) {
	var alloc Allocation
	err := s.store.InTx(ctx, func(tx Store) error {
		a, err := tx.GetAssetForUpdate(ctx, assetID)
		if err != nil {
			return notFound(err, "asset", assetID)
		}
		if a.Status != StatusAllocated {
			return NewError(ErrConflict, "asset %d is not allocated", assetID)
		}

		alloc, err = tx.CloseActiveAllocation(ctx, assetID)
		if err != nil {
			return err
		}
		_, err = tx.SetAssetStatus(ctx, assetID, StatusAvailable)
		return err
	})
	if err != nil {
		return Allocation{}, fmt.Errorf("return asset: %w", err)
	}
	return alloc, nil
}

// UpdateStatus moves an asset between available, maintenance and retired.
// "allocated" is only reachable through Allocate, and an allocated asset must
// be returned first, so status and allocations can never disagree.
func (s *AssetService) UpdateStatus(ctx context.Context, id int64, status AssetStatus) (Asset, error) {
	switch status {
	case StatusAvailable, StatusMaintenance, StatusRetired:
	case StatusAllocated:
		return Asset{}, NewError(ErrInvalid, "use allocate to mark an asset as allocated")
	default:
		return Asset{}, NewError(ErrInvalid, "status must be one of available, maintenance, retired")
	}

	var updated Asset
	err := s.store.InTx(ctx, func(tx Store) error {
		a, err := tx.GetAssetForUpdate(ctx, id)
		if err != nil {
			return notFound(err, "asset", id)
		}
		switch {
		case a.Status == StatusAllocated:
			return NewError(ErrConflict, "asset %d is allocated; return it first", id)
		case a.Status == StatusRetired && status != StatusRetired:
			return NewError(ErrConflict, "asset %d is retired", id)
		}

		updated, err = tx.SetAssetStatus(ctx, id, status)
		return err
	})
	if err != nil {
		return Asset{}, fmt.Errorf("update asset status: %w", err)
	}
	return updated, nil
}

func requireText(field, v string, maxLen int) error {
	if v == "" {
		return NewError(ErrInvalid, "%s is required", field)
	}
	if utf8.RuneCountInString(v) > maxLen {
		return NewError(ErrInvalid, "%s must be at most %d characters", field, maxLen)
	}
	return nil
}
