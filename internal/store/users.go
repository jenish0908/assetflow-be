package store

import (
	"context"

	"github.com/jenish0908/assetflow/internal/auth"
	"github.com/jenish0908/assetflow/internal/service"
	"github.com/jenish0908/assetflow/internal/store/db"
)

func (s *Store) CreateUser(ctx context.Context, u service.NewUser) (service.User, error) {
	row, err := s.q.CreateUser(ctx, db.CreateUserParams{
		Email:        u.Email,
		Name:         u.Name,
		PasswordHash: u.PasswordHash,
		Role:         string(u.Role),
	})
	return toUser(row), translate(err)
}

func (s *Store) GetUser(ctx context.Context, id int64) (service.User, error) {
	row, err := s.q.GetUser(ctx, id)
	return toUser(row), translate(err)
}

func (s *Store) GetUserCredentials(ctx context.Context, email string) (service.UserCredentials, error) {
	row, err := s.q.GetUserByEmail(ctx, email)
	return service.UserCredentials{User: toUser(row), PasswordHash: row.PasswordHash}, translate(err)
}

func (s *Store) FindUsers(ctx context.Context, query string) ([]service.User, error) {
	rows, err := s.q.FindUsers(ctx, query)
	if err != nil {
		return nil, translate(err)
	}
	out := make([]service.User, len(rows))
	for i, r := range rows {
		out[i] = toUser(r)
	}
	return out, nil
}

func toUser(r db.User) service.User {
	return service.User{
		ID:        r.ID,
		Email:     r.Email,
		Name:      r.Name,
		Role:      auth.Role(r.Role),
		CreatedAt: r.CreatedAt,
	}
}
