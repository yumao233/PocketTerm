package pty

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	ssh "golang.org/x/crypto/ssh" // crypto ssh client

	"github.com/wangkun/webshell/internal/model"
)

// SSHDialConfig is the address + passcode the Manager uses to dial the in-process
// local SSH server (internal/ssh). The address is normally the running server's
// Addr().String() (e.g. 127.0.0.1:2222); the passcode is the same shared secret
// the server was configured with.
type SSHDialConfig struct {
	Addr     string // e.g. "127.0.0.1:2222"
	Passcode string
}

// NewManager builds a Manager that dials the local SSH server described by
// sshCfg. Session metadata is held purely in memory — nothing is persisted to
// disk, so a process restart leaves zero sessions (no ghost "running" records
// whose PTY has already died). NewManager does NOT start the SSH server — that
// is the application's responsibility (the server must already be listening on
// sshCfg.Addr before Start is called).
func NewManager(sshCfg SSHDialConfig) *Manager {
	return &Manager{
		sshCfg:   sshCfg,
		sessions: make(map[string]*Session),
	}
}

// PTYSizer applies a window-size change directly to the PTY backing an SSH
// session, keyed by the webshell session id (the same uuid the Manager passes
// to the server via the WEBSESSION_ID env var). The local SSH server
// (internal/ssh) implements it; the Manager calls it on resize so the kernel
// PTY gets TIOCSWINSZ immediately, instead of relying on the SSH window-change
// request which gliderlabs/ssh v0.3.8 leaves un-applied to the real PTY (and
// whose s.Pty() channel races the window-change writer).
type PTYSizer interface {
	Resize(sessionID string, rows, cols int) error
}

// Manager owns the live PTY session pool. It is the bridge between the service
// layer (which calls Start/Kill/ListByUser) and the WS layer (which calls Get +
// Session.Subscribe/WriteStdin/Resize). Sessions are keyed by their model.ID.
//
// The Manager is the single source of truth for session metadata: it stores
// everything in memory and never writes to disk. When the process exits every
// session is destroyed (its PTY child dies with it); on restart the pool starts
// empty, so ListByUser never returns ghost "running" records whose PTY has
// already died.
type Manager struct {
	sshCfg SSHDialConfig

	mu       sync.RWMutex
	sessions map[string]*Session

	// sizer, when set, applies window-size changes directly to the local SSH
	// server's PTY (same process). When nil, resize falls back to the SSH
	// WindowChange request only — which the gliderlabs server acknowledges but
	// (v0.3.8) does not apply to the real PTY. Set via SetPTYSizer.
	sizer PTYSizer
}

// SetPTYSizer wires the in-process PTY resizer (the local SSH server). It is
// optional: without it resize degrades to the SSH WindowChange request only.
func (m *Manager) SetPTYSizer(s PTYSizer) { m.sizer = s }

