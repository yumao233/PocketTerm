package service

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wangkun/webshell/internal/auth"
	"github.com/wangkun/webshell/internal/model"
	"github.com/wangkun/webshell/internal/pty"
	sshserver "github.com/wangkun/webshell/internal/ssh"
	"github.com/wangkun/webshell/pkg/response"
)

// ---------------------------------------------------------------------------
// Shared test fixtures: temp-dir real stores + a real local SSH server (inlined
// minimal version of the pty package's helper so we do not import test helpers
// across packages).
// ---------------------------------------------------------------------------

const (
	testJwtSecret = "service-test-secret"
	testJwtTTL    = time.Hour
)

// startTestSSHServer mirrors internal/pty.manager_test.go's helper. It builds a
// local SSH server bound to an ephemeral loopback port, starts it, waits for the
// listener to bind, and returns addr + passcode + cleanup.
func startTestSSHServer(t *testing.T) (srv *sshserver.Server, addr, passcode string, cleanup func()) {
	t.Helper()
	passcode = "service-test-passcode-" + t.Name()
	srv, err := sshserver.NewServer(sshserver.Config{
		Listen:   "127.0.0.1:0",
		Passcode: passcode,
	})
	if err != nil {
		t.Fatalf("sshserver.NewServer: %v", err)
	}
	startErr := make(chan error, 1)
	go func() { startErr <- srv.Start() }()

	deadline := time.Now().Add(2 * time.Second)
	var bound string
	for time.Now().Before(deadline) {
		if a := srv.Addr(); a != nil {
			bound = a.String()
			break
		}
		select {
		case err := <-startErr:
			t.Fatalf("ssh server exited before binding: %v", err)
		default:
		}
		time.Sleep(5 * time.Millisecond)
	}
	if bound == "" {
		t.Fatalf("ssh server did not bind within 2s")
	}
	return srv, bound, passcode, func() { _ = srv.Close() }
}

// newAuthSvc builds an AuthService backed by fresh temp-dir stores.
func newAuthSvc(t *testing.T) *AuthService {
	t.Helper()
	dir := t.TempDir()
	users, err := model.NewUserStore(dir)
	if err != nil {
		t.Fatalf("NewUserStore: %v", err)
	}
	tokens, err := model.NewSsoTokenStore(dir)
	if err != nil {
		t.Fatalf("NewSsoTokenStore: %v", err)
	}
	return NewAuthService(users, tokens, testJwtSecret, testJwtTTL)
}

// newTplSvc builds a TemplateService over a fresh temp-dir store.
func newTplSvc(t *testing.T) *TemplateService {
	t.Helper()
	dir := t.TempDir()
	store, err := model.NewTemplateStore(dir)
	if err != nil {
		t.Fatalf("NewTemplateStore: %v", err)
	}
	return NewTemplateService(store)
}

// codeIs asserts the returned business code equals want.
func codeIs(t *testing.T, got, want int, label string) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: code = %d, want %d", label, got, want)
	}
}

// errIs asserts err != nil (the test only cares that an error was returned).
func errNonNil(t *testing.T, err error, label string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: expected non-nil error, got nil", label)
	}
}

// ---------------------------------------------------------------------------
// AuthService tests
// ---------------------------------------------------------------------------

// TestAuth_CheckSetup verifies CheckSetup is true on an empty store and flips
// to false after the first admin is registered.
func TestAuth_CheckSetup(t *testing.T) {
	s := newAuthSvc(t)

	empty, err := s.CheckSetup()
	if err != nil {
		t.Fatalf("CheckSetup (empty): %v", err)
	}
	if !empty {
		t.Fatalf("CheckSetup on empty store = false; want true")
	}

	if _, _, code, err := s.Setup("admin", "secret123"); err != nil || code != response.ErrOK {
		t.Fatalf("Setup: code=%d err=%v", code, err)
	}
	notEmpty, err := s.CheckSetup()
	if err != nil {
		t.Fatalf("CheckSetup (after setup): %v", err)
	}
	if notEmpty {
		t.Fatalf("CheckSetup after setup = true; want false")
	}
}

