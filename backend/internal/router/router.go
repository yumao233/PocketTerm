// Package router wires the Webshell HTTP surface onto gin. It is the only layer
// that knows the URL shape: it mounts the global middlewares (recovery, logging,
// CORS), registers the public + authenticated route groups and binds each path to
// the matching handler method.
//
// Route ownership:
//   - Public (no auth):    /healthz, /api/v1/check-setup, /api/v1/setup,
//     /api/v1/login, /api/v1/sso_login
//   - Bearer auth (Auth):   /api/v1/logout, /api/v1/me, /api/v1/templates*,
//     /api/v1/sessions*, /api/v1/system/stats, /api/v1/sso/tokens
//   - Query-token auth:     /api/v1/ws/:id  (WSAuth — browsers cannot set the
//     Authorization header on a WS upgrade, so the JWT is
//     passed as ?token=<jwt>)
//
// The WebSocket route is intentionally registered OUTSIDE the authenticated
// group so that only WSAuth (never the group's Auth) guards the upgrade. WSAuth
// populates the same context keys (userID/username) as Auth, so SessionHandler.WS
// can read the caller identity uniformly.
package router

import (
	"github.com/gin-gonic/gin"

	"github.com/wangkun/webshell/internal/config"
	"github.com/wangkun/webshell/internal/handler"
	"github.com/wangkun/webshell/internal/middleware"
	"github.com/wangkun/webshell/internal/pty"
	"github.com/wangkun/webshell/internal/service"
	"github.com/wangkun/webshell/pkg/response"
)

// Deps bundles the already-constructed dependencies the router needs. Keeping
// them in a struct (rather than a long positional list) makes the call site in
// main readable and keeps additions non-breaking.
type Deps struct {
	Cfg         *config.Config
	AuthSvc     *service.AuthService
	TemplateSvc *service.TemplateService
	PtySvc      *service.PtyService
	PtyManager  *pty.Manager
	SysInfoSvc  *service.SysInfoService
}

// New builds the gin engine and registers every route. The returned engine is
// ready to be handed to http.Server.
func New(deps Deps) *gin.Engine {
	r := gin.New()
	r.Use(middleware.Recovery())
	r.Use(middleware.Logger())
	r.Use(middleware.CORS(deps.Cfg.CORS.AllowedOrigins))

	// Health check — free (no auth, no envelope contract enforcement beyond a
	// simple ok payload). Used by orchestrators / load balancers.
	r.GET("/healthz", func(c *gin.Context) {
		response.OK(c, gin.H{"status": "ok"})
	})

	api := r.Group("/api/v1")

	authH := handler.NewAuthHandler(deps.AuthSvc)
	ssoH := handler.NewSsoHandler(deps.AuthSvc)
	tplH := handler.NewTemplateHandler(deps.TemplateSvc)
	sessH := handler.NewSessionHandler(deps.PtySvc, deps.PtyManager)
	sysinfoH := handler.NewSysInfoHandler(deps.SysInfoSvc)

	// --- public routes (no auth) ---
	api.GET("/check-setup", authH.CheckSetup)
	api.POST("/setup", authH.Setup)
	api.POST("/login", authH.Login)
	api.POST("/sso_login", ssoH.SsoLogin)

	// --- authenticated routes (Bearer JWT) ---
	authed := api.Group("")
	authed.Use(middleware.Auth(deps.Cfg.JWT.Secret))
	{
		authed.POST("/logout", authH.Logout)
		authed.GET("/me", authH.Me)

		// templates CRUD
		authed.GET("/templates", tplH.List)
		authed.POST("/templates", tplH.Create)
		authed.GET("/templates/:id", tplH.Get)
		authed.PUT("/templates/:id", tplH.Update)
		authed.DELETE("/templates/:id", tplH.Delete)

		// sessions lifecycle
		authed.GET("/sessions", sessH.List)
		authed.POST("/sessions", sessH.Create)
		authed.GET("/sessions/:id", sessH.Get)
		authed.DELETE("/sessions/:id", sessH.Delete)

		// system monitoring (host CPU/RAM/RTT snapshot; read-only, best-effort)
		authed.GET("/system/stats", sysinfoH.Stats)

		// SSO token minting (admin / authenticated only)
		authed.POST("/sso/tokens", ssoH.CreateToken)
	}

	// --- WebSocket route (query-token auth) ---
	// Registered on `api` (NOT the authed group) so the ONLY auth middleware that
	// runs is WSAuth. gin does not conflict here: /ws/:id and /sessions/:id share a
	// param position but have distinct static prefixes ("ws" vs "sessions"), so
	// they live in separate branches of the radix tree.
	api.GET("/ws/:id", middleware.WSAuth(deps.Cfg.JWT.Secret), sessH.WS)

	return r
}
