package service

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/wangkun/webshell/internal/model"
	"github.com/wangkun/webshell/pkg/response"
)

// TemplateService provides CRUD over terminal templates with name-uniqueness
// enforcement. It fills IDs, timestamps and the default Command on create.
type TemplateService struct {
	store model.TemplateStore
}

// NewTemplateService builds a TemplateService backed by store.
func NewTemplateService(store model.TemplateStore) *TemplateService {
	return &TemplateService{store: store}
}

// List returns all templates.
func (s *TemplateService) List() ([]*model.Template, int, error) {
	ts, err := s.store.List()
	if err != nil {
		return nil, response.ErrInternal, fmt.Errorf("service: list templates: %w", err)
	}
	return ts, response.ErrOK, nil
}

// Get returns the template with the given id, or ErrNotFound.
func (s *TemplateService) Get(id string) (*model.Template, int, error) {
	t, err := s.store.GetByID(id)
	if err != nil {
		if errors.Is(err, model.ErrNotFound) {
			return nil, response.ErrNotFound, errors.New("service: template not found")
		}
		return nil, response.ErrInternal, fmt.Errorf("service: get template: %w", err)
	}
	return t, response.ErrOK, nil
}

// Create persists t as a new template. It assigns a fresh UUID, CreatedAt and
// UpdatedAt, defaults Command to "bash" when empty, and rejects a duplicate
// name with ErrTemplateNameDuplicate. The stored record (mirroring t) is
// returned.
func (s *TemplateService) Create(t *model.Template) (*model.Template, int, error) {
	if t == nil {
		return nil, response.ErrInvalidParam, errors.New("service: nil template")
	}
	if t.Name == "" {
		return nil, response.ErrInvalidParam, errors.New("service: template name required")
	}
	if dup, err := s.store.GetByName(t.Name); err == nil && dup != nil {
		return nil, response.ErrTemplateNameDuplicate, errors.New("service: template name already exists")
	} else if err != nil && !errors.Is(err, model.ErrNotFound) {
		return nil, response.ErrInternal, fmt.Errorf("service: check duplicate name: %w", err)
	}

	now := time.Now()
	if t.Command == "" {
		t.Command = "bash"
	}
	t.ID = uuid.NewString()
	t.CreatedAt = now
	t.UpdatedAt = now

	if err := s.store.Create(t); err != nil {
		if errors.Is(err, model.ErrDuplicate) {
			return nil, response.ErrTemplateNameDuplicate, err
		}
		return nil, response.ErrInternal, fmt.Errorf("service: create template: %w", err)
	}
	return t, response.ErrOK, nil
}

// Update patches an existing template with the fields on t and refreshes
// UpdatedAt. It rejects a name collision with a different template
// (ErrTemplateNameDuplicate) and a missing id (ErrNotFound).
func (s *TemplateService) Update(t *model.Template) (*model.Template, int, error) {
	if t == nil || t.ID == "" {
		return nil, response.ErrInvalidParam, errors.New("service: template id required")
	}
	if t.Name == "" {
		return nil, response.ErrInvalidParam, errors.New("service: template name required")
	}

	// Existence check: the store's Update may be a blind overwrite, so verify
	// the record exists first to return ErrNotFound semantics.
	existing, err := s.store.GetByID(t.ID)
	if err != nil {
		if errors.Is(err, model.ErrNotFound) {
			return nil, response.ErrNotFound, errors.New("service: template not found")
		}
		return nil, response.ErrInternal, fmt.Errorf("service: get template for update: %w", err)
	}

	// Duplicate-name check excluding the record itself.
	if dup, err := s.store.GetByName(t.Name); err == nil && dup != nil && dup.ID != t.ID {
		return nil, response.ErrTemplateNameDuplicate, errors.New("service: template name already exists")
	} else if err != nil && !errors.Is(err, model.ErrNotFound) {
		return nil, response.ErrInternal, fmt.Errorf("service: check duplicate name: %w", err)
	}

	// Preserve CreatedAt from the stored record; only mutable fields come from t.
	t.CreatedAt = existing.CreatedAt
	t.UpdatedAt = time.Now()

	if err := s.store.Update(t); err != nil {
		if errors.Is(err, model.ErrDuplicate) {
			return nil, response.ErrTemplateNameDuplicate, err
		}
		return nil, response.ErrInternal, fmt.Errorf("service: update template: %w", err)
	}
	return t, response.ErrOK, nil
}

// Delete removes the template with the given id. The store's Delete is
// idempotent in this codebase, so we check existence first to surface
// ErrNotFound for an unknown id.
func (s *TemplateService) Delete(id string) (int, error) {
	if id == "" {
		return response.ErrInvalidParam, errors.New("service: template id required")
	}
	if _, err := s.store.GetByID(id); err != nil {
		if errors.Is(err, model.ErrNotFound) {
			return response.ErrNotFound, errors.New("service: template not found")
		}
		return response.ErrInternal, fmt.Errorf("service: get template for delete: %w", err)
	}
	if err := s.store.Delete(id); err != nil {
		return response.ErrInternal, fmt.Errorf("service: delete template: %w", err)
	}
	return response.ErrOK, nil
}