package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/wangkun/webshell/internal/auth"
	"github.com/wangkun/webshell/internal/middleware"
	"github.com/wangkun/webshell/internal/model"
	"github.com/wangkun/webshell/internal/service"
	"github.com/wangkun/webshell/pkg/response"
)

// ---------------------------------------------------------------------------
// Test fixtures: real temp-dir stores wired through the real services and
// handlers mounted on a gin engine. No SSH/PTY needed for auth/sso/template
// paths, so the PtyService is not constructed here.
// ---------------------------------------------------------------------------

const (
	testSecret = "handler-test-secret"
	testTTL    = time.Hour
)

// testEnv builds an engine with auth/template/sso handlers wired to fresh
// services, plus the Auth middleware on protected routes mirroring the real
// router layout. It returns the engine and the underlying services for direct
// seeding.
type testEnv struct {
	engine  *gin.Engine
	authSvc *service.AuthService
	tplSvc  *service.TemplateService
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	dir := t.TempDir()

	users, err := model.NewUserStore(dir)
	if err != nil {
		t.Fatalf("NewUserStore: %v", err)
	}
	tokens, err := model.NewSsoTokenStore(dir)
	if err != nil {
		t.Fatalf("NewSsoTokenStore: %v", err)
	}
	tplStore, err := model.NewTemplateStore(dir)
	if err != nil {
		t.Fatalf("NewTemplateStore: %v", err)
	}

	authSvc := service.NewAuthService(users, tokens, testSecret, testTTL)
	tplSvc := service.NewTemplateService(tplStore)

	authH := NewAuthHandler(authSvc)
	ssoH := NewSsoHandler(authSvc)
	tplH := NewTemplateHandler(tplSvc)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware.Recovery())

	v1 := r.Group("/api/v1")
	{
		v1.POST("/setup", authH.Setup)
		v1.POST("/login", authH.Login)
		v1.GET("/check-setup", authH.CheckSetup)
		v1.POST("/sso_login", ssoH.SsoLogin)

		// Protected: require auth.
		authed := v1.Group("")
		authed.Use(middleware.Auth(testSecret))
		{
			authed.POST("/logout", authH.Logout)
			authed.GET("/me", authH.Me)
			authed.POST("/sso/tokens", ssoH.CreateToken)

			authed.GET("/templates", tplH.List)
			authed.POST("/templates", tplH.Create)
			authed.GET("/templates/:id", tplH.Get)
			authed.PUT("/templates/:id", tplH.Update)
			authed.DELETE("/templates/:id", tplH.Delete)
		}
	}

	return &testEnv{engine: r, authSvc: authSvc, tplSvc: tplSvc}
}

// doJSON issues a request with an optional JSON body and returns the response
// recorder and the decoded envelope.
func doJSON(t *testing.T, r *gin.Engine, method, path, token string, body any) (*httptest.ResponseRecorder, response.Envelope) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		enc := json.NewEncoder(&buf)
		if err := enc.Encode(body); err != nil {
			t.Fatalf("encode body: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	var env response.Envelope
	if w.Body.Len() > 0 {
		if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
			t.Fatalf("decode envelope: %v (body=%q)", err, w.Body.String())
		}
	}
	return w, env
}

// doRaw issues a non-JSON request (used for GET/DELETE with no body).
func doRaw(t *testing.T, r *gin.Engine, method, path, token string) (*httptest.ResponseRecorder, response.Envelope) {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var env response.Envelope
	if w.Body.Len() > 0 {
		_ = json.Unmarshal(w.Body.Bytes(), &env)
	}
	return w, env
}

// setupAdmin creates the first admin via the Setup handler and returns the JWT.
func setupAdmin(t *testing.T, env *testEnv) string {
	t.Helper()
	_, e := doJSON(t, env.engine, http.MethodPost, "/api/v1/setup", "", gin.H{
		"username": "admin",
		"password": "secret123",
	})
	if e.Code != response.ErrOK {
		t.Fatalf("Setup: code=%d msg=%s", e.Code, e.Message)
	}
	var ar authResp
	raw, _ := json.Marshal(e.Data)
	if err := json.Unmarshal(raw, &ar); err != nil {
		t.Fatalf("decode authResp: %v", err)
	}
	if ar.Token == "" {
		t.Fatalf("Setup returned empty token")
	}
	return ar.Token
}

// ---------------------------------------------------------------------------
// AuthHandler
// ---------------------------------------------------------------------------

