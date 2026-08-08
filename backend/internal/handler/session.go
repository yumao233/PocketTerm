package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/wangkun/webshell/internal/middleware"
	"github.com/wangkun/webshell/internal/model"
	"github.com/wangkun/webshell/internal/pty"
	"github.com/wangkun/webshell/internal/service"
	"github.com/wangkun/webshell/internal/ws"
	"github.com/wangkun/webshell/pkg/response"
)

// SessionHandler exposes the session lifecycle (list/start/get/kill) plus the
// WebSocket bridge that streams PTY I/O to the browser.
type SessionHandler struct {
	svc *service.PtyService
	mgr *pty.Manager
}

// NewSessionHandler builds a SessionHandler. svc enforces ownership and
// persists metadata; mgr is the live PTY pool used by the WS bridge.
func NewSessionHandler(svc *service.PtyService, mgr *pty.Manager) *SessionHandler {
	return &SessionHandler{svc: svc, mgr: mgr}
}

// createSessionReq is the JSON body for POST /sessions.
type createSessionReq struct {
	TemplateID string `json:"template_id"`
	Title      string `json:"title"`
}

// List returns the calling user's sessions (metadata only).
func (h *SessionHandler) List(c *gin.Context) {
	user := ctxUser(c)
	sessions, code, err := h.svc.List(user.ID)
	if err != nil {
		response.Fail(c, code, err.Error())
		return
	}
	response.OK(c, gin.H{"sessions": sessions})
}

// Create launches a new terminal session from the named template.
func (h *SessionHandler) Create(c *gin.Context) {
	user := ctxUser(c)
	var req createSessionReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, response.ErrInvalidParam, "invalid request body")
		return
	}
	rec, code, err := h.svc.Start(user, req.TemplateID, req.Title)
	if err != nil {
		response.Fail(c, code, err.Error())
		return
	}
	response.OK(c, rec)
}

// Get returns the metadata for a single owned session.
func (h *SessionHandler) Get(c *gin.Context) {
	user := ctxUser(c)
	rec, code, err := h.svc.Get(user, c.Param("id"))
	if err != nil {
		response.Fail(c, code, err.Error())
		return
	}
	response.OK(c, rec)
}

// Delete kills the session and removes it from the in-memory pool. Nothing is
// persisted: once Kill returns the session is no longer observable via GET/List
// (they report ErrNotFound). The service enforces ownership; a foreign session
// yields ErrForbidden.
func (h *SessionHandler) Delete(c *gin.Context) {
	user := ctxUser(c)
	id := c.Param("id")
	code, err := h.svc.Kill(user, id)
	if err != nil {
		response.Fail(c, code, err.Error())
		return
	}
	response.OKMsg(c, "killed", nil)
}

// WS upgrades the HTTP connection to a WebSocket bridging the browser to the
// live PTY session. Ownership is verified (via svc.Get) BEFORE the upgrade so an
// unauthenticated or foreign user never reaches the PTY. Because sessions are
// pure in-memory, a session that has already ended is gone from the pool —
// svc.Get returns ErrNotFound first, so the common "connect to an ended session"
// case answers 1006 (there is no persisted closed record that could yield 3002).
// The live-pool nil check below only fires in the narrow race where the session
// exits between svc.Get and mgr.Get; in that window we reject with
// ErrSessionClosed (3002) instead of upgrading into a dead channel.
func (h *SessionHandler) WS(c *gin.Context) {
	user := ctxUser(c)
	id := c.Param("id")

	// Ownership check first — must succeed before touching the live pool.
	if _, code, err := h.svc.Get(user, id); err != nil {
		response.Fail(c, code, err.Error())
		c.Abort()
		return
	}

	live := h.mgr.Get(id)
	if live == nil {
		// Race: the session exited between the ownership check above and this
		// pool lookup. Reject rather than upgrade into a dead channel.
		response.Fail(c, response.ErrSessionClosed, "session closed")
		c.Abort()
		return
	}

	ws.ServeWS(c, live, func() {
		_, _ = h.svc.Kill(user, id)
	})
}

// ctxUser rebuilds a minimal model.User from the context values set by the
// Auth/WSAuth middlewares. The PTY service only reads ID and Username for
// ownership checks, so we do not need the full persisted record here. If the
// values are unexpectedly absent (misconfigured route) the zero value yields a
// service-level ErrInvalidParam rather than a panic.
func ctxUser(c *gin.Context) *model.User {
	u := &model.User{}
	if v, ok := c.Get(middleware.CtxUserIDKey()); ok {
		if s, ok := v.(string); ok {
			u.ID = s
		}
	}
	if v, ok := c.Get(middleware.CtxUsernameKey()); ok {
		if s, ok := v.(string); ok {
			u.Username = s
		}
	}
	return u
}