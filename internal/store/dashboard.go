package store

import (
	"context"

	"github.com/jenish0908/assetflow-be/internal/service"
	"github.com/jenish0908/assetflow-be/internal/store/db"
)

// Stats runs the overview's aggregate queries. They are separate statements
// (not one snapshot); a count may be a moment older than the next, which is
// fine for a dashboard.
func (s *Store) Stats(ctx context.Context) (service.Stats, error) {
	st := service.Stats{
		AssetsByStatus:   map[service.AssetStatus]int64{},
		JobsByStatus:     map[string]int64{},
		PaymentsByStatus: map[string]service.PaymentSummary{},
	}

	assets, err := s.q.AssetStatusCounts(ctx)
	if err != nil {
		return service.Stats{}, translate(err)
	}
	for _, r := range assets {
		st.AssetsByStatus[service.AssetStatus(r.Status)] = r.Total
		st.Assets += r.Total
	}

	jobs, err := s.q.JobStatusCounts(ctx)
	if err != nil {
		return service.Stats{}, translate(err)
	}
	for _, r := range jobs {
		st.JobsByStatus[r.Status] = r.Total
	}

	payments, err := s.q.PaymentStatusTotals(ctx)
	if err != nil {
		return service.Stats{}, translate(err)
	}
	for _, r := range payments {
		st.PaymentsByStatus[r.Status] = service.PaymentSummary{Count: r.Total, AmountCents: r.AmountCents}
	}

	if st.ActiveAllocations, err = s.q.CountActiveAllocations(ctx); err != nil {
		return service.Stats{}, translate(err)
	}
	if st.Users, err = s.q.CountUsers(ctx, nil); err != nil {
		return service.Stats{}, translate(err)
	}
	return st, nil
}

func (s *Store) RecentAllocations(ctx context.Context, limit int) ([]service.AllocationEvent, error) {
	rows, err := s.q.RecentAllocations(ctx, int32(limit))
	if err != nil {
		return nil, translate(err)
	}
	out := make([]service.AllocationEvent, len(rows))
	for i, r := range rows {
		out[i] = service.AllocationEvent{
			ID: r.ID, AssetID: r.AssetID, AssetTag: r.AssetTag, AssetName: r.AssetName,
			UserID: r.UserID, UserName: r.UserName, AllocatedAt: r.AllocatedAt, ReturnedAt: r.ReturnedAt,
		}
	}
	return out, nil
}

func (s *Store) ListNotifications(ctx context.Context, userID *int64, limit int) ([]service.Notification, error) {
	rows, err := s.q.ListNotifications(ctx, db.ListNotificationsParams{UserID: userID, Lim: int32(limit)})
	if err != nil {
		return nil, translate(err)
	}
	out := make([]service.Notification, len(rows))
	for i, r := range rows {
		out[i] = service.Notification{
			ID: r.ID, Kind: r.Kind, Payload: r.Payload, Status: r.Status,
			Attempts: int(r.Attempts), MaxAttempts: int(r.MaxAttempts), RunAt: r.RunAt,
			LastError: r.LastError, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
		}
	}
	return out, nil
}