// TestCheckSetup_NotInitialized: a fresh system reports initialized=false.
func TestCheckSetup_NotInitialized(t *testing.T) {
	env := newTestEnv(t)
	_, e := doRaw(t, env.engine, http.MethodGet, "/api/v1/check-setup", "")
	if e.Code != response.ErrOK {
		t.Fatalf("check-setup code=%d msg=%s", e.Code, e.Message)
	}
	var csr checkSetupResp
	raw, _ := json.Marshal(e.Data)
	if err := json.Unmarshal(raw, &csr); err != nil {
		t.Fatalf("decode checkSetupResp: %v", err)
	}
	if csr.Initialized {
		t.Fatalf("initialized=true on empty store; want false")
	}
}

// TestSetup_ThenCheckSetup: after setup the probe flips to true.
func TestSetup_ThenCheckSetup(t *testing.T) {
	env := newTestEnv(t)
	setupAdmin(t, env)
	_, e := doRaw(t, env.engine, http.MethodGet, "/api/v1/check-setup", "")
	var csr checkSetupResp
	raw, _ := json.Marshal(e.Data)
	if err := json.Unmarshal(raw, &csr); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !csr.Initialized {
		t.Fatalf("initialized=false after setup; want true")
	}
}

// TestSetup_AlreadyInitialized: a second setup returns ErrAlreadyInitialized.
func TestSetup_AlreadyInitialized(t *testing.T) {
	env := newTestEnv(t)
	setupAdmin(t, env)
	_, e := doJSON(t, env.engine, http.MethodPost, "/api/v1/setup", "", gin.H{
		"username": "other",
		"password": "secret456",
	})
	if e.Code != response.ErrAlreadyInitialized {
		t.Fatalf("second setup code=%d, want %d", e.Code, response.ErrAlreadyInitialized)
	}
}

// TestSetup_SanitizesUser: the returned user must not leak the password hash.
// The password_hash field may be present (omitempty on the model keeps it out
// when empty, but we assert on the value being empty — the hash itself, not the
// field name, is the leak we guard against).
func TestSetup_SanitizesUser(t *testing.T) {
	env := newTestEnv(t)
	_, e := doJSON(t, env.engine, http.MethodPost, "/api/v1/setup", "", gin.H{
		"username": "admin",
		"password": "secret123",
	})
	raw, _ := json.Marshal(e.Data)
	var ar authResp
	if err := json.Unmarshal(raw, &ar); err != nil {
		t.Fatalf("decode: %v", err)
	}
	uBytes, _ := json.Marshal(ar.User)
	var u model.User
	if err := json.Unmarshal(uBytes, &u); err != nil {
		t.Fatalf("decode user: %v", err)
	}
	if u.PasswordHash != "" {
		t.Fatalf("setup response leaks password hash: %q", u.PasswordHash)
	}
}

// TestLogin_Success: correct credentials yield a parseable JWT.
func TestLogin_Success(t *testing.T) {
	env := newTestEnv(t)
	setupAdmin(t, env)
	_, e := doJSON(t, env.engine, http.MethodPost, "/api/v1/login", "", gin.H{
		"username": "admin",
		"password": "secret123",
	})
	if e.Code != response.ErrOK {
		t.Fatalf("login code=%d msg=%s", e.Code, e.Message)
	}
	var ar authResp
	raw, _ := json.Marshal(e.Data)
	if err := json.Unmarshal(raw, &ar); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if ar.Token == "" {
		t.Fatalf("login returned empty token")
	}
}

// TestLogin_WrongPassword: a wrong password returns ErrInvalidCredentials.
func TestLogin_WrongPassword(t *testing.T) {
	env := newTestEnv(t)
	setupAdmin(t, env)
	_, e := doJSON(t, env.engine, http.MethodPost, "/api/v1/login", "", gin.H{
		"username": "admin",
		"password": "WRONG",
	})
	if e.Code != response.ErrInvalidCredentials {
		t.Fatalf("login wrong pw code=%d, want %d", e.Code, response.ErrInvalidCredentials)
	}
}

// TestLogin_NotInitialized: login before setup returns ErrNotInitialized.
func TestLogin_NotInitialized(t *testing.T) {
	env := newTestEnv(t)
	_, e := doJSON(t, env.engine, http.MethodPost, "/api/v1/login", "", gin.H{
		"username": "admin",
		"password": "secret123",
	})
	if e.Code != response.ErrNotInitialized {
		t.Fatalf("login before setup code=%d, want %d", e.Code, response.ErrNotInitialized)
	}
}

