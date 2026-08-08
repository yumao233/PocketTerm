package model

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// UserStoreJSON is the JSON file-backed implementation of UserStore. It keeps
// the whole collection in memory (keyed by ID, with a username index) and
// reflects every mutation to disk atomically. All methods are safe for
// concurrent use.
type UserStoreJSON struct {
	mu     sync.RWMutex
	path   string
	byID   map[string]*User
	byName map[string]*User
}

// NewUserStore opens (or creates) the user store rooted at dir. The data file
// is dir/users.json. If the file does not exist it is created with an empty
// collection; otherwise its contents are loaded into memory.
func NewUserStore(dir string) (*UserStoreJSON, error) {
	s := &UserStoreJSON{
		path:   filepath.Join(dir, "users.json"),
		byID:   make(map[string]*User),
		byName: make(map[string]*User),
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

// load reads the data file. A missing file seeds an empty collection and
// writes the initial (empty) file so subsequent reloads have a stable path.
func (s *UserStoreJSON) load() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			// Initialize empty collection and persist so the path exists.
			return s.persistLocked()
		}
		return fmt.Errorf("model: read users %s: %w", s.path, err)
	}
	if len(data) == 0 {
		return nil
	}
	var users []*User
	if err := json.Unmarshal(data, &users); err != nil {
		return fmt.Errorf("model: decode users %s: %w", s.path, err)
	}
	for _, u := range users {
		// Store copies to insulate the in-memory map from caller mutation.
		cp := *u
		s.byID[cp.ID] = &cp
		s.byName[cp.Username] = &cp
	}
	return nil
}

// persistLocked writes the current collection to disk. The caller MUST hold
// s.mu (write lock) — this is the single throw-point for all on-disk writes
// so that the lock is always held across mutate+persist.
func (s *UserStoreJSON) persistLocked() error {
	users := make([]*User, 0, len(s.byID))
	for _, u := range s.byID {
		users = append(users, u)
	}
	return atomicWriteJSON(s.path, users)
}

// Create stores u. A user with the same ID or Username already present yields
// ErrDuplicate.
func (s *UserStoreJSON) Create(u *User) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byID[u.ID]; ok {
		return ErrDuplicate
	}
	if _, ok := s.byName[u.Username]; ok {
		return ErrDuplicate
	}
	cp := *u
	s.byID[cp.ID] = &cp
	s.byName[cp.Username] = &cp
	return s.persistLocked()
}

// GetByUsername returns the user with the given username, or ErrNotFound.
func (s *UserStoreJSON) GetByUsername(name string) (*User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, ok := s.byName[name]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *u
	return &cp, nil
}

// GetByID returns the user with the given ID, or ErrNotFound.
func (s *UserStoreJSON) GetByID(id string) (*User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, ok := s.byID[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *u
	return &cp, nil
}

// Count returns the number of stored users.
func (s *UserStoreJSON) Count() (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.byID), nil
}

// List returns all users. The order is unspecified.
func (s *UserStoreJSON) List() ([]*User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*User, 0, len(s.byID))
	for _, u := range s.byID {
		cp := *u
		out = append(out, &cp)
	}
	return out, nil
}
