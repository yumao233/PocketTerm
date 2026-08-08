package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/wangkun/webshell/internal/model"
	"github.com/wangkun/webshell/internal/service"
	"github.com/wangkun/webshell/pkg/response"
)

// TemplateHandler exposes CRUD over terminal templates.
type TemplateHandler struct {
	svc *service.TemplateService
}

// NewTemplateHandler builds a TemplateHandler backed by svc.
func NewTemplateHandler(svc *service.TemplateService) *TemplateHandler {
	return &TemplateHandler{svc: svc}
}

// templateReq is the JSON body for Create/Update. Env is a list of {key,value}
// pairs mirroring model.KV. All fields are optional except name.
type templateReq struct {
	Name    string     `json:"name"`
	Command string     `json:"command"`
	Args    []string   `json:"args"`
	Cwd     string     `json:"cwd"`
	Env     []model.KV `json:"env"`
}

// List returns all templates.
func (h *TemplateHandler) List(c *gin.Context) {
	ts, code, err := h.svc.List()
	if err != nil {
		response.Fail(c, code, err.Error())
		return
	}
	response.OK(c, gin.H{"templates": ts})
}

// Create persists a new template. The service fills ID, timestamps and the
// default Command; duplicate names are rejected with ErrTemplateNameDuplicate.
func (h *TemplateHandler) Create(c *gin.Context) {
	var req templateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, response.ErrInvalidParam, "invalid request body")
		return
	}
	tpl := &model.Template{
		Name:    req.Name,
		Command: req.Command,
		Args:    req.Args,
		Cwd:     req.Cwd,
		Env:     req.Env,
	}
	created, code, err := h.svc.Create(tpl)
	if err != nil {
		response.Fail(c, code, err.Error())
		return
	}
	response.OK(c, created)
}

// Get returns the template identified by :id.
func (h *TemplateHandler) Get(c *gin.Context) {
	id := c.Param("id")
	tpl, code, err := h.svc.Get(id)
	if err != nil {
		response.Fail(c, code, err.Error())
		return
	}
	response.OK(c, tpl)
}

// Update overwrites the mutable fields of the template identified by :id. The
// ID comes from the URL; service.Update preserves CreatedAt.
func (h *TemplateHandler) Update(c *gin.Context) {
	id := c.Param("id")
	var req templateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, response.ErrInvalidParam, "invalid request body")
		return
	}
	tpl := &model.Template{
		ID:      id,
		Name:    req.Name,
		Command: req.Command,
		Args:    req.Args,
		Cwd:     req.Cwd,
		Env:     req.Env,
	}
	updated, code, err := h.svc.Update(tpl)
	if err != nil {
		response.Fail(c, code, err.Error())
		return
	}
	response.OK(c, updated)
}

// Delete removes the template identified by :id. Idempotent at the store level;
// the service surfaces ErrNotFound for an unknown id.
func (h *TemplateHandler) Delete(c *gin.Context) {
	id := c.Param("id")
	code, err := h.svc.Delete(id)
	if err != nil {
		response.Fail(c, code, err.Error())
		return
	}
	response.OKMsg(c, "deleted", nil)
}