// TestLogout: stateless logout returns ok.
func TestLogout(t *testing.T) {
	env := newTestEnv(t)
	token := setupAdmin(t, env)
	_, e := doJSON(t, env.engine, http.MethodPost, "/api/v1/logout", token, nil)
	if e.Code != response.ErrOK {
		t.Fatalf("logout code=%d msg=%s", e.Code, e.Message)
	}
}

// TestMe_WithToken: an authenticated /me returns the sanitized user.
func TestMe_WithToken(t *testing.T) {
	env := newTestEnv(t)
	token := setupAdmin(t, env)
	_, e := doRaw(t, env.engine, http.MethodGet, "/api/v1/me", token)
	if e.Code != response.ErrOK {
		t.Fatalf("me code=%d msg=%s", e.Code, e.Message)
	}
	raw, _ := json.Marshal(e.Data)
	var mr struct {
		User model.User `json:"user"`
	}
	if err := json.Unmarshal(raw, &mr); err != nil {
		t.Fatalf("decode me: %v (raw=%s)", err, raw)
	}
	if mr.User.Username != "admin" {
		t.Fatalf("me username=%q, want admin", mr.User.Username)
	}
	if mr.User.PasswordHash != "" {
		t.Fatalf("me leaks PasswordHash")
	}
}

// TestMe_WithoutToken: /me without a token is rejected with ErrUnauthenticated
// and the handler never runs (no user lookup).
func TestMe_WithoutToken(t *testing.T) {
	env := newTestEnv(t)
	_, e := doRaw(t, env.engine, http.MethodGet, "/api/v1/me", "")
	if e.Code != response.ErrUnauthenticated {
		t.Fatalf("me without token code=%d, want %d", e.Code, response.ErrUnauthenticated)
	}
}

// TestMe_InvalidToken: a malformed token is rejected identically.
func TestMe_InvalidToken(t *testing.T) {
	env := newTestEnv(t)
	_, e := doRaw(t, env.engine, http.MethodGet, "/api/v1/me", "garbage-token")
	if e.Code != response.ErrUnauthenticated {
		t.Fatalf("me garbage token code=%d, want %d", e.Code, response.ErrUnauthenticated)
	}
}

// TestProtectedRoute_NoAuth ensures the Auth middleware aborts before the
// handler for various protected routes (template CRUD used as a stand-in).
func TestProtectedRoute_NoAuth(t *testing.T) {
	env := newTestEnv(t)
	cases := []struct {
		method, path string
	}{
		{http.MethodGet, "/api/v1/templates"},
		{http.MethodPost, "/api/v1/templates"},
		{http.MethodGet, "/api/v1/templates/x"},
		{http.MethodDelete, "/api/v1/templates/x"},
		{http.MethodPost, "/api/v1/sso/tokens"},
	}
	for _, tc := range cases {
		_, e := doRaw(t, env.engine, tc.method, tc.path, "")
		if e.Code != response.ErrUnauthenticated {
			t.Fatalf("%s %s without token code=%d, want %d", tc.method, tc.path, e.Code, response.ErrUnauthenticated)
		}
	}
}

// ---------------------------------------------------------------------------
// SsoHandler
// ---------------------------------------------------------------------------

// TestSso_CreateToken_AndLogin_Flow: an authenticated admin mints an SSO token,
// then SsoLogin (without auth) exchanges it for a JWT.
func TestSso_CreateToken_AndLogin_Flow(t *testing.T) {
	env := newTestEnv(t)
	token := setupAdmin(t, env)

	// Mint an SSO token (auth required).
	_, e := doJSON(t, env.engine, http.MethodPost, "/api/v1/sso/tokens", token, gin.H{
		"username": "admin",
	})
	if e.Code != response.ErrOK {
		t.Fatalf("create token code=%d msg=%s", e.Code, e.Message)
	}
	var ctr createTokenResp
	raw, _ := json.Marshal(e.Data)
	if err := json.Unmarshal(raw, &ctr); err != nil {
		t.Fatalf("decode createTokenResp: %v", err)
	}
	if ctr.UUID == "" || ctr.Password == "" || ctr.Username != "admin" {
		t.Fatalf("createTokenResp incomplete: %+v", ctr)
	}

	// SsoLogin via JSON body (no auth).
	_, e = doJSON(t, env.engine, http.MethodPost, "/api/v1/sso_login", "", gin.H{
		"uuid":     ctr.UUID,
		"password": ctr.Password,
	})
	if e.Code != response.ErrOK {
		t.Fatalf("sso_login code=%d msg=%s", e.Code, e.Message)
	}
	var ar authResp
	raw, _ = json.Marshal(e.Data)
	if err := json.Unmarshal(raw, &ar); err != nil {
		t.Fatalf("decode ssoLogin authResp: %v", err)
	}
	if ar.Token == "" {
		t.Fatalf("sso_login returned empty token")
	}
}