// Start creates a new PTY session for the user/template: it dials the local SSH
// server, opens a session with a PTY, runs the template command (or an
// interactive shell) and registers the live *Session in the in-memory pool. It
// returns a metadata snapshot (*model.Session, status=running); the live session
// is obtained via Get(id).
//
// The session is registered in the pool BEFORE Start returns, so an immediate
// Get(id) will succeed (no race with the handler). Nothing is persisted to disk;
// the metadata lives only as long as the process.
func (m *Manager) Start(user *model.User, tpl *model.Template) (*model.Session, error) {
	if m == nil {
		return nil, errors.New("pty: nil manager")
	}
	if user == nil {
		return nil, errors.New("pty: nil user")
	}
	if tpl == nil {
		return nil, errors.New("pty: nil template")
	}

	now := time.Now()
	sessionID := uuid.NewString()
	title := tpl.Name
	if title == "" {
		title = "shell"
	}

	// Dial the local SSH server. The passcode authenticates; any username is
	// accepted by the server, so passing user.Username is fine and good for
	// audit logs.
	clientCfg := &ssh.ClientConfig{
		User:            user.Username,
		Auth:            []ssh.AuthMethod{ssh.Password(m.sshCfg.Passcode)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // loopback, fresh host key each process
		Timeout:         5 * time.Second,
	}
	client, err := ssh.Dial("tcp", m.sshCfg.Addr, clientCfg)
	if err != nil {
		return nil, fmt.Errorf("pty: dial local ssh %s: %w", m.sshCfg.Addr, err)
	}

	sess, err := client.NewSession()
	if err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("pty: new ssh session: %w", err)
	}

	// PTY: xterm-256color, default 80 columns x 24 rows. ECHO on so the user
	// sees typed input echoed back (matches xterm.js expectations in a normal
	// terminal).
	//
	// NOTE: golang.org/x/crypto/ssh RequestPty signature is
	// RequestPty(term string, h, w int, modes) — height(rows) FIRST, then
	// width(cols). Swapping them silently sets cols=24 which makes TUIs like
	// htop render at 24 columns until a resize lands. Keep rows, cols in that
	// order.
	modes := ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	}
	if err := sess.RequestPty("xterm-256color", 24, 80, modes); err != nil {
		_ = sess.Close()
		_ = client.Close()
		return nil, fmt.Errorf("pty: request pty: %w", err)
	}

	// Set env from the template (best-effort; ignored by the server if the
	// command does not read it).
	if tpl.Cwd != "" {
		_ = sess.Setenv("PWD", tpl.Cwd)
	}
	for _, kv := range tpl.Env {
		if kv.Key == "" {
			continue
		}
		_ = sess.Setenv(kv.Key, kv.Value)
	}
	// Pass the webshell session id to the server so it can key the PTY in its
	// in-process resize registry. The Manager's Resize calls the server's
	// resizer directly (same process) with this id; x/crypto/ssh does not
	// expose the SSH session id on the client side, so we use our own uuid.
	_ = sess.Setenv("WEBSESSION_ID", sessionID)

	// stdin pipe — must be obtained before Start/Shell.
	stdin, err := sess.StdinPipe()
	if err != nil {
		_ = sess.Close()
		_ = client.Close()
		return nil, fmt.Errorf("pty: stdin pipe: %w", err)
	}

	// Build the live Session and register it in the pool BEFORE Start/Shell.
	// This is REQUIRED: newSession assigns sess.Stdout/sess.Stderr, and
	// x/crypto/ssh spawns its stdout/stderr reader goroutines INSIDE Start/Shell
	// — assigning the writers after Start would race those readers. Building the
	// session first guarantees the writers are in place before the readers start.
	ps := newSession(sessionID, user.ID, tpl.ID, title, now, client, sess, stdin, m.sizer)
	m.mu.Lock()
	m.sessions[sessionID] = ps
	m.mu.Unlock()

	// Determine and start the command.
	//
	// If a Command is set we honor Cwd by wrapping in /bin/sh -c "cd <cwd> &&
	// exec <cmd> <args>". If no Cwd is set, run the command directly with its
	// args. If no Command is set, start an interactive shell and (best-effort)
	// send `cd <cwd>` as the first line of stdin so the user lands in the right
	// directory.
	if tpl.Command != "" {
		if err := m.startCommand(sess, tpl); err != nil {
			ps.Close()
			m.mu.Lock()
			delete(m.sessions, sessionID)
			m.mu.Unlock()
			return nil, fmt.Errorf("pty: start command: %w", err)
		}
	} else {
		if err := sess.Shell(); err != nil {
			ps.Close()
			m.mu.Lock()
			delete(m.sessions, sessionID)
			m.mu.Unlock()
			return nil, fmt.Errorf("pty: start shell: %w", err)
		}
		// Best-effort: drop the user into Cwd if set. We do not wait for the cd
		// to take effect — it is racy by nature, but in practice the shell reads
		// stdin sequentially.
		if tpl.Cwd != "" {
			go func(cwd string) {
				_, _ = stdin.Write([]byte("cd " + shellQuote(cwd) + "\n"))
			}(tpl.Cwd)
		}
	}

	// Pump goroutine: when the remote side exits (command finishes, shell
	// closed, network dropped) sess.Wait returns and we tear the session down.
	// Close is idempotent (sync.Once), so an overlapping Kill is safe. After
	// Close the session is removed from the in-memory pool — there is nothing
	// to persist, so natural exit simply makes the session disappear (a
	// subsequent Get/ListByUser will not see it).
	go func() {
		_ = sess.Wait()
		ps.Close()
		m.mu.Lock()
		delete(m.sessions, sessionID)
		m.mu.Unlock()
	}()

	rec := &model.Session{
		ID:         sessionID,
		UserID:     user.ID,
		TemplateID: tpl.ID,
		Title:      title,
		Status:     "running",
		CreatedAt:  now,
	}
	return rec, nil
}

