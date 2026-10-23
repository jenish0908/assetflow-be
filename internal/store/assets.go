package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/jenish0908/assetflow/internal/service"
	"github.com/jenish0908/assetflow/internal/store/db"
)

func (s *Store) CreateAsset(ctx context.Context, a service.NewAsset) (service.Asset, error) {
	row, err := s.q.CreateAsset(ctx, db.CreateAssetParams{Tag: a.Tag, Name: a.Name, Category: a.Category})
	return toAsset(row), translate(err)
}

func (s *Store) GetAsset(ctx context.Context, id int64) (service.Asset, error) {
	row, err := s.q.GetAsset(ctx, id)
	return toAsset(row), translate(err)
}

func (s *Store) GetAssetByTag(ctx context.Context, tag string) (service.Asset, error) {
	row, err := s.q.GetAssetByTag(ctx, tag)
	return toAsset(row), translate(err)
}

func (s *Store) GetAssetForUpdate(ctx context.Context, id int64) (service.Asset, error) {
	row, err := s.q.GetAssetForUpdate(ctx, id)
	return toAsset(row), translate(err)
}

// ListAssets expects limit and offset already validated by the service, so
// the int32 conversions cannot overflow.
func (s *Store) ListAssets(ctx context.Context, limit, offset int) ([]service.Asset, error) {
	rows, err := s.q.ListAssets(ctx, db.ListAssetsParams{Limit: int32(limit), Offset: int32(offset)})
	if err != nil {
		return nil, translate(err)
	}
	out := make([]service.Asset, len(rows))
	for i, r := range rows {
		out[i] = toAsset(r)
	}
	return out, nil
}

// ListAssetsWithAllocations runs the JOIN query and folds the flat rows (one
// per allocation, or one with NULL allocation columns for an asset that has
// none) back into one entry per asset. Rows arrive ordered by asset ID.
func (s *Store) ListAssetsWithAllocations(ctx context.Context, limit, offset int) ([]service.AssetWithAllocations, error) {
	rows, err := s.q.ListAssetsWithAllocations(ctx, db.ListAssetsWithAllocationsParams{Limit: int32(limit), Offset: int32(offset)})
	if err != nil {
		return nil, translate(err)
	}

	out := make([]service.AssetWithAllocations, 0, limit)
	for _, r := range rows {
		if len(out) == 0 || out[len(out)-1].ID != r.ID {
			out = append(out, service.AssetWithAllocations{
				Asset: toAsset(db.Asset{
					ID: r.ID, Tag: r.Tag, Name: r.Name, Category: r.Category,
					Status: r.Status, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
				}),
				Allocations: []service.Allocation{},
			})
		}
		if r.AllocationID == nil {
			continue // LEFT JOIN found no allocations for this asset
		}
		last := &out[len(out)-1]
		last.Allocations = append(last.Allocations, service.Allocation{
			ID:          *r.AllocationID,
			AssetID:     r.ID,
			UserID:      *r.AllocationUserID,
			AllocatedAt: *r.AllocationAllocatedAt,
			ReturnedAt:  r.AllocationReturnedAt,
		})
	}
	return out, nil
}

func (s *Store) CountAssets(ctx context.Context) (int64, error) {
	n, err := s.q.CountAssets(ctx)
	return n, translate(err)
}

func (s *Store) UpdateAsset(ctx context.Context, id int64, p service.AssetPatch) (service.Asset, error) {
	row, err := s.q.UpdateAsset(ctx, db.UpdateAssetParams{ID: id, Name: p.Name, Category: p.Category})
	return toAsset(row), translate(err)
}

func (s *Store) SetAssetStatus(ctx context.Context, id int64, status service.AssetStatus) (service.Asset, error) {
	row, err := s.q.SetAssetStatus(ctx, db.SetAssetStatusParams{ID: id, Status: string(status)})
	return toAsset(row), translate(err)
}

func (s *Store) DeleteAsset(ctx context.Context, id int64) error {
	n, err := s.q.DeleteAsset(ctx, id)
	if err != nil {
		return translate(err)
	}
	if n == 0 {
		return fmt.Errorf("delete asset %d: %w", id, translate(pgx.ErrNoRows))
	}
	return nil
}

func (s *Store) CreateAllocation(ctx context.Context, assetID, userID int64) (service.Allocation, error) {
	row, err := s.q.CreateAllocation(ctx, db.CreateAllocationParams{AssetID: assetID, UserID: userID})
	return toAllocation(row), translate(err)
}

func (s *Store) CloseActiveAllocation(ctx context.Context, assetID int64) (service.Allocation, error) {
	row, err := s.q.CloseActiveAllocation(ctx, assetID)
	return toAllocation(row), translate(err)
}

func (s *Store) ListAllocationsByAsset(ctx context.Context, assetID int64) ([]service.Allocation, error) {
	rows, err := s.q.ListAllocationsByAsset(ctx, assetID)
	if err != nil {
		return nil, translate(err)
	}
	out := make([]service.Allocation, len(rows))
	for i, r := range rows {
		out[i] = toAllocation(r)
	}
	return out, nil
}

func (s *Store) ListAllocationsByAssetIDs(ctx context.Context, assetIDs []int64) ([]service.Allocation, error) {
	rows, err := s.q.ListAllocationsByAssetIDs(ctx, assetIDs)
	if err != nil {
		return nil, translate(err)
	}
	out := make([]service.Allocation, len(rows))
	for i, r := range rows {
		out[i] = toAllocation(r)
	}
	return out, nil
}

func (s *Store) EnqueueNotification(ctx context.Context, kind string, payload []byte) error {
	return translate(s.q.EnqueueNotification(ctx, db.EnqueueNotificationParams{Kind: kind, Payload: payload}))
}

func toAsset(r db.Asset) service.Asset {
	return service.Asset{
		ID:        r.ID,
		Tag:       r.Tag,
		Name:      r.Name,
		Category:  r.Category,
		Status:    service.AssetStatus(r.Status),
		CreatedAt: r.CreatedAt,
		UpdatedAt: r.UpdatedAt,
	}
}

func toAllocation(r db.Allocation) service.Allocation {
	return service.Allocation{
		ID:          r.ID,
		AssetID:     r.AssetID,
		UserID:      r.UserID,
		AllocatedAt: r.AllocatedAt,
		ReturnedAt:  r.ReturnedAt,
	}
}
