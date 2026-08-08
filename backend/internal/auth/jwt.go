package auth

import (
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Claims is the custom JWT claims set. The UserID (uid), Username (usr) and
// IsAdmin (adm) fields carry the authenticated subject; IsAdmin is populated
// from user.IsAdmin at sign time and read by the Auth middleware so admin-only
// endpoints (e.g. SSO token generation) can authorize without a fresh store
// lookup (the value is fresh as of login, never goes stale mid-session). The
// embedded RegisteredClaims provide standard fields such as ExpiresAt, IssuedAt
// and Subject.
type Claims struct {
	UserID   string `json:"uid"`
	Username string `json:"usr"`
	IsAdmin  bool   `json:"adm"`
	jwt.RegisteredClaims
}

// NewClaims builds a Claims value for the given user with standard timestamps
// populated: ExpiresAt is now+ttl, IssuedAt is now, and Subject is the username.
// isAdmin is captured from the user record so downstream middleware can
// authorize admin-only endpoints from the token alone. A ttl <= 0 yields an
// already-expired token, which is useful for tests.
func NewClaims(userID, username string, isAdmin bool, ttl time.Duration) Claims {
	now := time.Now()
	return Claims{
		UserID:   userID,
		Username: username,
		IsAdmin:  isAdmin,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
			IssuedAt:  jwt.NewNumericDate(now),
			Subject:   username,
		},
	}
}

// Sign produces a signed HS256 JWT string for the given claims using secret.
// The secret is used verbatim as the HMAC key.
func Sign(secret string, c Claims) (string, error) {
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, c)
	signed, err := token.SignedString([]byte(secret))
	if err != nil {
		return "", fmt.Errorf("auth: sign token: %w", err)
	}
	return signed, nil
}

// Parse verifies the signature and expiry of tokenStr using secret and returns
// the populated Claims on success. The signature must be HS256-compatible (the
// key returned by keyFunc is the raw secret bytes) and the exp claim, when
// present, must not be in the past. Any verification error (wrong secret,
// malformed token, expired) yields a nil Claims and a non-nil error.
func Parse(secret, tokenStr string) (*Claims, error) {
	claims := &Claims{}
	token, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (any, error) {
		// Ensure the token uses an HMAC signing method; a token signed with a
		// different algorithm (e.g. RS256, "none") must not be accepted even if
		// the key bytes happen to verify.
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("auth: unexpected signing method %v", t.Header["alg"])
		}
		return []byte(secret), nil
	})
	if err != nil {
		return nil, fmt.Errorf("auth: parse token: %w", err)
	}
	if !token.Valid {
		return nil, fmt.Errorf("auth: invalid token")
	}
	return claims, nil
}