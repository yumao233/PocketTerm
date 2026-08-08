package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/wangkun/webshell/internal/service"
	"github.com/wangkun/webshell/pkg/response"
)

// SysInfoHandler exposes host-level resource metrics (CPU/RAM) for the
// frontend monitoring strip. The single endpoint is read-only and
// best-effort: any per-metric collection failure degrades that field rather
// than producing a 5xx.
type SysInfoHandler struct {
	svc *service.SysInfoService
}

// NewSysInfoHandler builds a SysInfoHandler backed by svc.
func NewSysInfoHandler(svc *service.SysInfoService) *SysInfoHandler {
	return &SysInfoHandler{svc: svc}
}

// Stats returns a real-time snapshot of host CPU and memory usage plus a
// coarse process-level RTT approximation. There is no request body. The
// service never returns a non-OK code (partial failures degrade to 0/-1), so
// any non-nil error here is a true internal failure mapped to ErrInternal.
//
// Route: GET /api/v1/system/stats (authenticated).
func (h *SysInfoHandler) Stats(c *gin.Context) {
	stats, code, err := h.svc.Stats()
	if err != nil {
		response.Fail(c, code, err.Error())
		return
	}
	response.OK(c, stats)
}