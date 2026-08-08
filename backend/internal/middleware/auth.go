package middleware

import (
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/wangkun/webshell/internal/auth"
	"github.com/wangkun/webshell/pkg/response"
)

// ctxUserID / ctxUsername / ctxIsAdmin are the context keys used to expose the
// authenticated subject to downstream handlers. Strings (not custom types) keep
// the keys simple to read when debugging c.Keys.
const (
	ctxUserID   = "userID"
	ctxUsername = "username"
	ctxIsAdmin  = "is_admin"
)

// CtxUserIDKey returns the context key under which the Auth/WSAuth middlewares
// store the authenticated user's ID. Handlers read it via c.Get(CtxUserIDKey()).
func CtxUserIDKey() string { return ctxUserID }

// CtxUsernameKey returns the context key for the authenticated username.
func CtxUsernameKey() string { return ctxUsername }

// CtxIsAdminKey returns the context key for the authenticated user's admin
// flag, sourced from the JWT's adm claim. Handlers read it via
// c.Get(CtxIsAdminKey()) and type-assert to bool.
func CtxIsAdminKey() string { return ctxIsAdmin }

// bearerPrefix is the expected Authorization header prefix (case-insensitive
// after lowercasing the whole header value).
const bearerPrefix = "bearer "

// Auth requires a valid Bearer JWT. A missing/malformed/invalid token aborts
// with ErrUnauthenticated. On success the userID, username and is_admin (from
// the token's adm claim) are stored in the request context for downstream
// handlers. is_admin is authoritative as of login time — handlers must not
// re-derive it from a store lookup.
func Auth(secret string) gin.HandlerFunc {
	return func(c *gin.Context) {
		token, ok := extractBearer(c.GetHeader("Authorization"))
		if !ok {
			response.Fail(c, response.ErrUnauthenticated, "unauthenticated")
			c.Abort()
			return
		}
		claims, err := auth.Parse(secret, token)
		if err != nil {
			response.Fail(c, response.ErrUnauthenticated, "unauthenticated")
			c.Abort()
			return
		}
		c.Set(ctxUserID, claims.UserID)
		c.Set(ctxUsername, claims.Username)
		c.Set(ctxIsAdmin, claims.IsAdmin)
		c.Next()
	}
}

// OptionalAuth parses the Bearer token when present but never aborts. If the
// token is present and valid the userID/username/is_admin are set; if it is
// missing or invalid the context is left unset and the request proceeds
// anonymously. This is for endpoints that behave differently for authenticated
// vs anonymous callers but do not require auth.
func OptionalAuth(secret string) gin.HandlerFunc {
	return func(c *gin.Context) {
		token, ok := extractBearer(c.GetHeader("Authorization"))
		if !ok {
			c.Next()
			return
		}
		if claims, err := auth.Parse(secret, token); err == nil {
			c.Set(ctxUserID, claims.UserID)
			c.Set(ctxUsername, claims.Username)
			c.Set(ctxIsAdmin, claims.IsAdmin)
		}
		c.Next()
	}
}

// WSAuth authenticates a WebSocket handshake by reading the JWT from the
// "token" query parameter. Browsers cannot set custom headers on the WS
// upgrade request, so the token is passed on the query string instead of the
// Authorization header. A missing/invalid token aborts with ErrUnauthenticated
// (the upgrade never happens). On success userID/username/is_admin are set in
// the context.
func WSAuth(secret string) gin.HandlerFunc {
	return func(c *gin.Context) {
		token := strings.TrimSpace(c.Query("token"))
		if token == "" {
			response.Fail(c, response.ErrUnauthenticated, "unauthenticated")
			c.Abort()
			return
		}
		claims, err := auth.Parse(secret, token)
		if err != nil {
			response.Fail(c, response.ErrUnauthenticated, "unauthenticated")
			c.Abort()
			return
		}
		c.Set(ctxUserID, claims.UserID)
		c.Set(ctxUsername, claims.Username)
		c.Set(ctxIsAdmin, claims.IsAdmin)
		c.Next()
	}
}

// extractBearer pulls the token out of a "Bearer <token>" Authorization header.
// The scheme match is case-insensitive. ok is false when the header is absent or
// not in Bearer form.
func extractBearer(header string) (string, bool) {
	if header == "" {
		return "", false
	}
	lower := strings.ToLower(header)
	if !strings.HasPrefix(lower, bearerPrefix) {
		return "", false
	}
	token := strings.TrimSpace(header[len(bearerPrefix):])
	if token == "" {
		return "", false
	}
	return token, true
}