// TestAuth_SetupAlreadyInitialized: a second Setup after the first must return
// ErrAlreadyInitialized.
func TestAuth_SetupAlreadyInitialized(t *testing.T) {
	s := newAuthSvc(t)
	if _, _, _, err := s.Setup("admin", "secret123"); err != nil {
		t.Fatalf("first Setup: %v", err)
	}
	_, _, code, err := s.Setup("other", "secret456")
	errNonNil(t, err, "second Setup")
	codeIs(t, code, response.ErrAlreadyInitialized, "second Setup")
}

// TestAuth_SetupInvalidParam: empty username and short password reject with
// ErrInvalidParam.
func TestAuth_SetupInvalidParam(t *testing.T) {
	s := newAuthSvc(t)

	_, _, code, err := s.Setup("", "secret123")
	errNonNil(t, err, "empty username")
	codeIs(t, code, response.ErrInvalidParam, "empty username")

	_, _, code, err = s.Setup("admin", "12345")
	errNonNil(t, err, "short password")
	codeIs(t, code, response.ErrInvalidParam, "short password")
}

// TestAuth_SetupSanitizesHash: the returned user must NOT carry the password
// hash.
func TestAuth_SetupSanitizesHash(t *testing.T) {
	s := newAuthSvc(t)
	u, _, _, err := s.Setup("admin", "secret123")
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if u.PasswordHash != "" {
		t.Fatalf("returned user leaks PasswordHash: %q", u.PasswordHash)
	}
}

// TestAuth_LoginSuccess: correct credentials return a parseable JWT whose
// claims match the user.
func TestAuth_LoginSuccess(t *testing.T) {
	s := newAuthSvc(t)
	if _, _, _, err := s.Setup("admin", "secret123"); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	u, token, code, err := s.Login("admin", "secret123")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	codeIs(t, code, response.ErrOK, "Login")
	if u.PasswordHash != "" {
		t.Fatalf("Login leaks PasswordHash")
	}
	claims, err := auth.Parse(testJwtSecret, token)
	if err != nil {
		t.Fatalf("Parse jwt: %v", err)
	}
	if claims.Username != "admin" {
		t.Fatalf("claims.Username = %q, want admin", claims.Username)
	}
	if claims.UserID != u.ID {
		t.Fatalf("claims.UserID = %q, want %q", claims.UserID, u.ID)
	}
}

// TestAuth_LoginNotInitialized: logging in before setup returns
// ErrNotInitialized.
func TestAuth_LoginNotInitialized(t *testing.T) {
	s := newAuthSvc(t)
	_, _, code, err := s.Login("admin", "secret123")
	errNonNil(t, err, "Login before setup")
	codeIs(t, code, response.ErrNotInitialized, "Login before setup")
}

// TestAuth_LoginWrongPassword: a wrong password returns ErrInvalidCredentials.
func TestAuth_LoginWrongPassword(t *testing.T) {
	s := newAuthSvc(t)
	if _, _, _, err := s.Setup("admin", "secret123"); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	_, _, code, err := s.Login("admin", "WRONG")
	errNonNil(t, err, "Login wrong pw")
	codeIs(t, code, response.ErrInvalidCredentials, "Login wrong pw")
}

// TestAuth_LoginUnknownUser: an unknown username also returns
// ErrInvalidCredentials (no user enumeration).
func TestAuth_LoginUnknownUser(t *testing.T) {
	s := newAuthSvc(t)
	if _, _, _, err := s.Setup("admin", "secret123"); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	_, _, code, err := s.Login("ghost", "secret123")
	errNonNil(t, err, "Login unknown user")
	codeIs(t, code, response.ErrInvalidCredentials, "Login unknown user")
}

