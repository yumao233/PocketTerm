package router

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/wangkun/webshell/internal/config"
	"github.com/wangkun/webshell/internal/model"
	"github.com/wangkun/webshell/internal/pty"
	"github.com/wangkun/webshell/internal/service"
)

// TestHealthzAndCheckSetup builds the full router with real (file-backed) stores
// and a dummy SSH address and verifies the two public routes the frontend
// bootstrap depends on: /healthz and /api/v1/check-setup. These routes do not
// touch the SSH server, so a bogus loopback address is fine; the /sessions and
// /ws routes are exercised by Task 12 (integration), not here.
//
// We also assert the WS route is reachable (404 vs 401 tells us it was
// registered outside the auth group: WSAuth rejects an empty token query with
// ErrUnauthenticated rather than authing through a missing Bearer header).
func TestHealthzAndCheckSetup(t *testing.T) {
	gin.SetMode(gin.TestMode)

	dir := t.TempDir()
	users, err := model.NewUserStore(dir)
	if err != nil {
		t.Fatalf("user store: %v", err)
	}
	tplStore, err := model.NewTemplateStore(dir)
	if err != nil {
		t.Fatalf("template store: %v", err)
	}
	sso, err := model.NewSsoTokenStore(dir)
	if err != nil {
		t.Fatalf("sso store: %v", err)
	}

	ptyMgr := pty.NewManager(pty.SSHDialConfig{Addr: "127.0.0.1:0", Passcode: "x"})
	authSvc := service.NewAuthService(users, sso, "test-secret", 60)
	tplSvc := service.NewTemplateService(tplStore)
	ptySvc := service.NewPtyService(ptyMgr, tplSvc)

	cfg := &config.Config{
		JWT:  config.JWTConfig{Secret: "test-secret", TTLMinutes: 60},
		CORS: config.CORSConfig{AllowedOrigins: []string{"*"}},
	}

	r := New(Deps{
		Cfg:         cfg,
		AuthSvc:     authSvc,
		TemplateSvc: tplSvc,
		PtySvc:      ptySvc,
		PtyManager:  ptyMgr,
	})

	// /healthz → env code 0, data.status == "ok".
	t.Run("healthz", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		r.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", w.Code)
		}
		var body struct {
			Code int             `json:"code"`
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("unmarshal: %v body=%s", err, w.Body.String())
		}
		if body.Code != 0 {
			t.Fatalf("code = %d, want 0", body.Code)
		}
		var data struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(body.Data, &data); err != nil {
			t.Fatalf("unmarshal data: %v", err)
		}
		if data.Status != "ok" {
			t.Fatalf("status = %q, want ok", data.Status)
		}
	})

	// Fresh store → check-setup reports initialized=false (no admin yet).
	t.Run("check-setup", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/check-setup", nil)
		r.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", w.Code)
		}
		var body struct {
			Code int `json:"code"`
			Data struct {
				Initialized bool `json:"initialized"`
			} `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("unmarshal: %v body=%s", err, w.Body.String())
		}
		if body.Code != 0 {
			t.Fatalf("code = %d, want 0", body.Code)
		}
		if body.Data.Initialized {
			t.Fatalf("initialized = true, want false on fresh store")
		}
	})

	// /api/v1/ws/:id without a token query must be rejected as unauthenticated
	// (not 404). This proves the route is registered (not swallowed by the auth
	// group's missing-Bearer path) and guarded by WSAuth.
	t.Run("ws_requires_token", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/ws/abc", nil)
		r.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (envelope)", w.Code)
		}
		var body struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("unmarshal: %v body=%s", err, w.Body.String())
		}
		if body.Code == 0 {
			t.Fatalf("code = 0, want non-zero (unauthenticated) body=%s", w.Body.String())
		}
		if body.Message != "unauthenticated" {
			t.Fatalf("message = %q, want unauthenticated", body.Message)
		}
	})

	// /api/v1/me without a Bearer header must be rejected — proves the authed
	// group's Auth middleware is mounted.
	t.Run("authed_requires_bearer", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
		r.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (envelope)", w.Code)
		}
		var body struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("unmarshal: %v body=%s", err, w.Body.String())
		}
		if body.Message != "unauthenticated" {
			t.Fatalf("message = %q, want unauthenticated", body.Message)
		}
	})
}
