package model

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// TemplateStoreJSON is the JSON file-backed implementation of TemplateStore.
// It holds the whole collection in memory keyed by ID; GetByName scans the
// in-memory map (the collection is small). Every mutation is persisted
// atomically. All methods are safe for concurrent use.
type TemplateStoreJSON struct {
	mu   sync.RWMutex
	path string
	byID map[string]*Template
}

// NewTemplateStore opens (or creates) the template store rooted at dir. The
// data file is dir/templates.json. A missing file is initialized as an empty
// collection and written to disk.
func NewTemplateStore(dir string) (*TemplateStoreJSON, error) {
	s := &TemplateStoreJSON{
		path: filepath.Join(dir, "templates.json"),
		byID: make(map[string]*Template),
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *TemplateStoreJSON) load() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return s.persistLocked()
		}
		return fmt.Errorf("model: read templates %s: %w", s.path, err)
	}
	if len(data) == 0 {
		return nil
	}
	var ts []*Template
	if err := json.Unmarshal(data, &ts); err != nil {
		return fmt.Errorf("model: decode templates %s: %w", s.path, err)
	}
	for _, t := range ts {
		cp := *t
		s.byID[cp.ID] = &cp
	}
	return nil
}

func (s *TemplateStoreJSON) persistLocked() error {
	ts := make([]*Template, 0, len(s.byID))
	for _, t := range s.byID {
		ts = append(ts, t)
	}
	return atomicWriteJSON(s.path, ts)
}

// Create stores t. A template with the same ID or Name yields ErrDuplicate.
func (s *TemplateStoreJSON) Create(t *Template) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byID[t.ID]; ok {
		return ErrDuplicate
	}
	if existing := s.findByNameLocked(t.Name); existing != nil {
		return ErrDuplicate
	}
	cp := *t
	s.byID[cp.ID] = &cp
	return s.persistLocked()
}

// GetByID returns the template with the given ID, or ErrNotFound.
func (s *TemplateStoreJSON) GetByID(id string) (*Template, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.byID[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *t
	return &cp, nil
}

// GetByName returns the template with the given (case-sensitive) name, or
// ErrNotFound. It scans the in-memory map — the collection is expected to be
// small.
func (s *TemplateStoreJSON) GetByName(name string) (*Template, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t := s.findByNameLocked(name)
	if t == nil {
		return nil, ErrNotFound
	}
	cp := *t
	return &cp, nil
}

// findByNameLocked scans for a template matching name. The caller must hold
// the (read or write) lock. Returns nil when absent.
func (s *TemplateStoreJSON) findByNameLocked(name string) *Template {
	for _, t := range s.byID {
		if t.Name == name {
			return t
		}
	}
	return nil
}

// List returns all templates. The order is unspecified.
func (s *TemplateStoreJSON) List() ([]*Template, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Template, 0, len(s.byID))
	for _, t := range s.byID {
		cp := *t
		out = append(out, &cp)
	}
	return out, nil
}

// Update overwrites the stored template with the same ID. It enforces name
// uniqueness: if another template already owns t.Name, ErrDuplicate is
// returned. ErrNotFound is returned when no template has the given ID. The
// caller is responsible for setting UpdatedAt.
func (s *TemplateStoreJSON) Update(t *Template) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byID[t.ID]; !ok {
		return ErrNotFound
	}
	// Enforce name uniqueness against a DIFFERENT id.
	if owner := s.findByNameLocked(t.Name); owner != nil && owner.ID != t.ID {
		return ErrDuplicate
	}
	cp := *t
	s.byID[cp.ID] = &cp
	return s.persistLocked()
}

// Delete removes the template with the given ID. Deleting a non-existent id
// is a no-op (returns nil) to keep Delete idempotent.
func (s *TemplateStoreJSON) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byID[id]; !ok {
		return nil
	}
	delete(s.byID, id)
	return s.persistLocked()
}