// startCommand runs tpl.Command with tpl.Args, honoring tpl.Cwd.
//
// It assembles a single shell command line from Command + shell-quoted Args,
// prefixing `cd <cwd> && exec ` when Cwd is set, and sends it to the SSH server
// via sess.Start. The local SSH server (internal/ssh) runs the raw command line
// through its login shell (`<shell> -c "<line>"`), so Args are parsed
// consistently and Cwd is honored before the command runs.
func (m *Manager) startCommand(sess *ssh.Session, tpl *model.Template) error {
	var b strings.Builder
	if tpl.Cwd != "" {
		b.WriteString("cd ")
		b.WriteString(shellQuote(tpl.Cwd))
		b.WriteString(" && exec ")
	}
	b.WriteString(tpl.Command)
	for _, a := range tpl.Args {
		b.WriteByte(' ')
		b.WriteString(shellQuote(a))
	}
	return sess.Start(b.String())
}

// Get returns the live session, or nil if missing/closed.
func (m *Manager) Get(id string) *Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sessions[id]
}

// Kill closes the session and removes it from the pool. Returns
// ErrSessionNotFound if the id is not in the live pool. The session's Close is
// idempotent, so a Kill racing with a natural-exit pump is safe.
func (m *Manager) Kill(id string) error {
	m.mu.Lock()
	ps, ok := m.sessions[id]
	if ok {
		delete(m.sessions, id)
	}
	m.mu.Unlock()
	if !ok {
		return ErrSessionNotFound
	}
	ps.Close()
	return nil
}

// Resize resizes a live session. Returns ErrSessionNotFound if missing.
func (m *Manager) Resize(id string, cols, rows int) error {
	ps := m.Get(id)
	if ps == nil {
		return ErrSessionNotFound
	}
	return ps.Resize(cols, rows)
}

// ListByUser returns the user's live sessions from the in-memory pool (metadata
// copies only; the live *Session is obtained via Get). A session that has exited
// naturally is already removed from the pool by the pump goroutine, so it will
// not appear here — there are no ghost "running" records after a PTY dies.
func (m *Manager) ListByUser(userID string) ([]*model.Session, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*model.Session, 0, len(m.sessions))
	for _, ps := range m.sessions {
		if ps.UserID != userID {
			continue
		}
		out = append(out, &model.Session{
			ID:         ps.ID,
			UserID:     ps.UserID,
			TemplateID: ps.TemplateID,
			Title:      ps.Title,
			Status:     "running",
			CreatedAt:  ps.CreatedAt,
		})
	}
	return out, nil
}

// GetMeta returns the metadata snapshot (a *model.Session) for a live session by
// id, or nil if not in the pool. Read-only; the live PTY is obtained via Get.
func (m *Manager) GetMeta(id string) *model.Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ps, ok := m.sessions[id]
	if !ok {
		return nil
	}
	return &model.Session{
		ID:         ps.ID,
		UserID:     ps.UserID,
		TemplateID: ps.TemplateID,
		Title:      ps.Title,
		Status:     "running",
		CreatedAt:  ps.CreatedAt,
	}
}

// SetTitle updates the title of a live session in memory (used when the caller
// overrides the template's name).
func (m *Manager) SetTitle(id, title string) {
	m.mu.Lock()
	if ps, ok := m.sessions[id]; ok {
		ps.Title = title
	}
	m.mu.Unlock()
}

// CloseAll closes every live session and clears the pool. Used on graceful
// shutdown so no PTY child process outlives the webshell process. Best-effort:
// errors from individual closes are ignored; each session's Close is idempotent
// (sync.Once).
func (m *Manager) CloseAll() {
	m.mu.Lock()
	sessions := m.sessions
	m.sessions = make(map[string]*Session)
	m.mu.Unlock()
	for _, ps := range sessions {
		_ = ps.Close()
	}
}

// shellQuote wraps s in single quotes, escaping any embedded single quotes, so
// the result is safe to splice into a shell command line. This guards Cwd/args
// against injection when building "cd <cwd> && exec <cmd> <args>".
func shellQuote(s string) string {
	// Replace every ' with '\''. Single quotes have no other special handling
	// inside a single-quoted string.
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
