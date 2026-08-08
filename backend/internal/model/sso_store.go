package model

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// SsoTokenStoreJSON is the JSON file-backed implementation of SsoTokenStore.
// Tokens are kept in memory keyed by ID; every mutation is persisted
// atomically. All methods are safe for concurrent use.
type SsoTokenStoreJSON struct {
	mu   sync.RWMutex
	path string
	byID map[string]*SsoToken
}

// NewSsoTokenStore opens (or creates) the SSO token store rooted at dir. The
// data file is dir/sso_tokens.json. A missing file is initialized as an empty
// collection and written to disk.
func NewSsoTokenStore(dir string) (*SsoTokenStoreJSON, error) {
	s := &SsoTokenStoreJSON{
		path: filepath.Join(dir, "sso_tokens.json"),
		byID: make(map[string]*SsoToken),
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *SsoTokenStoreJSON) load() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return s.persistLocked()
		}
		return fmt.Errorf("model: read sso_tokens %s: %w", s.path, err)
	}
	if len(data) == 0 {
		return nil
	}
	var ts []*SsoToken
	if err := json.Unmarshal(data, &ts); err != nil {
		return fmt.Errorf("model: decode sso_tokens %s: %w", s.path, err)
	}
	for _, t := range ts {
		cp := *t
		s.byID[cp.ID] = &cp
	}
	return nil
}

func (s *SsoTokenStoreJSON) persistLocked() error {
	ts := make([]*SsoToken, 0, len(s.byID))
	for _, t := range s.byID {
		ts = append(ts, t)
	}
	return atomicWriteJSON(s.path, ts)
}

// Create stores t. A token with the same ID already present yields
// ErrDuplicate. (Username is not required to be unique — an admin may mint
// multiple tokens for the same user.)
func (s *SsoTokenStoreJSON) Create(t *SsoToken) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byID[t.ID]; ok {
		return ErrDuplicate
	}
	cp := *t
	s.byID[cp.ID] = &cp
	return s.persistLocked()
}

// GetByID returns the token with the given ID, or ErrNotFound.
func (s *SsoTokenStoreJSON) GetByID(id string) (*SsoToken, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.byID[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *t
	return &cp, nil
}

// MarkUsed sets Used=true on the token with the given id and persists. It is
// idempotent: marking an already-used token succeeds. ErrNotFound is returned
// when the id is unknown.
func (s *SsoTokenStoreJSON) MarkUsed(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.byID[id]
	if !ok {
		return ErrNotFound
	}
	if t.Used {
		// Idempotent: nothing to persist, but not an error.
		return nil
	}
	t.Used = true
	return s.persistLocked()
}

// Consume atomically marks the token with the given id as used and returns a
// copy, iff the token exists, is unused, and has not expired. The used-flag
// check, the expiry check, the in-memory mutation and the on-disk persistence
// all happen under the single write lock s.mu, so concurrent Consume calls for
// the same id can never both observe Used=false (defeating the TOCTOU replay
// race that GetByID + MarkUsed would otherwise have). On any failure — not
// found, already used, expired, or a persistence error — it returns (nil,
// false) and leaves the in-memory state unchanged. The returned token is a
// copy so the caller cannot mutate the store's internal record.
func (s *SsoTokenStoreJSON) Consume(id string) (*SsoToken, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.byID[id]
	if !ok {
		return nil, false
	}
	if t.Used {
		return nil, false
	}
	if time.Now().After(t.ExpiresAt) {
		return nil, false
	}
	t.Used = true
	if err := s.persistLocked(); err != nil {
		// Persistence failed: revert the in-memory mutation to keep the
		// in-memory and on-disk states consistent, and report failure. We do
		// NOT surface the token as consumed — callers treat (nil, false) as
		// "login failed" and the token remains usable on a retry if the I/O
		// error was transient.
		t.Used = false
		return nil, false
	}
	cp := *t
	return &cp, true
}

// DeleteExpired removes every token whose ExpiresAt is strictly before now.
// Returns nil when there is nothing to remove. It is safe to call
// concurrently with MarkUsed / GetByID.
func (s *SsoTokenStoreJSON) DeleteExpired() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	removed := false
	for id, t := range s.byID {
		if t.ExpiresAt.Before(now) {
			delete(s.byID, id)
			removed = true
		}
	}
	if !removed {
		return nil
	}
	return s.persistLocked()
}
