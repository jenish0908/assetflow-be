// Package service holds the business rules. It knows nothing about HTTP or
// SQL: it talks to storage through the Store interface.
package service

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"

	"github.com/jenish0908/assetflow-be/internal/auth"
)

// TokenIssuer creates access tokens. *auth.TokenManager satisfies it.
type TokenIssuer interface {
	Issue(userID int64, role auth.Role) (string, error)
}

// The same message for "no such user" and "wrong password" so the API does
// not reveal which emails are registered.
var errBadCredentials = &Error{Kind: ErrUnauthenticated, Msg: "invalid email or password"}

type UserService struct {
	store  Store
	tokens TokenIssuer
}

func NewUserService(store Store, tokens TokenIssuer) *UserService {
	return &UserService{store: store, tokens: tokens}
}

type CreateUserInput struct {
	Email    string    `json:"email"`
	Name     string    `json:"name"`
	Password string    `json:"password"`
	Role     auth.Role `json:"role"`
}

func (in *CreateUserInput) normalize() {
	in.Email = normalizeEmail(in.Email)
	in.Name = strings.TrimSpace(in.Name)
}

func (in CreateUserInput) validate() error {
	if addr, err := mail.ParseAddress(in.Email); err != nil || addr.Address != in.Email {
		return NewError(ErrInvalid, "email must be a valid address")
	}
	if err := requireText("name", in.Name, 100); err != nil {
		return err
	}
	// bcrypt only uses the first 72 bytes, so longer passwords are rejected.
	if len(in.Password) < 8 || len(in.Password) > 72 {
		return NewError(ErrInvalid, "password must be 8 to 72 bytes")
	}
	if !in.Role.Valid() {
		return NewError(ErrInvalid, "role must be one of admin, manager, viewer")
	}
	return nil
}

func (s *UserService) Create(ctx context.Context, in CreateUserInput) (User, error) {
	in.normalize()
	if err := in.validate(); err != nil {
		return User{}, err
	}

	hash, err := auth.HashPassword(in.Password)
	if err != nil {
		return User{}, fmt.Errorf("create user: %w", err)
	}

	u, err := s.store.CreateUser(ctx, NewUser{
		Email:        in.Email,
		Name:         in.Name,
		PasswordHash: hash,
		Role:         in.Role,
	})
	if errors.Is(err, ErrConflict) {
		return User{}, NewError(ErrConflict, "email %q is already registered", in.Email)
	}
	if err != nil {
		return User{}, fmt.Errorf("create user: %w", err)
	}
	return u, nil
}

// Login checks the credentials and returns a signed access token.
func (s *UserService) Login(ctx context.Context, email, password string) (string, error) {
	creds, err := s.store.GetUserCredentials(ctx, normalizeEmail(email))
	if errors.Is(err, ErrNotFound) {
		return "", errBadCredentials
	}
	if err != nil {
		return "", fmt.Errorf("login: %w", err)
	}
	if !auth.CheckPassword(creds.PasswordHash, password) {
		return "", errBadCredentials
	}

	token, err := s.tokens.Issue(creds.ID, creds.Role)
	if err != nil {
		return "", fmt.Errorf("login: %w", err)
	}
	return token, nil
}

// Find returns users whose email or exact name matches query
// (case-insensitive), at most five.
func (s *UserService) Find(ctx context.Context, query string) ([]User, error) {
	query = strings.TrimSpace(query)
	if err := requireText("user", query, 200); err != nil {
		return nil, err
	}
	users, err := s.store.FindUsers(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("find users: %w", err)
	}
	return users, nil
}

func normalizeEmail(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// Get returns one user.
func (s *UserService) Get(ctx context.Context, id int64) (User, error) {
	u, err := s.store.GetUser(ctx, id)
	if err != nil {
		return User{}, fmt.Errorf("get user: %w", notFound(err, "user", id))
	}
	return u, nil
}

// List returns a page of the people directory, optionally filtered by a
// case-insensitive search on name and email.
func (s *UserService) List(ctx context.Context, query string, limit, offset int) (UserPage, error) {
	limit, offset, err := Paginate(limit, offset)
	if err != nil {
		return UserPage{}, err
	}
	pattern, err := likePattern(query)
	if err != nil {
		return UserPage{}, err
	}
	users, err := s.store.ListUsers(ctx, pattern, limit, offset)
	if err != nil {
		return UserPage{}, fmt.Errorf("list users: %w", err)
	}
	total, err := s.store.CountUsers(ctx, pattern)
	if err != nil {
		return UserPage{}, fmt.Errorf("count users: %w", err)
	}
	return UserPage{Items: users, Limit: limit, Offset: offset, Total: total}, nil
}

// GetMany returns the users with the given IDs (unknown IDs are skipped),
// so a page that mentions several people needs one request, not one each.
func (s *UserService) GetMany(ctx context.Context, ids []int64) ([]User, error) {
	if len(ids) == 0 || len(ids) > maxPageSize {
		return nil, NewError(ErrInvalid, "ids must list 1 to %d user IDs", maxPageSize)
	}
	users, err := s.store.ListUsersByIDs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("get users: %w", err)
	}
	return users, nil
}