// TestAuth_SsoLoginFlow: GenSsoToken -> SsoLogin succeeds and marks the token
// used; a second SsoLogin on the same token fails.
func TestAuth_SsoLoginFlow(t *testing.T) {
	s := newAuthSvc(t)
	if _, _, _, err := s.Setup("admin", "secret123"); err != nil {
		t.Fatalf("Setup: %v", err)
	}

	tok, code, err := s.GenSsoToken("admin")
	if err != nil {
		t.Fatalf("GenSsoToken: %v", err)
	}
	codeIs(t, code, response.ErrOK, "GenSsoToken")
	if tok.Password == "" {
		t.Fatalf("GenSsoToken returned empty password")
	}

	u, jwt, code, err := s.SsoLogin(tok.ID, tok.Password)
	if err != nil {
		t.Fatalf("SsoLogin: %v", err)
	}
	codeIs(t, code, response.ErrOK, "SsoLogin")
	if u.PasswordHash != "" {
		t.Fatalf("SsoLogin leaks PasswordHash")
	}
	if _, perr := auth.Parse(testJwtSecret, jwt); perr != nil {
		t.Fatalf("SsoLogin jwt unparseable: %v", perr)
	}

	// Second use must fail with ErrInvalidCredentials (token now used).
	_, _, code, err = s.SsoLogin(tok.ID, tok.Password)
	errNonNil(t, err, "second SsoLogin")
	codeIs(t, code, response.ErrInvalidCredentials, "second SsoLogin")
}

// TestAuth_SsoLoginUnknownToken: an unknown uuid returns ErrInvalidCredentials.
func TestAuth_SsoLoginUnknownToken(t *testing.T) {
	s := newAuthSvc(t)
	if _, _, _, err := s.Setup("admin", "secret123"); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	_, _, code, err := s.SsoLogin("no-such-uuid", "whatever")
	errNonNil(t, err, "SsoLogin unknown uuid")
	codeIs(t, code, response.ErrInvalidCredentials, "SsoLogin unknown uuid")
}

// TestAuth_SsoLoginWrongPassword: correct uuid but wrong password fails.
func TestAuth_SsoLoginWrongPassword(t *testing.T) {
	s := newAuthSvc(t)
	if _, _, _, err := s.Setup("admin", "secret123"); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	tok, _, err := s.GenSsoToken("admin")
	if err != nil {
		t.Fatalf("GenSsoToken: %v", err)
	}
	_, _, code, err := s.SsoLogin(tok.ID, "WRONG")
	errNonNil(t, err, "SsoLogin wrong pw")
	codeIs(t, code, response.ErrInvalidCredentials, "SsoLogin wrong pw")
}

// TestAuth_SsoLoginExpiredDirect builds an expired SSO token directly through
// the concrete JSON store (the service never creates expired tokens), then
// verifies SsoLogin rejects it.
func TestAuth_SsoLoginExpiredDirect(t *testing.T) {
	dir := t.TempDir()
	users, err := model.NewUserStore(dir)
	if err != nil {
		t.Fatalf("NewUserStore: %v", err)
	}
	tokens, err := model.NewSsoTokenStore(dir)
	if err != nil {
		t.Fatalf("NewSsoTokenStore: %v", err)
	}
	s := NewAuthService(users, tokens, testJwtSecret, testJwtTTL)
	if _, _, _, err := s.Setup("admin", "secret123"); err != nil {
		t.Fatalf("Setup: %v", err)
	}

	now := time.Now()
	expired := &model.SsoToken{
		ID:        "expired-token-id",
		Username:  "admin",
		Password:  "expiredpw",
		Used:      false,
		CreatedAt: now.Add(-2 * time.Minute),
		ExpiresAt: now.Add(-time.Minute),
	}
	if err := tokens.Create(expired); err != nil {
		t.Fatalf("tokens.Create expired: %v", err)
	}

	_, _, code, err := s.SsoLogin(expired.ID, expired.Password)
	errNonNil(t, err, "SsoLogin expired")
	codeIs(t, code, response.ErrInvalidCredentials, "SsoLogin expired")
}

