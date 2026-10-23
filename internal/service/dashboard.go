package service

import (
	"context"
	"fmt"

	"github.com/jenish0908/assetflow-be/internal/auth"
)

// DashboardService serves read-only summaries: the overview and the
// notification feed.
type DashboardService struct {
	store Store
}

func NewDashboardService(store Store) *DashboardService {
	return &DashboardService{store: store}
}

type Overview struct {
	Stats  Stats             `json:"stats"`
	Recent []AllocationEvent `json:"recent_allocations"`
}

func (s *DashboardService) Overview(ctx context.Context) (Overview, error) {
	stats, err := s.store.Stats(ctx)
	if err != nil {
		return Overview{}, fmt.Errorf("overview stats: %w", err)
	}
	recent, err := s.store.RecentAllocations(ctx, 8)
	if err != nil {
		return Overview{}, fmt.Errorf("overview recent allocations: %w", err)
	}
	return Overview{Stats: stats, Recent: recent}, nil
}

// Notifications returns the caller's notifications, or everyone's for an
// admin, newest first.
func (s *DashboardService) Notifications(ctx context.Context, actor auth.Claims, limit int) ([]Notification, error) {
	limit, _, err := Paginate(limit, 0)
	if err != nil {
		return nil, err
	}
	var userID *int64
	if actor.Role != auth.RoleAdmin {
		userID = &actor.UserID
	}
	items, err := s.store.ListNotifications(ctx, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("list notifications: %w", err)
	}
	return items, nil
}
