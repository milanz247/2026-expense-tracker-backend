// Package auth issues and verifies the JWTs used to authenticate API
// requests after login.
package auth

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// ErrInvalidToken is returned by ParseToken for any token that is
// malformed, expired, or signed with an unexpected method/key.
var ErrInvalidToken = errors.New("auth: invalid or expired token")

// Claims are the custom JWT claims embedded alongside the standard
// registered claims (exp, iat, sub). Name and Currency are duplicated
// from the User record so the frontend can render basic account info
// without an extra API round trip.
type Claims struct {
	UserID   uint   `json:"user_id"`
	Name     string `json:"name"`
	Email    string `json:"email"`
	Currency string `json:"currency"`
	jwt.RegisteredClaims
}

// TokenManager generates and validates JWTs using a single HMAC secret.
// It is injected into handlers rather than used via package-level state.
type TokenManager struct {
	secret []byte
	ttl    time.Duration
}

// NewTokenManager builds a TokenManager. ttl controls how long issued
// tokens remain valid.
func NewTokenManager(secret string, ttl time.Duration) *TokenManager {
	return &TokenManager{secret: []byte(secret), ttl: ttl}
}

// GenerateToken creates a signed JWT for the given user.
func (m *TokenManager) GenerateToken(userID uint, name, email, currency string) (string, error) {
	now := time.Now()
	claims := Claims{
		UserID:   userID,
		Name:     name,
		Email:    email,
		Currency: currency,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   strconv.FormatUint(uint64(userID), 10),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(m.ttl)),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)

	signed, err := token.SignedString(m.secret)
	if err != nil {
		return "", fmt.Errorf("auth: failed to sign token: %w", err)
	}

	return signed, nil
}

// ParseToken validates a signed JWT and returns its claims. It rejects
// tokens signed with anything other than HMAC to guard against algorithm
// confusion attacks.
func (m *TokenManager) ParseToken(tokenString string) (*Claims, error) {
	claims := &Claims{}

	token, err := jwt.ParseWithClaims(tokenString, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, ErrInvalidToken
		}
		return m.secret, nil
	})
	if err != nil || !token.Valid {
		return nil, ErrInvalidToken
	}

	return claims, nil
}
