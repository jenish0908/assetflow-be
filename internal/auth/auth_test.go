package auth

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRoleAtLeast(t *testing.T) {
	tests := []struct {
		role     Role
		required Role
		want     bool
	}{
		{RoleAdmin, RoleViewer, true},
		{RoleAdmin, RoleAdmin, true},
		{RoleManager, RoleViewer, true},
		{RoleManager, RoleAdmin, false},
		{RoleViewer, RoleManager, false},
		{Role("root"), RoleViewer, false},
	}
	for _, tt := range tests {
		t.Run(string(tt.role)+">="+string(tt.required), func(t *testing.T) {
			assert.Equal(t, tt.want, tt.role.AtLeast(tt.required))
		})
	}
}

func TestTokenRoundTrip(t *testing.T) {
	m := NewTokenManager([]byte("test-secret-test-secret-test-secret"), time.Hour)

	tok, err := m.Issue(42, RoleManager)
	require.NoError(t, err)

	got, err := m.Parse(tok)
	require.NoError(t, err)
	assert.Equal(t, Claims{UserID: 42, Role: RoleManager}, got)
}

func TestTokenRejected(t *testing.T) {
	secret := []byte("test-secret-test-secret-test-secret")
	issuedAt := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	issuer := NewTokenManager(secret, time.Hour)
	issuer.now = func() time.Time { return issuedAt }
	valid, err := issuer.Issue(1, RoleAdmin)
	require.NoError(t, err)

	unsigned, err := jwt.NewWithClaims(jwt.SigningMethodNone, jwtClaims{
		Role:             RoleAdmin,
		RegisteredClaims: jwt.RegisteredClaims{Subject: "1", ExpiresAt: jwt.NewNumericDate(issuedAt.Add(time.Hour))},
	}).SignedString(jwt.UnsafeAllowNoneSignatureType)
	require.NoError(t, err)

	tests := []struct {
		name   string
		token  string
		secret []byte
		now    time.Time
	}{
		{"expired", valid, secret, issuedAt.Add(2 * time.Hour)},
		{"wrong secret", valid, []byte("another-secret-another-secret-xx"), issuedAt},
		{"tampered", valid + "x", secret, issuedAt},
		{"alg none", unsigned, secret, issuedAt},
		{"garbage", "not-a-jwt", secret, issuedAt},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewTokenManager(tt.secret, time.Hour)
			m.now = func() time.Time { return tt.now }

			_, err := m.Parse(tt.token)
			require.ErrorIs(t, err, ErrInvalidToken)
		})
	}
}