// TestSso_Login_ViaQuery: SsoLogin accepts uuid/password as query params.
func TestSso_Login_ViaQuery(t *testing.T) {
	env := newTestEnv(t)
	token := setupAdmin(t, env)

	_, e := doJSON(t, env.engine, http.MethodPost, "/api/v1/sso/tokens", token, gin.H{
		"username": "admin",
	})
	var ctr createTokenResp
	raw, _ := json.Marshal(e.Data)
	if err := json.Unmarshal(raw, &ctr); err != nil {
		t.Fatalf("decode: %v", err)
	}

	path := "/api/v1/sso_login?uuid=" + ctr.UUID + "&password=" + ctr.Password
	_, e = doRaw(t, env.engine, http.MethodPost, path, "")
	if e.Code != response.ErrOK {
		t.Fatalf("sso_login via query code=%d msg=%s", e.Code, e.Message)
	}
}

// TestSso_Login_Reused: a token used once cannot be reused.
func TestSso_Login_Reused(t *testing.T) {
	env := newTestEnv(t)
	token := setupAdmin(t, env)

	_, e := doJSON(t, env.engine, http.MethodPost, "/api/v1/sso/tokens", token, gin.H{
		"username": "admin",
	})
	var ctr createTokenResp
	raw, _ := json.Marshal(e.Data)
	if err := json.Unmarshal(raw, &ctr); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// First use succeeds.
	_, e = doJSON(t, env.engine, http.MethodPost, "/api/v1/sso_login", "", gin.H{
		"uuid": ctr.UUID, "password": ctr.Password,
	})
	if e.Code != response.ErrOK {
		t.Fatalf("first sso_login code=%d msg=%s", e.Code, e.Message)
	}
	// Second use fails.
	_, e = doJSON(t, env.engine, http.MethodPost, "/api/v1/sso_login", "", gin.H{
		"uuid": ctr.UUID, "password": ctr.Password,
	})
	if e.Code != response.ErrInvalidCredentials {
		t.Fatalf("reused sso_login code=%d, want %d", e.Code, response.ErrInvalidCredentials)
	}
}

// TestSso_Redirect: redirect=1 yields a 302 to /?token=.
func TestSso_Redirect(t *testing.T) {
	env := newTestEnv(t)
	token := setupAdmin(t, env)

	_, e := doJSON(t, env.engine, http.MethodPost, "/api/v1/sso/tokens", token, gin.H{
		"username": "admin",
	})
	var ctr createTokenResp
	raw, _ := json.Marshal(e.Data)
	if err := json.Unmarshal(raw, &ctr); err != nil {
		t.Fatalf("decode: %v", err)
	}

	path := "/api/v1/sso_login?uuid=" + ctr.UUID + "&password=" + ctr.Password + "&redirect=1"
	w, _ := doRaw(t, env.engine, http.MethodPost, path, "")
	if w.Code != http.StatusFound {
		t.Fatalf("redirect status=%d, want %d", w.Code, http.StatusFound)
	}
	loc := w.Header().Get("Location")
	if !strings.HasPrefix(loc, "/?token=") {
		t.Fatalf("Location=%q, want /?token=<jwt>", loc)
	}
}

// TestSso_CreateToken_NoAuth: minting without auth is rejected.
func TestSso_CreateToken_NoAuth(t *testing.T) {
	env := newTestEnv(t)
	_, e := doJSON(t, env.engine, http.MethodPost, "/api/v1/sso/tokens", "", gin.H{
		"username": "admin",
	})
	if e.Code != response.ErrUnauthenticated {
		t.Fatalf("create token without auth code=%d, want %d", e.Code, response.ErrUnauthenticated)
	}
}

