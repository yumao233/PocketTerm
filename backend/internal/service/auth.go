// Package service implements the business-logic layer that sits between the
// HTTP handlers and the lower-level stores / pty.Manager. Each service method
// returns the result, a business error code (a response.ErrXxx constant, 0 on
// success), and a Go error; handlers map the code to the JSON envelope.
//
// The services are intentionally thin: they orchestrate stores + auth + pty and
// enforce invariants (uniqueness, ownership, sanitization of secrets) while
// leaving persistence mechanics to the model layer.
package service

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/wangkun/webshell/internal/auth"
	"github.com/wangkun/webshell/internal/model"
	"github.com/wangkun/webshell/pkg/response"
)

// ssoTokenTTL is how long a generated SSO token remains valid. Five minutes is
// long enough for an external system to pick the token up and present it, and
// short enough to limit the replay window.
const ssoTokenTTL = 5 * time.Minute

// minPasswordLen is the minimum acceptable length for a setup password.
const minPasswordLen = 6

// AuthService handles user setup, login and single-sign-on token flow. It owns
// the JWT secret and TTL and is the only place that signs tokens.
type AuthService struct {
	users  model.UserStore
	tokens model.SsoTokenStore
	jwtSec string
	jwtTTL time.Duration
}

// NewAuthService builds an AuthService backed by the given user and SSO-token
// stores. jwtSecret is used as the HMAC key for token signing/parsing; jwtTTL
// is the token lifetime.
func NewAuthService(users model.UserStore, tokens model.SsoTokenStore, jwtSecret string, jwtTTL time.Duration) *AuthService {
	return &AuthService{
		users:  users,
		tokens: tokens,
		jwtSec: jwtSecret,
		jwtTTL: jwtTTL,
	}
}

// CheckSetup reports whether the system has been initialized. It returns true
// when no admin user exists yet (user count == 0), guiding the frontend to show
// the setup page.
func (s *AuthService) CheckSetup() (bool, error) {
	count, err := s.users.Count()
	if err != nil {
		return false, fmt.Errorf("service: count users: %w", err)
	}
	return count == 0, nil
}

// Setup registers the FIRST admin user. It fails with ErrAlreadyInitialized if
// an admin already exists, ErrInvalidParam if the username is empty or the
// password is shorter than minPasswordLen. On success it returns the new user
// (PasswordHash zeroed) and a fresh JWT.
func (s *AuthService) Setup(username, password string) (*model.User, string, int, error) {
	count, err := s.users.Count()
	if err != nil {
		return nil, "", response.ErrInternal, fmt.Errorf("service: count users: %w", err)
	}
	if count > 0 {
		return nil, "", response.ErrAlreadyInitialized, errors.New("service: system already initialized")
	}
	if username == "" || len(password) < minPasswordLen {
		return nil, "", response.ErrInvalidParam, errors.New("service: invalid username or password")
	}

	hash, err := auth.HashPassword(password)
	if err != nil {
		return nil, "", response.ErrInternal, fmt.Errorf("service: hash password: %w", err)
	}
	user := &model.User{
		ID:           uuid.NewString(),
		Username:     username,
		PasswordHash: hash,
		IsAdmin:      true,
		CreatedAt:    time.Now(),
	}
	if err := s.users.Create(user); err != nil {
		if errors.Is(err, model.ErrDuplicate) {
			return nil, "", response.ErrInvalidParam, err
		}
		return nil, "", response.ErrInternal, fmt.Errorf("service: create user: %w", err)
	}

	token, err := s.signToken(user)
	if err != nil {
		return nil, "", response.ErrInternal, err
	}
	return sanitizeUser(user), token, response.ErrOK, nil
}

// Login validates username + password and returns the user (sanitized) plus a
// fresh JWT. Empty store, missing user or wrong password all map to
// ErrInvalidCredentials so the login endpoint does not leak which sub-case
// failed.
func (s *AuthService) Login(username, password string) (*model.User, string, int, error) {
	count, err := s.users.Count()
	if err != nil {
		return nil, "", response.ErrInternal, fmt.Errorf("service: count users: %w", err)
	}
	if count == 0 {
		return nil, "", response.ErrNotInitialized, errors.New("service: system not initialized")
	}

	user, err := s.users.GetByUsername(username)
	if err != nil {
		if errors.Is(err, model.ErrNotFound) {
			return nil, "", response.ErrInvalidCredentials, errors.New("service: invalid credentials")
		}
		return nil, "", response.ErrInternal, fmt.Errorf("service: get user: %w", err)
	}
	if !auth.CheckPassword(user.PasswordHash, password) {
		return nil, "", response.ErrInvalidCredentials, errors.New("service: invalid credentials")
	}

	token, err := s.signToken(user)
	if err != nil {
		return nil, "", response.ErrInternal, err
	}
	return sanitizeUser(user), token, response.ErrOK, nil
}

