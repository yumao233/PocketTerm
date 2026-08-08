package model

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// --- helpers --------------------------------------------------------------

func newTestUser(name string) *User {
	return &User{
		ID:        uuid.NewString(),
		Username:  name,
		IsAdmin:   false,
		CreatedAt: time.Now().UTC(),
	}
}

func newTestTemplate(name string) *Template {
	now := time.Now().UTC()
	return &Template{
		ID:        uuid.NewString(),
		Name:      name,
		Command:   "bash",
		Args:      []string{},
		Cwd:       "/tmp",
		Env:       []KV{},
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// --- UserStore ------------------------------------------------------------

// TestUserStore_ConcurrentCreate launches 10 goroutines each creating a
// distinct user. After joining, the store must hold exactly 10 users with no
// duplicate IDs. Run with -race to detect data races.
func TestUserStore_ConcurrentCreate(t *testing.T) {
	dir := t.TempDir()
	store, err := NewUserStore(dir)
	if err != nil {
		t.Fatalf("NewUserStore: %v", err)
	}

	const n = 10
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			u := newTestUser(fmt.Sprintf("u%d", i))
			errs <- store.Create(u)
		}(i)
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Create returned error: %v", err)
		}
	}

	got, err := store.Count()
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if got != n {
		t.Fatalf("Count = %d, want %d", got, n)
	}

	// No duplicate IDs: every u0..u9 must be retrievable by name and by id.
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("u%d", i)
		byName, err := store.GetByUsername(name)
		if err != nil {
			t.Fatalf("GetByUsername(%q): %v", name, err)
		}
		byID, err := store.GetByID(byName.ID)
		if err != nil {
			t.Fatalf("GetByID(%q): %v", byName.ID, err)
		}
		if byID.Username != name {
			t.Fatalf("username mismatch: got %q want %q", byID.Username, name)
		}
	}
}

// TestUserStore_DuplicateUsername verifies that a second Create with an
// already-used username yields ErrDuplicate and does not mutate the store.
func TestUserStore_DuplicateUsername(t *testing.T) {
	dir := t.TempDir()
	store, err := NewUserStore(dir)
	if err != nil {
		t.Fatalf("NewUserStore: %v", err)
	}
	if err := store.Create(newTestUser("dup")); err != nil {
		t.Fatalf("first Create: %v", err)
	}

	err = store.Create(newTestUser("dup"))
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("second Create err = %v, want ErrDuplicate", err)
	}

	count, _ := store.Count()
	if count != 1 {
		t.Fatalf("Count = %d, want 1 (duplicate must not be stored)", count)
	}
}

// TestReloadPersists verifies that a record committed by one store instance
// is observable by a fresh instance opened on the same directory — i.e. the
// atomic write really persisted to disk.
func TestReloadPersists(t *testing.T) {
	dir := t.TempDir()
	s1, err := NewUserStore(dir)
	if err != nil {
		t.Fatalf("NewUserStore s1: %v", err)
	}
	u := newTestUser("persist-user")
	if err := s1.Create(u); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Open a brand-new instance on the same dir; the file must contain the user.
	s2, err := NewUserStore(dir)
	if err != nil {
		t.Fatalf("NewUserStore s2: %v", err)
	}
	got, err := s2.GetByID(u.ID)
	if err != nil {
		t.Fatalf("s2.GetByID: %v", err)
	}
	if got.Username != u.Username {
		t.Fatalf("reloaded username = %q, want %q", got.Username, u.Username)
	}
}

// --- TemplateStore --------------------------------------------------------