// TestSso_CreateToken_NonAdminForbidden: an authenticated non-admin (a token
// whose adm claim is false) is rejected with ErrForbidden, proving the
// admin-only gate reads the JWT's IsAdmin rather than blindly allowing any
// authenticated caller. We forge the non-admin token by signing with the same
// test secret the middleware uses; the Auth middleware accepts it (valid sig +
// unexpired) but is_admin comes out false.
func TestSso_CreateToken_NonAdminForbidden(t *testing.T) {
	env := newTestEnv(t)
	setupAdmin(t, env) // ensure the system is initialized (and an admin exists)

	// Forge a non-admin token with the SAME secret the middleware verifies
	// against. This is exactly what a real non-admin user's login would have
	// received: valid signature, adm=false.
	nonAdmin, err := auth.Sign(testSecret, auth.NewClaims("non-admin-id", "regular", false, time.Hour))
	if err != nil {
		t.Fatalf("auth.Sign non-admin: %v", err)
	}

	_, e := doJSON(t, env.engine, http.MethodPost, "/api/v1/sso/tokens", nonAdmin, gin.H{
		"username": "admin",
	})
	if e.Code != response.ErrForbidden {
		t.Fatalf("create token as non-admin code=%d, want %d (ErrForbidden)", e.Code, response.ErrForbidden)
	}
}

// TestSso_CreateToken_AdminSucceeds: an authenticated admin (the default setup
// user) can still mint tokens — the positive counterpart to the non-admin test,
// guarding against the admin gate being too strict.
func TestSso_CreateToken_AdminSucceeds(t *testing.T) {
	env := newTestEnv(t)
	token := setupAdmin(t, env)
	_, e := doJSON(t, env.engine, http.MethodPost, "/api/v1/sso/tokens", token, gin.H{
		"username": "admin",
	})
	if e.Code != response.ErrOK {
		t.Fatalf("create token as admin code=%d, want %d (msg=%s)", e.Code, response.ErrOK, e.Message)
	}
}

// ---------------------------------------------------------------------------
// TemplateHandler (full CRUD over HTTP)
// ---------------------------------------------------------------------------

// TestTemplate_CRUD_Flow exercises Create -> Get -> List -> Update -> Delete.
func TestTemplate_CRUD_Flow(t *testing.T) {
	env := newTestEnv(t)
	token := setupAdmin(t, env)

	// Create.
	_, e := doJSON(t, env.engine, http.MethodPost, "/api/v1/templates", token, gin.H{
		"name":    "bash-tpl",
		"command": "/bin/bash",
		"args":    []string{"-l"},
		"cwd":     "/tmp",
		"env":     []model.KV{{Key: "FOO", Value: "bar"}},
	})
	if e.Code != response.ErrOK {
		t.Fatalf("create code=%d msg=%s", e.Code, e.Message)
	}
	var tpl model.Template
	raw, _ := json.Marshal(e.Data)
	if err := json.Unmarshal(raw, &tpl); err != nil {
		t.Fatalf("decode template: %v", err)
	}
	if tpl.ID == "" {
		t.Fatalf("create did not fill ID")
	}
	if tpl.Command != "/bin/bash" {
		t.Fatalf("command=%q, want /bin/bash", tpl.Command)
	}
	if len(tpl.Env) != 1 || tpl.Env[0].Key != "FOO" {
		t.Fatalf("env not round-tripped: %+v", tpl.Env)
	}

	// Get.
	_, e = doRaw(t, env.engine, http.MethodGet, "/api/v1/templates/"+tpl.ID, token)
	if e.Code != response.ErrOK {
		t.Fatalf("get code=%d msg=%s", e.Code, e.Message)
	}

	// List.
	_, e = doRaw(t, env.engine, http.MethodGet, "/api/v1/templates", token)
	if e.Code != response.ErrOK {
		t.Fatalf("list code=%d msg=%s", e.Code, e.Message)
	}

	// Update.
	_, e = doJSON(t, env.engine, http.MethodPut, "/api/v1/templates/"+tpl.ID, token, gin.H{
		"name":    "bash-tpl",
		"command": "sh",
		"args":    []string{},
	})
	if e.Code != response.ErrOK {
		t.Fatalf("update code=%d msg=%s", e.Code, e.Message)
	}

	// Delete.
	_, e = doRaw(t, env.engine, http.MethodDelete, "/api/v1/templates/"+tpl.ID, token)
	if e.Code != response.ErrOK {
		t.Fatalf("delete code=%d msg=%s", e.Code, e.Message)
	}

	// Get after delete -> NotFound.
	_, e = doRaw(t, env.engine, http.MethodGet, "/api/v1/templates/"+tpl.ID, token)
	if e.Code != response.ErrNotFound {
		t.Fatalf("get after delete code=%d, want %d", e.Code, response.ErrNotFound)
	}
}

