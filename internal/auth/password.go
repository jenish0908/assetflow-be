package auth

import (
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

// HashPassword returns a bcrypt hash of password. bcrypt is deliberately slow
// and salts every hash, which makes offline brute-forcing expensive.
func HashPassword(password string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("auth: hash password: %w", err)
	}
	return string(h), nil
}

// CheckPassword reports whether password matches hash.
func CheckPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}
