package model

import "errors"

// Sentinel errors returned by store implementations. Callers must use
// errors.Is to check for them (the stores never wrap these two) so that
// service-layer logic can branch on the failure mode.
var (
	// ErrNotFound is returned by Get* methods when no record matches the
	// supplied key.
	ErrNotFound = errors.New("record not found")

	// ErrDuplicate is returned by Create/Update when a unique constraint
	// (e.g. user username, template name) would be violated.
	ErrDuplicate = errors.New("duplicate record")
)

// UserStore is the storage boundary for User records. Implementations must be
// safe for concurrent use.
type UserStore interface {
	Create(u *User) error
	GetByUsername(name string) (*User, error)
	GetByID(id string) (*User, error)
	Count() (int, error)
	List() ([]*User, error)
}

// TemplateStore is the storage boundary for Template records. GetByName is
// used by the service layer to enforce name uniqueness. Implementations must
// be safe for concurrent use.
type TemplateStore interface {
	Create(t *Template) error
	GetByID(id string) (*Template, error)
	GetByName(name string) (*Template, error)
	List() ([]*Template, error)
	Update(t *Template) error
	Delete(id string) error
}

// SsoTokenStore is the storage boundary for SsoToken records. MarkUsed marks a
// token as consumed (one-time use); DeleteExpired purges tokens whose
// ExpiresAt is in the past. Consume is the atomic consume primitive used by the
// SSO login flow to defeat replay races. Implementations must be safe for
// concurrent use.
type SsoTokenStore interface {
	Create(t *SsoToken) error
	GetByID(id string) (*SsoToken, error)
	MarkUsed(id string) error
	DeleteExpired() error
	// Consume atomically marks the token used and returns it, iff it is unused
	// and not expired. Returns (token, true) on success, (nil, false) if the
	// token is not found, already used, or expired. The check-and-set and the
	// persistence all happen under a single write lock, so two concurrent
	// Consume calls for the same id can never both succeed (TOCTOU-safe). The
	// returned token is a copy; callers must not mutate the store's internal
	// record through it.
	Consume(id string) (*SsoToken, bool)
}
