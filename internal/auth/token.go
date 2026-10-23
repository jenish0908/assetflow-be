package auth

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// ErrInvalidToken is returned for any token that fails verification.
var ErrInvalidToken = errors.New("auth: invalid token")

// Claims is the verified identity carried by an access token.
type Claims struct {
	UserID int64
	Role   Role
}

type jwtClaims struct {
	Role Role `json:"role"`
	jwt.RegisteredClaims
}

// TokenManager issues and verifies HS256-signed JWTs.
type TokenManager struct {
	secret []byte
	ttl    time.Duration
	now    func() time.Time
}

func NewTokenManager(secret []byte, ttl time.Duration) *TokenManager {
	return &TokenManager{secret: secret, ttl: ttl, now: time.Now}
}

// Issue returns a signed token for the given user that expires after the TTL.
func (m *TokenManager) Issue(userID int64, role Role) (string, error) {
	now := m.now()
	claims := jwtClaims{
		Role: role,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   strconv.FormatInt(userID, 10),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(m.ttl)),
		},
	}
	s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(m.secret)
	if err != nil {
		return "", fmt.Errorf("auth: sign token: %w", err)
	}
	return s, nil
}

// Parse verifies the signature and expiry of token and returns its claims.
func (m *TokenManager) Parse(token string) (Claims, error) {
	var c jwtClaims
	_, err := jwt.ParseWithClaims(token, &c,
		func(*jwt.Token) (any, error) { return m.secret, nil },
		// Pinning the algorithm stops "alg: none" and algorithm-confusion attacks.
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithExpirationRequired(),
		jwt.WithTimeFunc(m.now),
	)
	if err != nil {
		return Claims{}, fmt.Errorf("%w: %w", ErrInvalidToken, err)
	}

	id, err := strconv.ParseInt(c.Subject, 10, 64)
	if err != nil || !c.Role.Valid() {
		return Claims{}, ErrInvalidToken
	}
	return Claims{UserID: id, Role: c.Role}, nil
}
