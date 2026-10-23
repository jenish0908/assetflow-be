package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jenish0908/assetflow-be/internal/auth"
)

type fakeTokens struct{}

func (fakeTokens) Issue(userID int64, role auth.Role) (string, error) {
	return "token-for-" + string(role), nil
}

func TestUserCreateValidation(t *testing.T) {
	valid := CreateUserInput{Email: "Priya@Example.com ", Name: "Priya", Password: "correct-horse", Role: auth.RoleViewer}

	tests := []struct {
		name     string
		mutate   func(*CreateUserInput)
		wantKind error
	}{
		{name: "ok", mutate: func(*CreateUserInput) {}},
		{name: "bad email", mutate: func(in *CreateUserInput) { in.Email = "priya" }, wantKind: ErrInvalid},
		{name: "display-name email", mutate: func(in *CreateUserInput) { in.Email = "Priya <p@x.com>" }, wantKind: ErrInvalid},
		{name: "missing name", mutate: func(in *CreateUserInput) { in.Name = "" }, wantKind: ErrInvalid},
		{name: "short password", mutate: func(in *CreateUserInput) { in.Password = "short" }, wantKind: ErrInvalid},
		{name: "unknown role", mutate: func(in *CreateUserInput) { in.Role = "root" }, wantKind: ErrInvalid},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := valid
			tt.mutate(&in)
			store := &mockStore{
				createUser: func(_ context.Context, u NewUser) (User, error) {
					assert.Equal(t, "priya@example.com", u.Email, "email should be normalised")
					assert.True(t, auth.CheckPassword(u.PasswordHash, "correct-horse"), "password should be hashed")
					return User{ID: 1, Email: u.Email, Role: u.Role}, nil
				},
			}

			_, err := NewUserService(store, fakeTokens{}).Create(context.Background(), in)
			if tt.wantKind != nil {
				require.ErrorIs(t, err, tt.wantKind)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestUserLogin(t *testing.T) {
	hash, err := auth.HashPassword("correct-horse")
	require.NoError(t, err)
	stored := UserCredentials{User: User{ID: 9, Role: auth.RoleManager}, PasswordHash: hash}

	tests := []struct {
		name      string
		password  string
		storeErr  error
		wantToken string
		wantKind  error
	}{
		{name: "ok", password: "correct-horse", wantToken: "token-for-manager"},
		{name: "wrong password", password: "wrong", wantKind: ErrUnauthenticated},
		{name: "unknown email", password: "correct-horse", storeErr: ErrNotFound, wantKind: ErrUnauthenticated},
		{name: "db error is not a login failure", password: "correct-horse", storeErr: errors.New("db down")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &mockStore{
				getUserCredentials: func(_ context.Context, email string) (UserCredentials, error) {
					assert.Equal(t, "priya@example.com", email)
					return stored, tt.storeErr
				},
			}

			token, err := NewUserService(store, fakeTokens{}).Login(context.Background(), " PRIYA@example.com", tt.password)
			switch {
			case tt.wantKind != nil:
				require.ErrorIs(t, err, tt.wantKind)
				assert.Equal(t, "invalid email or password", err.Error(), "must not reveal which part was wrong")
			case tt.storeErr != nil:
				require.Error(t, err)
				assert.NotErrorIs(t, err, ErrUnauthenticated)
			default:
				require.NoError(t, err)
				assert.Equal(t, tt.wantToken, token)
			}
		})
	}
}