// TestSsoLogin_ConcurrentReplayRejected is the TOCTOU replay guard: fire N
// goroutines that all call SsoLogin with the SAME one-time token at once.
// Exactly ONE must succeed (the atomic Consume wins the race) and the rest must
// get ErrInvalidCredentials. This catches the original GetByID + MarkUsed race
// where two goroutines could both observe Used=false before either persisted.
// Run with -race to also catch any data race in Consume itself.
func TestSsoLogin_ConcurrentReplayRejected(t *testing.T) {
	s := newAuthSvc(t)
	if _, _, _, err := s.Setup("admin", "secret123"); err != nil {
		t.Fatalf("Setup: %v", err)
	}

	tok, code, err := s.GenSsoToken("admin")
	if err != nil {
		t.Fatalf("GenSsoToken: %v", err)
	}
	codeIs(t, code, response.ErrOK, "GenSsoToken")

	const n = 5
	var wg sync.WaitGroup
	var successes int32 // atomic counter of ErrOK returns
	wg.Add(n)
	// Tiny barrier so all goroutines race the Consume lock together rather than
	// serializing on staggered scheduling.
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			<-start
			_, _, c, _ := s.SsoLogin(tok.ID, tok.Password)
			if c == response.ErrOK {
				atomic.AddInt32(&successes, 1)
			}
		}()
	}
	close(start)
	wg.Wait()

	if got := atomic.LoadInt32(&successes); got != 1 {
		t.Fatalf("concurrent SsoLogin replay: %d successes, want exactly 1", got)
	}
}

// TestSsoTokenStore_Consume_ConcurrentExactlyOne verifies the store-layer
// Consume primitive directly (service-free): N concurrent Consume calls on the
// same fresh token must yield exactly one (token, true) and the rest (nil,
// false). This is the lower-level guarantee the service test builds on.
func TestSsoTokenStore_Consume_ConcurrentExactlyOne(t *testing.T) {
	dir := t.TempDir()
	store, err := model.NewSsoTokenStore(dir)
	if err != nil {
		t.Fatalf("NewSsoTokenStore: %v", err)
	}
	now := time.Now()
	tok := &model.SsoToken{
		ID:        "race-token",
		Username:  "admin",
		Password:  "pw",
		Used:      false,
		CreatedAt: now,
		ExpiresAt: now.Add(time.Hour),
	}
	if err := store.Create(tok); err != nil {
		t.Fatalf("Create: %v", err)
	}

	const n = 8
	var wg sync.WaitGroup
	var success int32
	start := make(chan struct{})
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			<-start
			if _, ok := store.Consume(tok.ID); ok {
				atomic.AddInt32(&success, 1)
			}
		}()
	}
	close(start)
	wg.Wait()

	if got := atomic.LoadInt32(&success); got != 1 {
		t.Fatalf("Consume concurrent: %d successes, want exactly 1", got)
	}
	// After the race, the token is used; a final Consume must fail.
	if _, ok := store.Consume(tok.ID); ok {
		t.Fatalf("Consume after concurrent race still succeeded; want false (already used)")
	}
}

// TestAuth_GenSsoTokenUnknownUser: minting a token for a missing user returns
// ErrNotFound.
func TestAuth_GenSsoTokenUnknownUser(t *testing.T) {
	s := newAuthSvc(t)
	if _, _, _, err := s.Setup("admin", "secret123"); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	_, code, err := s.GenSsoToken("ghost")
	errNonNil(t, err, "GenSsoToken unknown user")
	codeIs(t, code, response.ErrNotFound, "GenSsoToken unknown user")
}

// ---------------------------------------------------------------------------
// TemplateService tests
// ---------------------------------------------------------------------------

// TestTemplate_Create verifies Create fills ID/timestamps, defaults Command to
// bash, and persists the record.
func TestTemplate_Create(t *testing.T) {
	s := newTplSvc(t)
	tpl := &model.Template{Name: "bash-template"}
	got, code, err := s.Create(tpl)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	codeIs(t, code, response.ErrOK, "Create")
	if got.ID == "" {
		t.Fatalf("Create did not fill ID")
	}
	if got.Command != "bash" {
		t.Fatalf("Create default Command = %q, want bash", got.Command)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Fatalf("Create did not fill timestamps")
	}
}