// TestTemplate_CreateDuplicateName: a second create with the same name fails.
func TestTemplate_CreateDuplicateName(t *testing.T) {
	env := newTestEnv(t)
	token := setupAdmin(t, env)
	doJSON(t, env.engine, http.MethodPost, "/api/v1/templates", token, gin.H{"name": "dup"})
	_, e := doJSON(t, env.engine, http.MethodPost, "/api/v1/templates", token, gin.H{"name": "dup"})
	if e.Code != response.ErrTemplateNameDuplicate {
		t.Fatalf("duplicate create code=%d, want %d", e.Code, response.ErrTemplateNameDuplicate)
	}
}

// TestTemplate_GetMissing: unknown id returns ErrNotFound.
func TestTemplate_GetMissing(t *testing.T) {
	env := newTestEnv(t)
	token := setupAdmin(t, env)
	_, e := doRaw(t, env.engine, http.MethodGet, "/api/v1/templates/no-such-id", token)
	if e.Code != response.ErrNotFound {
		t.Fatalf("get missing code=%d, want %d", e.Code, response.ErrNotFound)
	}
}

// TestTemplate_NoAuth: template routes require auth.
func TestTemplate_NoAuth(t *testing.T) {
	env := newTestEnv(t)
	_, e := doRaw(t, env.engine, http.MethodGet, "/api/v1/templates", "")
	if e.Code != response.ErrUnauthenticated {
		t.Fatalf("templates without auth code=%d, want %d", e.Code, response.ErrUnauthenticated)
	}
}

// ---------------------------------------------------------------------------
// Recovery middleware
// ---------------------------------------------------------------------------

// TestRecovery_PanicReturns500: a panicking handler yields a 500 + envelope,
// not a bare stack trace or 200.
func TestRecovery_PanicReturns500(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware.Recovery())
	r.GET("/boom", func(c *gin.Context) { panic("kaboom") })

	req := httptest.NewRequest(http.MethodGet, "/boom", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d, want 500", w.Code)
	}
	var env response.Envelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode envelope: %v (body=%q)", err, w.Body.String())
	}
	if env.Code != response.ErrInternal {
		t.Fatalf("code=%d, want %d", env.Code, response.ErrInternal)
	}
	if env.Message != "internal server error" {
		t.Fatalf("message=%q, want %q", env.Message, "internal server error")
	}
}

// ---------------------------------------------------------------------------
// CORS middleware
// ---------------------------------------------------------------------------

// TestCORSPreflight: an OPTIONS request returns 204 with the right headers.
func TestCORSPreflight(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware.CORS([]string{"https://app.example.com"}))

	req := httptest.NewRequest(http.MethodOptions, "/anything", nil)
	req.Header.Set("Origin", "https://app.example.com")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status=%d, want 204", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
		t.Fatalf("ACAO=%q, want https://app.example.com", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Fatalf("ACAC=%q, want true", got)
	}
}

// TestCORS_Wildcard: a "*" config echoes * and omits credentials.
func TestCORS_Wildcard(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware.CORS([]string{"*"}))
	r.GET("/x", func(c *gin.Context) { c.Status(200) })

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Origin", "https://anywhere.example.com")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("ACAO=%q, want *", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Fatalf("ACAC=%q, want empty (forbidden with wildcard)", got)
	}
}

// TestCORS_DisallowedOrigin: an origin not in the list gets no ACAO header.
func TestCORS_DisallowedOrigin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware.CORS([]string{"https://app.example.com"}))
	r.GET("/x", func(c *gin.Context) { c.Status(200) })

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Origin", "https://evil.example.com")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("ACAO=%q, want empty for disallowed origin", got)
	}
}

// TestCORS_EmptyList: no allowed origins → no headers added (same-origin only).
func TestCORS_EmptyList(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware.CORS(nil))
	r.GET("/x", func(c *gin.Context) { c.Status(200) })

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Origin", "https://anywhere.example.com")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("ACAO=%q, want empty when no origins configured", got)
	}
}