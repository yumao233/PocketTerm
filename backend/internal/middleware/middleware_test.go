package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/wangkun/webshell/internal/auth"
	"github.com/wangkun/webshell/pkg/response"
)

const mwTestSecret = "mw-test-secret"

// validToken returns a signed JWT (admin) for userID/username valid for 1h.
func validToken(t *testing.T, userID, username string) string {
	t.Helper()
	return validTokenAs(t, userID, username, true)
}

// validTokenAs returns a signed JWT for userID/username with the given isAdmin,
// valid for 1h. Used to mint non-admin tokens for the admin-only negative path.
func validTokenAs(t *testing.T, userID, username string, isAdmin bool) string {
	t.Helper()
	tok, err := auth.Sign(mwTestSecret, auth.NewClaims(userID, username, isAdmin, time.Hour))
	if err != nil {
		t.Fatalf("auth.Sign: %v", err)
	}
	return tok
}

// assertEnvelope checks the recorder body is a valid envelope with the expected
// code.
func assertEnvelope(t *testing.T, w *httptest.ResponseRecorder, wantCode int) {
	t.Helper()
	var env response.Envelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode envelope: %v (body=%q)", err, w.Body.String())
	}
	if env.Code != wantCode {
		t.Fatalf("code=%d, want %d (msg=%s)", env.Code, wantCode, env.Message)
	}
}

// ---------------------------------------------------------------------------
// Auth middleware
// ---------------------------------------------------------------------------

// TestAuth_ValidBearer: a valid Bearer token lets the handler run and exposes
// userID/username in context.
func TestAuth_ValidBearer(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	var gotID, gotName string
	r.Use(Auth(mwTestSecret))
	r.GET("/x", func(c *gin.Context) {
		if v, ok := c.Get(ctxUserID); ok {
			gotID, _ = v.(string)
		}
		if v, ok := c.Get(ctxUsername); ok {
			gotName, _ = v.(string)
		}
		c.Status(200)
	})

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Authorization", "Bearer "+validToken(t, "u1", "alice"))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status=%d, want 200", w.Code)
	}
	if gotID != "u1" {
		t.Fatalf("userID in ctx=%q, want u1", gotID)
	}
	if gotName != "alice" {
		t.Fatalf("username in ctx=%q, want alice", gotName)
	}
}

// TestAuth_SetsIsAdmin: the is_admin context value mirrors the JWT's adm claim,
// for both admin and non-admin tokens. Downstream admin-only handlers rely on
// this rather than a fresh store lookup.
func TestAuth_SetsIsAdmin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	var gotAdmin *bool
	r.Use(Auth(mwTestSecret))
	r.GET("/x", func(c *gin.Context) {
		if v, ok := c.Get(ctxIsAdmin); ok {
			b, _ := v.(bool)
			gotAdmin = &b
		}
		c.Status(200)
	})

	// Admin token → is_admin == true.
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Authorization", "Bearer "+validTokenAs(t, "u1", "alice", true))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 200 || gotAdmin == nil || !*gotAdmin {
		t.Fatalf("admin token: status=%d gotAdmin=%v, want 200 + true", w.Code, gotAdmin)
	}

	// Non-admin token → is_admin == false.
	gotAdmin = nil
	req = httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Authorization", "Bearer "+validTokenAs(t, "u2", "bob", false))
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 200 || gotAdmin == nil || *gotAdmin {
		t.Fatalf("non-admin token: status=%d gotAdmin=%v, want 200 + false", w.Code, gotAdmin)
	}
}

// TestAuth_MissingHeader: no Authorization header → ErrUnauthenticated + abort.
func TestAuth_MissingHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	called := false
	r.Use(Auth(mwTestSecret))
	r.GET("/x", func(c *gin.Context) { called = true })

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if called {
		t.Fatalf("handler ran despite missing auth")
	}
	assertEnvelope(t, w, response.ErrUnauthenticated)
}

// TestAuth_GarbageToken: a non-Bearer header is rejected.
func TestAuth_GarbageToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	called := false
	r.Use(Auth(mwTestSecret))
	r.GET("/x", func(c *gin.Context) { called = true })

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Authorization", "Bogus xyz")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if called {
		t.Fatalf("handler ran despite bogus auth")
	}
	assertEnvelope(t, w, response.ErrUnauthenticated)
}

// TestAuth_TamperedToken: a token signed with the wrong secret is rejected.
func TestAuth_TamperedToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	called := false
	r.Use(Auth(mwTestSecret))
	r.GET("/x", func(c *gin.Context) { called = true })

	wrong, _ := auth.Sign("WRONG-SECRET", auth.NewClaims("u1", "alice", true, time.Hour))
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Authorization", "Bearer "+wrong)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if called {
		t.Fatalf("handler ran despite wrong-secret token")
	}
	assertEnvelope(t, w, response.ErrUnauthenticated)
}

