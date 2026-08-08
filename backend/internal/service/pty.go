package service

import (
	"errors"
	"fmt"

	"github.com/wangkun/webshell/internal/model"
	"github.com/wangkun/webshell/internal/pty"
	"github.com/wangkun/webshell/pkg/response"
)

// PtyService bridges the PTY session pool (pty.Manager) with the TemplateService.
// It enforces per-user ownership of sessions so one user can never read, list or
// kill another user's terminal. Session metadata lives only in the Manager's
// memory pool — nothing is persisted to disk.
type PtyService struct {
	manager *pty.Manager
	tplSvc  *TemplateService
}

// NewPtyService builds a PtyService. tplSvc is used to resolve the template a
// new session should be launched from.
func NewPtyService(manager *pty.Manager, tplSvc *TemplateService) *PtyService {
	return &PtyService{manager: manager, tplSvc: tplSvc}
}

// List returns the calling user's live sessions from the in-memory pool
// (metadata only). A session whose PTY has exited is already gone from the pool,
// so it will not appear here.
func (s *PtyService) List(userID string) ([]*model.Session, int, error) {
	sessions, _ := s.manager.ListByUser(userID)
	return sessions, response.ErrOK, nil
}

// Start launches a new terminal session for the user from the named template.
// If title is empty the template's Name is used. The template is resolved via
// the TemplateService (ErrNotFound if missing); the actual PTY/SSH lifecycle is
// delegated to pty.Manager.Start, which registers the live session in memory.
func (s *PtyService) Start(user *model.User, templateID, title string) (*model.Session, int, error) {
	if user == nil || user.ID == "" {
		return nil, response.ErrInvalidParam, errors.New("service: user required")
	}
	if templateID == "" {
		return nil, response.ErrInvalidParam, errors.New("service: template id required")
	}

	tpl, code, err := s.tplSvc.Get(templateID)
	if err != nil {
		return nil, code, err
	}

	if title == "" {
		title = tpl.Name
	}

	// pty.Manager.Start dials the local SSH server, opens the PTY, runs the
	// template command and registers the live session in the in-memory pool. It
	// returns a metadata snapshot; the live *pty.Session is obtained via
	// manager.Get(id) by the WS layer.
	rec, startErr := s.manager.Start(user, tpl)
	if startErr != nil {
		// Manager failures are infrastructure-level (dial, pty); surface as
		// internal error.
		_ = title
		return nil, response.ErrInternal, fmt.Errorf("service: start pty: %w", startErr)
	}
	// Honor the caller's title: if they passed one (different from tpl.Name),
	// update the live session's title in memory and sync the returned record so
	// the UI shows it.
	if title != "" && title != rec.Title {
		s.manager.SetTitle(rec.ID, title)
		rec.Title = title
	}
	return rec, response.ErrOK, nil
}

// Get returns the session metadata for sessionID owned by user. A missing
// session (not in the live pool — already exited or never existed) yields
// ErrNotFound; a session owned by a different user yields ErrForbidden.
func (s *PtyService) Get(user *model.User, sessionID string) (*model.Session, int, error) {
	if user == nil || sessionID == "" {
		return nil, response.ErrInvalidParam, errors.New("service: user and session id required")
	}
	rec := s.manager.GetMeta(sessionID)
	if rec == nil {
		return nil, response.ErrNotFound, errors.New("service: session not found")
	}
	if rec.UserID != user.ID {
		return nil, response.ErrForbidden, errors.New("service: session not owned by user")
	}
	return rec, response.ErrOK, nil
}

// Kill terminates the session owned by user. Ownership is checked first via Get
// (ErrForbidden / ErrNotFound). Because sessions are pure in-memory, a session
// that has already ended is gone from the pool — Get returns ErrNotFound first,
// so the common "DELETE an already-ended session" case surfaces 1006 (there is
// no persisted closed record; "already ended" is indistinguishable from "never
// existed"). The ErrSessionNotFound branch below only fires in the narrow race
// where the session exits between Get and manager.Kill; in that window treating
// the kill as success is correct (the PTY is already tearing down).
func (s *PtyService) Kill(user *model.User, sessionID string) (int, error) {
	if _, code, err := s.Get(user, sessionID); err != nil {
		return code, err
	}

	killErr := s.manager.Kill(sessionID)
	if killErr != nil {
		if errors.Is(killErr, pty.ErrSessionNotFound) {
			// Race: the session exited between the ownership check above and
			// manager.Kill. Nothing left to tear down; report success.
			return response.ErrOK, nil
		}
		return response.ErrInternal, fmt.Errorf("service: kill pty: %w", killErr)
	}
	return response.ErrOK, nil
}