// SsoLogin exchanges a one-time SSO token (uuid + password) for a JWT bound to
// the token's username. The token is consumed atomically via Consume, which
// checks existence, the Used flag and the Expiry timer and marks the token
// used all under a single write lock — so two concurrent SsoLogin calls with
// the same uuid can never both succeed (TOCTOU-safe). Any failure (token not
// found / already used / expired / wrong password / bound user missing) maps
// to ErrInvalidCredentials to avoid leaking which sub-case failed.
//
// The password is checked AFTER Consume succeeds. This means a wrong password
// burns the token: this is the desired security posture (one-time tokens must
// not be brute-forced) and the caller cannot distinguish "bad password" from
// "expired/used" because both return ErrInvalidCredentials.
func (s *AuthService) SsoLogin(uuidStr, password string) (*model.User, string, int, error) {
	tok, ok := s.tokens.Consume(uuidStr)
	if !ok {
		// not found / already used / expired / persist failure — all map to the
		// same code so the caller cannot enumerate the reason.
		return nil, "", response.ErrInvalidCredentials, errors.New("service: sso token invalid")
	}
	if tok.Password != password {
		// The token is already consumed (one-time); a wrong password cannot be
		// retried. Surface the same generic code to avoid enumeration.
		return nil, "", response.ErrInvalidCredentials, errors.New("service: sso token invalid")
	}

	user, err := s.users.GetByUsername(tok.Username)
	if err != nil {
		if errors.Is(err, model.ErrNotFound) {
			return nil, "", response.ErrInvalidCredentials, errors.New("service: sso token invalid")
		}
		return nil, "", response.ErrInternal, fmt.Errorf("service: get sso user: %w", err)
	}

	token, err := s.signToken(user)
	if err != nil {
		return nil, "", response.ErrInternal, err
	}
	return sanitizeUser(user), token, response.ErrOK, nil
}

// GenSsoToken mints a one-time SSO token bound to username. The named user must
// exist (ErrNotFound otherwise). The token's plaintext password is a random
// 16-byte hex string, returned ONCE in the SsoToken.Password field. The caller
// (an authenticated admin endpoint) is responsible for delivering it securely
// to the external system.
func (s *AuthService) GenSsoToken(username string) (*model.SsoToken, int, error) {
	if _, err := s.users.GetByUsername(username); err != nil {
		if errors.Is(err, model.ErrNotFound) {
			return nil, response.ErrNotFound, errors.New("service: user not found")
		}
		return nil, response.ErrInternal, fmt.Errorf("service: get user for sso: %w", err)
	}

	pw, err := randomHex(16)
	if err != nil {
		return nil, response.ErrInternal, fmt.Errorf("service: gen sso password: %w", err)
	}
	now := time.Now()
	tok := &model.SsoToken{
		ID:        uuid.NewString(),
		Username:  username,
		Password:  pw,
		Used:      false,
		CreatedAt: now,
		ExpiresAt: now.Add(ssoTokenTTL),
	}
	if err := s.tokens.Create(tok); err != nil {
		return nil, response.ErrInternal, fmt.Errorf("service: create sso token: %w", err)
	}
	return tok, response.ErrOK, nil
}

// Me returns the sanitized user for the given userID. It is the backing lookup
// for GET /me, which already has the authenticated userID in context. A missing
// user yields ErrNotFound.
func (s *AuthService) Me(userID string) (*model.User, int, error) {
	if userID == "" {
		return nil, response.ErrInvalidParam, errors.New("service: user id required")
	}
	user, err := s.users.GetByID(userID)
	if err != nil {
		if errors.Is(err, model.ErrNotFound) {
			return nil, response.ErrNotFound, errors.New("service: user not found")
		}
		return nil, response.ErrInternal, fmt.Errorf("service: get user: %w", err)
	}
	return sanitizeUser(user), response.ErrOK, nil
}

// signToken signs a JWT for user using the service's secret and TTL. The user's
// IsAdmin flag is captured in the claims so downstream middleware can authorize
// admin-only endpoints from the token alone (no per-request store lookup).
func (s *AuthService) signToken(user *model.User) (string, error) {
	tok, err := auth.Sign(s.jwtSec, auth.NewClaims(user.ID, user.Username, user.IsAdmin, s.jwtTTL))
	if err != nil {
		return "", fmt.Errorf("service: sign jwt: %w", err)
	}
	return tok, nil
}

// sanitizeUser returns a copy of u with PasswordHash cleared so the bcrypt
// hash is never serialized into an API response. The original u is left
// untouched.
func sanitizeUser(u *model.User) *model.User {
	if u == nil {
		return nil
	}
	cp := *u
	cp.PasswordHash = ""
	return &cp
}

// randomHex returns n bytes of crypto/rand randomness as a 2*n-char hex string.
func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}