// TestAuth_ExpiredToken: an already-expired token is rejected.
func TestAuth_ExpiredToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	called := false
	r.Use(Auth(mwTestSecret))
	r.GET("/x", func(c *gin.Context) { called = true })

	// ttl <= 0 yields an already-expired token (per auth.NewClaims contract).
	expired, _ := auth.Sign(mwTestSecret, auth.NewClaims("u1", "alice", true, -time.Minute))
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Authorization", "Bearer "+expired)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if called {
		t.Fatalf("handler ran despite expired token")
	}
	assertEnvelope(t, w, response.ErrUnauthenticated)
}

// TestAuth_CaseInsensitiveScheme: "bearer" (lowercase) parses the token.
func TestAuth_CaseInsensitiveScheme(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	called := false
	r.Use(Auth(mwTestSecret))
	r.GET("/x", func(c *gin.Context) { called = true })

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Authorization", "bearer "+validToken(t, "u1", "alice"))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if !called {
		t.Fatalf("handler did not run for lowercase bearer")
	}
	if w.Code != 200 {
		t.Fatalf("status=%d, want 200", w.Code)
	}
}

// ---------------------------------------------------------------------------
// OptionalAuth
// ---------------------------------------------------------------------------

// TestOptionalAuth_NoHeader: missing header proceeds without setting context.
func TestOptionalAuth_NoHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	called := false
	var hasID bool
	r.Use(OptionalAuth(mwTestSecret))
	r.GET("/x", func(c *gin.Context) {
		called = true
		_, hasID = c.Get(ctxUserID)
		c.Status(200)
	})

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if !called {
		t.Fatalf("handler did not run under OptionalAuth")
	}
	if hasID {
		t.Fatalf("userID set in ctx despite no token")
	}
}

// TestOptionalAuth_ValidToken: a valid token sets context (otherwise no).
func TestOptionalAuth_ValidToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	var gotID string
	r.Use(OptionalAuth(mwTestSecret))
	r.GET("/x", func(c *gin.Context) {
		if v, ok := c.Get(ctxUserID); ok {
			gotID, _ = v.(string)
		}
		c.Status(200)
	})

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Authorization", "Bearer "+validToken(t, "u9", "bob"))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if gotID != "u9" {
		t.Fatalf("userID=%q, want u9", gotID)
	}
}

// TestOptionalAuth_InvalidToken: an invalid token is ignored (no abort, no ctx).
func TestOptionalAuth_InvalidToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	called := false
	var hasID bool
	r.Use(OptionalAuth(mwTestSecret))
	r.GET("/x", func(c *gin.Context) {
		called = true
		_, hasID = c.Get(ctxUserID)
		c.Status(200)
	})

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Authorization", "Bearer garbage")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if !called {
		t.Fatalf("handler did not run under OptionalAuth with bad token")
	}
	if hasID {
		t.Fatalf("userID set despite invalid token")
	}
}

// ---------------------------------------------------------------------------
// WSAuth
// ---------------------------------------------------------------------------

// TestWSAuth_ValidToken: a valid query token sets context and lets the handler
// run (the WS upgrade would follow in production).
func TestWSAuth_ValidToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	var gotID string
	r.Use(WSAuth(mwTestSecret))
	r.GET("/ws/:id", func(c *gin.Context) {
		if v, ok := c.Get(ctxUserID); ok {
			gotID, _ = v.(string)
		}
		c.Status(200)
	})

	req := httptest.NewRequest(http.MethodGet, "/ws/s1?token="+validToken(t, "u1", "alice"), nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status=%d, want 200", w.Code)
	}
	if gotID != "u1" {
		t.Fatalf("userID=%q, want u1", gotID)
	}
}

// TestWSAuth_MissingToken: no token query → abort.
func TestWSAuth_MissingToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	called := false
	r.Use(WSAuth(mwTestSecret))
	r.GET("/ws/:id", func(c *gin.Context) { called = true })

	req := httptest.NewRequest(http.MethodGet, "/ws/s1", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if called {
		t.Fatalf("handler ran despite missing ws token")
	}
	assertEnvelope(t, w, response.ErrUnauthenticated)
}

// TestWSAuth_InvalidToken: garbage token → abort.
func TestWSAuth_InvalidToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	called := false
	r.Use(WSAuth(mwTestSecret))
	r.GET("/ws/:id", func(c *gin.Context) { called = true })

	req := httptest.NewRequest(http.MethodGet, "/ws/s1?token=garbage", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if called {
		t.Fatalf("handler ran despite garbage ws token")
	}
	assertEnvelope(t, w, response.ErrUnauthenticated)
}

// ---------------------------------------------------------------------------
// Logger (smoke test: just ensure it does not break the chain)
// ---------------------------------------------------------------------------

// TestLogger_NoBreak: the Logger middleware passes through and records a line.
func TestLogger_NoBreak(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(Logger())
	r.GET("/x", func(c *gin.Context) { c.Status(204) })

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 204 {
		t.Fatalf("status=%d, want 204", w.Code)
	}
}