// TestTemplate_CreateDuplicateName: a second create with the same name fails.
func TestTemplate_CreateDuplicateName(t *testing.T) {
	s := newTplSvc(t)
	if _, _, err := s.Create(&model.Template{Name: "dup", Command: "bash"}); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	_, code, err := s.Create(&model.Template{Name: "dup"})
	errNonNil(t, err, "duplicate Create")
	codeIs(t, code, response.ErrTemplateNameDuplicate, "duplicate Create")
}

// TestTemplate_CreateEmptyName: a template with no name is rejected.
func TestTemplate_CreateEmptyName(t *testing.T) {
	s := newTplSvc(t)
	_, code, err := s.Create(&model.Template{Name: ""})
	errNonNil(t, err, "empty name Create")
	codeIs(t, code, response.ErrInvalidParam, "empty name Create")
}

// TestTemplate_GetNotFound: reading an unknown id returns ErrNotFound.
func TestTemplate_GetNotFound(t *testing.T) {
	s := newTplSvc(t)
	_, code, err := s.Get("nope")
	errNonNil(t, err, "Get unknown")
	codeIs(t, code, response.ErrNotFound, "Get unknown")
}

// TestTemplate_Update verifies Update refreshes UpdatedAt while keeping
// CreatedAt, and rejects a name collision with a different template.
func TestTemplate_Update(t *testing.T) {
	s := newTplSvc(t)
	a, code, err := s.Create(&model.Template{Name: "alpha", Command: "sh"})
	if err != nil {
		t.Fatalf("Create alpha: %v (code %d)", err, code)
	}
	b, _, err := s.Create(&model.Template{Name: "beta", Command: "sh"})
	if err != nil {
		t.Fatalf("Create beta: %v", err)
	}

	origCreated := a.CreatedAt
	time.Sleep(10 * time.Millisecond) // ensure UpdatedAt moves forward
	a.Command = "/bin/bash"
	updated, code, err := s.Update(a)
	if err != nil {
		t.Fatalf("Update alpha: %v (code %d)", err, code)
	}
	codeIs(t, code, response.ErrOK, "Update alpha")
	if !updated.CreatedAt.Equal(origCreated) {
		t.Fatalf("Update changed CreatedAt: was %v now %v", origCreated, updated.CreatedAt)
	}
	if !updated.UpdatedAt.After(origCreated) {
		t.Fatalf("Update did not advance UpdatedAt")
	}

	// Name collision: rename alpha to "beta" (owned by b).
	a.Name = "beta"
	_, code, err = s.Update(a)
	errNonNil(t, err, "Update to duplicate name")
	codeIs(t, code, response.ErrTemplateNameDuplicate, "Update to duplicate name")

	// Renaming to its OWN name is fine (excludes itself).
	a.Name = "alpha"
	_, code, err = s.Update(a)
	if err != nil {
		t.Fatalf("Update same name: %v (code %d)", err, code)
	}
	codeIs(t, code, response.ErrOK, "Update same name")
	_ = b
}

// TestTemplate_UpdateNotFound: updating an unknown id returns ErrNotFound.
func TestTemplate_UpdateNotFound(t *testing.T) {
	s := newTplSvc(t)
	_, code, err := s.Update(&model.Template{ID: "ghost", Name: "x"})
	errNonNil(t, err, "Update unknown")
	codeIs(t, code, response.ErrNotFound, "Update unknown")
}

