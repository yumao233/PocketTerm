// Package handler implements the HTTP handlers for the Webshell REST API.
//
// Each handler is a thin adapter over a service: it binds the request, calls the
// service, and maps the service's (data, code, err) triple onto the unified
// response envelope. All non-WebSocket handlers return response.Envelope JSON
// (HTTP 200 with a logical code), as mandated by the API contract.
package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/wangkun/webshell/internal/middleware"
	"github.com/wangkun/webshell/internal/service"
	"github.com/wangkun/webshell/pkg/response"
)

// AuthHandler exposes the auth lifecycle: setup, login, logout, me and the
// setup probe consumed by the frontend bootstrap.
type AuthHandler struct {
	svc *service.AuthService
}

// NewAuthHandler builds an AuthHandler backed by svc.
func NewAuthHandler(svc *service.AuthService) *AuthHandler {
	return &AuthHandler{svc: svc}
}

// setupReq is the JSON body for POST /setup.
type setupReq struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// loginReq is the JSON body for POST /login.
type loginReq struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// authResp carries the fresh JWT plus the sanitized user record.
type authResp struct {
	Token string      `json:"token"`
	User  interface{} `json:"user"`
}

// checkSetupResp is the body of GET /check-setup.
type checkSetupResp struct {
	Initialized bool `json:"initialized"`
}

// Setup registers the first admin user and returns a JWT. Binding failures are
// mapped to ErrInvalidParam. Service errors (already initialized, bad params)
// are surfaced verbatim via the envelope code.
func (h *AuthHandler) Setup(c *gin.Context) {
	var req setupReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, response.ErrInvalidParam, "invalid request body")
		return
	}
	user, token, code, err := h.svc.Setup(req.Username, req.Password)
	if err != nil {
		response.Fail(c, code, err.Error())
		return
	}
	response.OK(c, authResp{Token: token, User: user})
}

// Login validates credentials and returns a JWT plus the sanitized user.
func (h *AuthHandler) Login(c *gin.Context) {
	var req loginReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, response.ErrInvalidParam, "invalid request body")
		return
	}
	user, token, code, err := h.svc.Login(req.Username, req.Password)
	if err != nil {
		response.Fail(c, code, err.Error())
		return
	}
	response.OK(c, authResp{Token: token, User: user})
}

// Logout is a stateless no-op: the JWT is not server-side tracked, so the
// client simply discards it. We return a success envelope so the frontend can
// branch on code==0 uniformly.
func (h *AuthHandler) Logout(c *gin.Context) {
	response.OKMsg(c, "logged out", nil)
}

// Me returns the sanitized user for the authenticated subject. The userID is
// pulled from the context (set by the Auth middleware); there is no request
// body.
func (h *AuthHandler) Me(c *gin.Context) {
	uid, _ := c.Get(middleware.CtxUserIDKey())
	userID, _ := uid.(string)
	user, code, err := h.svc.Me(userID)
	if err != nil {
		response.Fail(c, code, err.Error())
		return
	}
	response.OK(c, gin.H{"user": user})
}

// CheckSetup reports whether the system has been initialized (an admin user
// exists). The response is {initialized: bool}.
func (h *AuthHandler) CheckSetup(c *gin.Context) {
	initialized, err := h.svc.CheckSetup()
	if err != nil {
		response.Fail(c, response.ErrInternal, err.Error())
		return
	}
	response.OK(c, checkSetupResp{Initialized: !initialized})
}