// TestTemplateStore_CRUD exercises Create, GetByID, GetByName, Update (name +
// UpdatedAt change) and Delete (then GetByID -> ErrNotFound).
func TestTemplateStore_CRUD(t *testing.T) {
	dir := t.TempDir()
	store, err := NewTemplateStore(dir)
	if err != nil {
		t.Fatalf("NewTemplateStore: %v", err)
	}

	tpl := newTestTemplate("bash-tpl")
	if err := store.Create(tpl); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := store.GetByID(tpl.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Name != tpl.Name {
		t.Fatalf("GetByID name = %q, want %q", got.Name, tpl.Name)
	}

	byName, err := store.GetByName("bash-tpl")
	if err != nil {
		t.Fatalf("GetByName: %v", err)
	}
	if byName.ID != tpl.ID {
		t.Fatalf("GetByName id = %q, want %q", byName.ID, tpl.ID)
	}
	if _, err := store.GetByName("missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetByName(missing) err = %v, want ErrNotFound", err)
	}

	// Update: change Name and UpdatedAt.
	updated := *got
	updated.Name = "bash-tpl-renamed"
	updated.UpdatedAt = got.UpdatedAt.Add(time.Hour)
	if err := store.Update(&updated); err != nil {
		t.Fatalf("Update: %v", err)
	}
	after, err := store.GetByID(tpl.ID)
	if err != nil {
		t.Fatalf("GetByID after Update: %v", err)
	}
	if after.Name != "bash-tpl-renamed" {
		t.Fatalf("after Update name = %q, want %q", after.Name, "bash-tpl-renamed")
	}
	if !after.UpdatedAt.Equal(updated.UpdatedAt) {
		t.Fatalf("after Update UpdatedAt = %v, want %v", after.UpdatedAt, updated.UpdatedAt)
	}
	// Old name is now free; new name resolves.
	if _, err := store.GetByName("bash-tpl"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetByName(old) after rename err = %v, want ErrNotFound", err)
	}
	if _, err := store.GetByName("bash-tpl-renamed"); err != nil {
		t.Fatalf("GetByName(new) after rename: %v", err)
	}

	// Delete: then GetByID -> ErrNotFound.
	if err := store.Delete(tpl.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := store.GetByID(tpl.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetByID after Delete err = %v, want ErrNotFound", err)
	}
}

// TestTemplateStore_DuplicateName verifies Create rejects a duplicate template
// name and Update rejects stealing an existing name.
func TestTemplateStore_DuplicateName(t *testing.T) {
	dir := t.TempDir()
	store, err := NewTemplateStore(dir)
	if err != nil {
		t.Fatalf("NewTemplateStore: %v", err)
	}
	if err := store.Create(newTestTemplate("a")); err != nil {
		t.Fatalf("Create a: %v", err)
	}
	if err := store.Create(newTestTemplate("a")); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("Create a (dup) err = %v, want ErrDuplicate", err)
	}

	// Stealing a name via Update on a different template must fail.
	b := newTestTemplate("b")
	if err := store.Create(b); err != nil {
		t.Fatalf("Create b: %v", err)
	}
	b.Name = "a"
	if err := store.Update(b); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("Update stealing name err = %v, want ErrDuplicate", err)
	}
	// Renaming b back to itself is allowed (no duplicate-of-other).
	b.Name = "b"
	if err := store.Update(b); err != nil {
		t.Fatalf("Update self name err = %v", err)
	}
}

// --- SsoTokenStore --------------------------------------------------------

// TestSsoTokenStore_MarkUsed creates a token, marks it used, then opens a
// fresh store on the same dir and asserts Used persisted. It then creates an
// expired token and verifies DeleteExpired removes only that one.
func TestSsoTokenStore_MarkUsed(t *testing.T) {
	dir := t.TempDir()
	store, err := NewSsoTokenStore(dir)
	if err != nil {
		t.Fatalf("NewSsoTokenStore: %v", err)
	}

	now := time.Now().UTC()
	tok := &SsoToken{
		ID:        uuid.NewString(),
		Username:  "sso-user",
		Password:  "one-time-pw",
		Used:      false,
		CreatedAt: now,
		ExpiresAt: now.Add(time.Hour),
	}
	if err := store.Create(tok); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := store.MarkUsed(tok.ID); err != nil {
		t.Fatalf("MarkUsed: %v", err)
	}

	// Reload from the same dir; Used must have been persisted.
	store2, err := NewSsoTokenStore(dir)
	if err != nil {
		t.Fatalf("NewSsoTokenStore s2: %v", err)
	}
	reloaded, err := store2.GetByID(tok.ID)
	if err != nil {
		t.Fatalf("GetByID after reload: %v", err)
	}
	if !reloaded.Used {
		t.Fatalf("after reload Used = false, want true (not persisted)")
	}

	// MarkUsed is idempotent.
	if err := store2.MarkUsed(tok.ID); err != nil {
		t.Fatalf("idempotent MarkUsed: %v", err)
	}

	// Create an already-expired token; DeleteExpired must remove it but keep
	// the still-valid (but used) token above.
	expired := &SsoToken{
		ID:        uuid.NewString(),
		Username:  "expired-user",
		Password:  "pw",
		Used:      false,
		CreatedAt: now.Add(-2 * time.Hour),
		ExpiresAt: now.Add(-time.Hour), // in the past
	}
	if err := store2.Create(expired); err != nil {
		t.Fatalf("Create expired: %v", err)
	}
	if err := store2.DeleteExpired(); err != nil {
		t.Fatalf("DeleteExpired: %v", err)
	}
	if _, err := store2.GetByID(expired.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired token still present after DeleteExpired: err = %v, want ErrNotFound", err)
	}
	if _, err := store2.GetByID(tok.ID); err != nil {
		t.Fatalf("valid token removed by DeleteExpired: %v", err)
	}
}