// TestTemplate_Delete: delete removes the record; a second delete returns
// ErrNotFound.
func TestTemplate_Delete(t *testing.T) {
	s := newTplSvc(t)
	a, _, err := s.Create(&model.Template{Name: "del-me", Command: "bash"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if code, err := s.Delete(a.ID); err != nil {
		t.Fatalf("Delete: %v (code %d)", err, code)
	}
	code, err := s.Delete(a.ID)
	errNonNil(t, err, "Delete again")
	codeIs(t, code, response.ErrNotFound, "Delete again")
}

// TestTemplate_List verifies List returns created templates.
func TestTemplate_List(t *testing.T) {
	s := newTplSvc(t)
	if _, _, err := s.Create(&model.Template{Name: "c1", Command: "bash"}); err != nil {
		t.Fatalf("Create c1: %v", err)
	}
	if _, _, err := s.Create(&model.Template{Name: "c2", Command: "bash"}); err != nil {
		t.Fatalf("Create c2: %v", err)
	}
	ts, code, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	codeIs(t, code, response.ErrOK, "List")
	if len(ts) != 2 {
		t.Fatalf("List returned %d templates, want 2", len(ts))
	}
}

// ---------------------------------------------------------------------------
// PtyService tests
// ---------------------------------------------------------------------------

// newPtySvc builds a PtyService backed by a real local SSH server and a fresh
// temp-dir template store. The PTY manager is pure in-memory (no persistence).
// The returned TemplateService must be used to create the template before Start
// is called.
func newPtySvc(t *testing.T) (*PtyService, *TemplateService, func()) {
	t.Helper()
	srv, addr, passcode, sshCleanup := startTestSSHServer(t)

	dir := t.TempDir()
	tplStore, err := model.NewTemplateStore(dir)
	if err != nil {
		t.Fatalf("NewTemplateStore: %v", err)
	}
	tplSvc := NewTemplateService(tplStore)
	mgr := pty.NewManager(pty.SSHDialConfig{Addr: addr, Passcode: passcode})
	mgr.SetPTYSizer(srv) // wire the in-process PTY resizer
	svc := NewPtyService(mgr, tplSvc)
	return svc, tplSvc, sshCleanup
}

// TestPty_StartListGetKill exercises the happy path: create a template, start a
// session, list it, get it, then kill it. Killing removes the session from the
// in-memory pool, so a subsequent List/Get no longer sees it.
func TestPty_StartListGetKill(t *testing.T) {
	svc, tplSvc, cleanup := newPtySvc(t)
	defer cleanup()

	tpl, _, err := tplSvc.Create(&model.Template{
		Name:    "pty-happy",
		Command: "/bin/bash",
		Args:    []string{"-c", "sleep 30"},
	})
	if err != nil {
		t.Fatalf("Create template: %v", err)
	}

	user := &model.User{ID: "u1", Username: "alice"}
	rec, code, err := svc.Start(user, tpl.ID, "my terminal")
	if err != nil {
		t.Fatalf("Start: %v (code %d)", err, code)
	}
	codeIs(t, code, response.ErrOK, "Start")
	if rec.ID == "" {
		t.Fatalf("Start returned empty session ID")
	}
	if rec.Title != "my terminal" {
		t.Fatalf("Start title = %q, want %q", rec.Title, "my terminal")
	}
	if rec.UserID != user.ID {
		t.Fatalf("Start UserID = %q, want %q", rec.UserID, user.ID)
	}
	if rec.Status != "running" {
		t.Fatalf("Start status = %q, want running", rec.Status)
	}

	// List should include the session.
	sessions, _, err := svc.List(user.ID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("List returned %d sessions, want 1", len(sessions))
	}

	// Get returns the same record.
	got, _, err := svc.Get(user, rec.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != rec.ID {
		t.Fatalf("Get ID mismatch")
	}

	// Kill removes the session from the live pool. A subsequent List/Get must
	// no longer observe it (there is no persisted "closed" record anymore).
	if code, err := svc.Kill(user, rec.ID); err != nil {
		t.Fatalf("Kill: %v (code %d)", err, code)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if after, _, _ := svc.List(user.ID); len(after) == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	after, _, _ := svc.List(user.ID)
	if len(after) != 0 {
		t.Fatalf("after Kill List returned %d sessions, want 0 (removed from pool)", len(after))
	}
	if _, gcode, gerr := svc.Get(user, rec.ID); gerr == nil {
		t.Fatalf("Get after Kill succeeded; want ErrNotFound (removed from pool). code=%d", gcode)
	}
}

// TestPty_StartTemplateNotFound: starting from an unknown template returns
// ErrNotFound.
func TestPty_StartTemplateNotFound(t *testing.T) {
	svc, _, cleanup := newPtySvc(t)
	defer cleanup()

	user := &model.User{ID: "u1", Username: "alice"}
	_, code, err := svc.Start(user, "no-such-template", "")
	errNonNil(t, err, "Start unknown template")
	codeIs(t, code, response.ErrNotFound, "Start unknown template")
}

// TestPty_GetForbidden: a different user cannot read another user's session.
func TestPty_GetForbidden(t *testing.T) {
	svc, tplSvc, cleanup := newPtySvc(t)
	defer cleanup()

	tpl, _, err := tplSvc.Create(&model.Template{
		Name:    "pty-forbidden",
		Command: "/bin/bash",
		Args:    []string{"-c", "sleep 30"},
	})
	if err != nil {
		t.Fatalf("Create template: %v", err)
	}

	alice := &model.User{ID: "alice-id", Username: "alice"}
	bob := &model.User{ID: "bob-id", Username: "bob"}
	rec, _, err := svc.Start(alice, tpl.ID, "")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _, _ = svc.Kill(alice, rec.ID) }()

	_, code, err := svc.Get(bob, rec.ID)
	errNonNil(t, err, "Get as wrong user")
	if code != response.ErrForbidden {
		t.Fatalf("Get as wrong user: code = %d, want %d", code, response.ErrForbidden)
	}
}

// TestPty_GetNotFound: getting an unknown session returns ErrNotFound.
func TestPty_GetNotFound(t *testing.T) {
	svc, _, cleanup := newPtySvc(t)
	defer cleanup()
	user := &model.User{ID: "u1", Username: "alice"}
	_, code, err := svc.Get(user, "ghost-session")
	errNonNil(t, err, "Get unknown session")
	codeIs(t, code, response.ErrNotFound, "Get unknown session")
}

// TestPty_KillForbidden: a different user cannot kill another user's session.
func TestPty_KillForbidden(t *testing.T) {
	svc, tplSvc, cleanup := newPtySvc(t)
	defer cleanup()

	tpl, _, err := tplSvc.Create(&model.Template{
		Name:    "pty-kill-forbidden",
		Command: "/bin/bash",
		Args:    []string{"-c", "sleep 30"},
	})
	if err != nil {
		t.Fatalf("Create template: %v", err)
	}
	alice := &model.User{ID: "alice-id", Username: "alice"}
	bob := &model.User{ID: "bob-id", Username: "bob"}
	rec, _, err := svc.Start(alice, tpl.ID, "")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _, _ = svc.Kill(alice, rec.ID) }()

	code, err := svc.Kill(bob, rec.ID)
	errNonNil(t, err, "Kill as wrong user")
	if code != response.ErrForbidden {
		t.Fatalf("Kill as wrong user: code = %d, want %d", code, response.ErrForbidden)
	}
}

// TestPty_StartDefaultTitle: when title is empty, the persisted title defaults
// to the template's Name.
func TestPty_StartDefaultTitle(t *testing.T) {
	svc, tplSvc, cleanup := newPtySvc(t)
	defer cleanup()

	tpl, _, err := tplSvc.Create(&model.Template{
		Name:    "named-tpl",
		Command: "/bin/bash",
		Args:    []string{"-c", "sleep 30"},
	})
	if err != nil {
		t.Fatalf("Create template: %v", err)
	}
	user := &model.User{ID: "u1", Username: "alice"}
	rec, _, err := svc.Start(user, tpl.ID, "")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _, _ = svc.Kill(user, rec.ID) }()
	if rec.Title != "named-tpl" {
		t.Fatalf("default title = %q, want named-tpl", rec.Title)
	}
}
