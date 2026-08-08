package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/wangkun/webshell/internal/service"
	"github.com/wangkun/webshell/pkg/response"
)

// SsoHandler implements the single-sign-on token flow: an external system
// exchanges a pre-generated uuid/password pair for a JWT via SsoLogin, and an
// authenticated admin mints such a pair via CreateToken.
type SsoHandler struct {
	svc *service.AuthService
}

// NewSsoHandler builds an SsoHandler backed by svc.
func NewSsoHandler(svc *service.AuthService) *SsoHandler {
	return &SsoHandler{svc: svc}
}

// ssoLoginReq carries the uuid/password credentials submitted by an external
// system. They may arrive as JSON body OR as query parameters, so binding is
// best-effort.
type ssoLoginReq struct {
	UUID     string `json:"uuid"`
	Password string `json:"password"`
}

// ssoLoginResp mirrors authResp: a fresh JWT plus the bound, sanitized user.
type ssoLoginResp struct {
	Token string      `json:"token"`
	User  interface{} `json:"user"`
}

// createTokenReq is the JSON body for POST /sso/tokens.
type createTokenReq struct {
	Username string `json:"username"`
}

// createTokenResp carries the freshly minted one-time credentials. The password
// is plaintext and is returned exactly once; the caller must deliver it
// securely to the external system.
type createTokenResp struct {
	UUID     string `json:"uuid"`
	Password string `json:"password"`
	Username string `json:"username"`
}

// SsoLogin exchanges a one-time SSO token for a JWT. The uuid and password may
// be supplied either as query parameters (useful for a redirect link) or as a
// JSON body; query takes precedence so a typed body never overrides a link.
//
// When redirect=1 is set and authentication succeeds, the response is a 302 to
// "/?token=<jwt>" so the browser lands directly in the app. Otherwise a normal
// {token, user} envelope is returned.
func (h *SsoHandler) SsoLogin(c *gin.Context) {
	var body ssoLoginReq
	// Best-effort bind: an empty/non-JSON body is fine (query params used).
	_ = c.ShouldBindJSON(&body)

	uuid := firstNonEmpty(c.Query("uuid"), body.UUID)
	password := firstNonEmpty(c.Query("password"), body.Password)

	user, token, code, err := h.svc.SsoLogin(uuid, password)
	if err != nil {
		response.Fail(c, code, err.Error())
		return
	}

	if c.Query("redirect") == "1" {
		c.Redirect(http.StatusFound, "/?token="+token)
		return
	}
	response.OK(c, ssoLoginResp{Token: token, User: user})
}

// CreateToken mints a one-time SSO token bound to the given username. The
// caller must be an authenticated administrator (Auth middleware sets the
// is_admin context key from the JWT's adm claim); the target user must already
// exist. A non-admin authenticated user is rejected with ErrForbidden before
// any business logic runs.
func (h *SsoHandler) CreateToken(c *gin.Context) {
	raw, exists := c.Get("is_admin")
	isAdmin, _ := raw.(bool) // missing → zero-value false → forbidden
	if !exists || !isAdmin {
		response.Fail(c, response.ErrForbidden, "admin only")
		c.Abort()
		return
	}
	var req createTokenReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, response.ErrInvalidParam, "invalid request body")
		return
	}
	tok, code, err := h.svc.GenSsoToken(req.Username)
	if err != nil {
		response.Fail(c, code, err.Error())
		return
	}
	response.OK(c, createTokenResp{
		UUID:     tok.ID,
		Password: tok.Password,
		Username: tok.Username,
	})
}

// firstNonEmpty returns s if non-empty (after trimming), otherwise fallback.
func firstNonEmpty(s, fallback string) string {
	if s != "" {
		return s
	}
	return fallback
}