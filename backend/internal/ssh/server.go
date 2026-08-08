// Package ssh implements an in-process SSH server that listens on the loopback
// interface and serves interactive PTY shells. Each Webshell terminal session
// connects to this server as an SSH *client*; the server authenticates by a
// single shared passcode (any username is accepted) and spawns the user's
// login shell ($SHELL, falling back to /bin/bash) attached to a real PTY.
//
// The server deliberately listens on 127.0.0.1 only — it is never exposed on a
// public interface. The passcode provides a second factor on top of the
// network isolation.
//
// Host key handling: when Config.HostKeyFile is empty an ed25519 key is
// generated in-memory and used for the lifetime of the process (lost on
// restart). When HostKeyFile is set and the file exists it is loaded; if it is
// set but missing a fresh ed25519 key is generated and persisted to that path
// so subsequent starts present a stable host key.
package ssh

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/gliderlabs/ssh"
	gossh "golang.org/x/crypto/ssh"
)

// Config configures a local SSH server.
type Config struct {
	// Listen is the bind address, e.g. "127.0.0.1:2222". Use "127.0.0.1:0" in
	// tests to grab an ephemeral loopback port.
	Listen string
	// Passcode is the shared secret required to authenticate. Any username is
	// accepted; only the passcode is validated.
	Passcode string
	// HostKeyFile optionally points at a PEM-encoded private key file. If empty,
	// an ed25519 key is generated in-memory. If set but missing, a fresh
	// ed25519 key is generated and persisted to this path.
	HostKeyFile string
}

// ctxKeyPty is the context key under which the PtyCallback stores a value copy
// of the requested ssh.Pty for the session handler to read (see the comment in
// NewServer's PtyCallback assignment). It is an unexported type so it cannot
// collide with keys other packages might set on the same context.
type ctxKeyPty struct{}

// Server is a local (loopback-only) SSH server that hands out interactive PTY
// shells.
type Server struct {
	srv        *ssh.Server
	listener   net.Listener
	cfg        Config
	hostSigner gossh.Signer
	mu         sync.Mutex
	closed     bool

	// resizers maps each SSH session id to a live ptyHandle, so the process
	// that dialed us (the pty.Manager, running in the SAME process) can resize
	// the real PTY directly instead of relying on gliderlabs's window-change
	// channel. That channel is obtained via s.Pty(), which in gliderlabs/ssh
	// v0.3.8 dereferences *sess.pty without a lock — racing the window-change
	// writer and tripping the race detector. The in-process registry sidesteps
	// the upstream race entirely and applies TIOCSWINSZ immediately.
	resizers resizerRegistry
}

// ptyHandle is the per-session resize handle kept in the Server's resizer
// registry. mu guards ptyFile/closed against a concurrent teardown Close so
// pty.Setsize (which calls ptyFile.Fd()) never targets a closed/recycled fd.
type ptyHandle struct {
	mu      sync.Mutex
	ptyFile *os.File
	closed  bool
}

// Resize applies a window-size change to the PTY of the given webshell session
// id. It is a no-op (returns nil) if the session is unknown — the pty.Manager
// treats resize as best-effort, and a session that already tore down has
// nothing to resize. The method satisfies pty.PTYSizer (the interface is
// declared in the pty package, the consumer; *Server implements it without an
// explicit import).
func (s *Server) Resize(sessionID string, rows, cols int) error {
	h := s.resizers.get(sessionID)
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil
	}
	_ = pty.Setsize(h.ptyFile, &pty.Winsize{
		Rows: uint16(rows),
		Cols: uint16(cols),
	})
	return nil
}

// NewServer builds a Server from cfg. It resolves the host signer (loading or
// generating/persisting the host key as configured) and wires the password
// handler and session handler. It does NOT start listening; call Start.
func NewServer(cfg Config) (*Server, error) {
	if cfg.Listen == "" {
		return nil, errors.New("ssh: Config.Listen must not be empty")
	}
	signer, err := resolveHostSigner(cfg.HostKeyFile)
	if err != nil {
		return nil, fmt.Errorf("ssh: resolve host signer: %w", err)
	}

	srv := &ssh.Server{
		// IdleTimeout/MaxTimeout left at zero (no timeout) — the session may be
		// long-lived; lifecycle is managed by the PTY bridge.
	}
	srv.AddHostKey(signer)

	passcode := cfg.Passcode
	srv.PasswordHandler = func(ctx ssh.Context, password string) bool {
		return password == passcode
	}

	// PtyCallback runs synchronously when a "pty-req" is received — BEFORE any
	// "exec"/"shell" request spawns the session handler, and BEFORE any
	// "window-change" can arrive. We capture a VALUE COPY of the requested Pty
	// (Term + initial Window) into the session context so the handler can read
	// the PTY parameters without ever touching gliderlabs's `sess.pty` pointer.
	//
	// Why: gliderlabs/ssh v0.3.8 mutates `sess.pty.Window = win` on every
	// window-change without a lock, while (*session).Pty() dereferences
	// `*sess.pty`. Calling s.Pty() from the handler therefore races the
	// window-change writer under -race. By capturing a private copy here we
	// avoid the dereference entirely; the handler does not call s.Pty().
	srv.PtyCallback = func(ctx ssh.Context, pty ssh.Pty) bool {
		// Copy the value (Pty is a plain struct: Term string + Window struct).
		cp := pty
		ctx.SetValue(ctxKeyPty{}, &cp)
		return true
	}

	sv := &Server{srv: srv, cfg: cfg, hostSigner: signer}
	srv.Handler = sv.sessionHandler
	return sv, nil
}

// Start binds the listener and serves the SSH protocol. It is BLOCKING: it
// returns only when the server stops (via Close) or on a fatal listen/serve
// error. Callers that want non-blocking behavior should run it in a goroutine.
// After Start has bound the socket, Addr returns the actual address (useful
// when the configured port was 0).
func (s *Server) Start() error {
	ln, err := net.Listen("tcp", s.cfg.Listen)
	if err != nil {
		return fmt.Errorf("ssh: listen %s: %w", s.cfg.Listen, err)
	}
	s.mu.Lock()
	s.listener = ln
	s.mu.Unlock()

	// srv.Serve blocks until the listener is closed.
	if err := s.srv.Serve(ln); err != nil {
		// Serve returns net.ErrClosed when Close shutdowns the listener; treat
		// that as a clean stop rather than an error.
		if errors.Is(err, net.ErrClosed) {
			return nil
		}
		return err
	}
	return nil
}

// Addr returns the actual bound address of the listener, or nil if Start has
// not bound the socket yet.
func (s *Server) Addr() net.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener == nil {
		return nil
	}
	return s.listener.Addr()
}

// Close gracefully stops the server: it closes the listener (unblocking Serve)
// and shuts down all in-flight sessions with a short grace period.
func (s *Server) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	ln := s.listener
	s.mu.Unlock()

	if ln != nil {
		_ = ln.Close()
	}
	// Shutdown with a grace period so active sessions get torn down cleanly.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = s.srv.Shutdown(ctx)
	return nil
}

// sessionHandler is invoked by gliderlabs/ssh for each new SSH session. It
// allocates a real PTY, starts the user's shell, and bridges the SSH session
// I/O to the PTY in both directions. It returns when either side closes.
func (sv *Server) sessionHandler(s ssh.Session) {
	// Public key / agent etc. are not used; passcode-only auth already happened
	// in PasswordHandler. The exit code is reported back to the client.
	exit := func(code int, msg string) {
		if msg != "" {
			_, _ = io.WriteString(s.Stderr(), msg+"\r\n")
		}
		_ = s.Exit(code)
	}

	// Read the PTY request captured by PtyCallback (run synchronously on
	// pty-req, BEFORE any window-change). We deliberately do NOT call s.Pty()
	// here: gliderlabs v0.3.8 mutates sess.pty.Window on window-change without
	// a lock, racing the *sess.pty dereference inside Pty(). The captured value
	// is a private copy isolated from those writes.
	pv := s.Context().Value(ctxKeyPty{})
	ptyReqPtr, isPty := pv.(*ssh.Pty)
	if !isPty || ptyReqPtr == nil {
		exit(1, "pty required: this server only serves interactive PTY shells")
		return
	}
	ptyReq := *ptyReqPtr

	shell := determineShell()

	// An "exec" request (sess.Start on the client) carries a command string via
	// RawCommand; a "shell" request leaves it empty. When a command is present
	// we run it through the login shell (`<shell> -c "<rawCmd>"`) so the
	// template's command line — already shell-quoted by the pty client — is
	// parsed by a real shell and the PTY still applies. When no command is
	// present we start an interactive login shell, exactly as before.
	rawCmd := s.RawCommand()
	var cmd *exec.Cmd
	if rawCmd != "" {
		cmd = exec.Command(shell, "-c", rawCmd)
	} else {
		cmd = exec.Command(shell)
	}
	cmd.Env = append(os.Environ(), "TERM="+ptyReq.Term)
	// Merge any env vars the client requested via sess.Setenv (gliderlabs
	// surfaces them on the session). Login-ish environment: HOME/USER/LOGNAME
	// sensible defaults are appended for any that are missing.
	cmd.Env = append(cmd.Env, s.Environ()...)
	if cmd.Env = ensureLoginEnv(cmd.Env); len(cmd.Env) == 0 {
		cmd.Env = os.Environ()
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid:  true, // start a new session so we're the controlling process
		Setctty: true, // set the PTY as the controlling terminal
	}

	// Start the shell attached to a freshly allocated PTY sized to the
	// requested window. Errors here are fatal — there is no useful shell to
	// run.
	ptyFile, err := pty.StartWithSize(cmd, &pty.Winsize{
		Rows: uint16(ptyReq.Window.Height),
		Cols: uint16(ptyReq.Window.Width),
	})
	if err != nil {
		exit(1, fmt.Sprintf("ssh: start pty: %v", err))
		return
	}

	// Register this session's PTY for direct in-process resize. The pty.Manager
	// (same process as this server) calls Server.Resize(sessionID, …) with the
	// webshell session id it passed to us via the WEBSESSION_ID env var; we apply
	// TIOCSWINSZ here under ptyHandle.mu so it cannot race the teardown Close.
	// This replaces consuming gliderlabs's winch channel — s.Pty() (the only way
	// to that channel) dereferences *sess.pty without a lock in v0.3.8, racing
	// the window-change writer and tripping the race detector. The registry
	// sidesteps that upstream race entirely.
	sessionID := envValue(s.Environ(), "WEBSESSION_ID")
	handle := &ptyHandle{ptyFile: ptyFile}
	if sessionID != "" {
		sv.resizers.register(sessionID, handle)
	}
	defer sv.resizers.unregister(sessionID)

	// Coordinate teardown: when EITHER copy completes, the session is done.
	// We track both goroutines and tear everything down once one returns.
	var copyDone sync.WaitGroup
	copyDone.Add(2)

	stop := make(chan struct{})
	var stopOnce sync.Once

	// teardown is the SINGLE path that releases every resource the session
	// holds: it kills the shell process (best-effort), closes the PTY (which
	// unblocks the pty->client read), and closes the SSH session via
	// s.Exit(code) (which sends exit-status to the client and closes the SSH
	// channel, unblocking s.Read in the client->pty goroutine). It is guarded
	// by sync.Once so a second invocation from the other select branch — or
	// from either copy goroutine — is a safe no-op and never double-closes an
	// fd / never panics on a second s.Exit. All steps ignore errors: by the
	// time teardown runs some of these are already closing, and the only thing
	// we care about is that the close happens at all so the blocked reads in
	// BOTH copy goroutines return and copyDone.Wait() can complete.
	//
	// The PTY is closed under handle.mu so an in-flight Server.Resize
	// (TIOCSWINSZ via ptyFile.Fd()) cannot race the close or target a recycled
	// fd; handle.closed makes a later Resize a no-op.
	var teardownOnce sync.Once
	teardown := func(code int) {
		stopOnce.Do(func() { close(stop) })
		teardownOnce.Do(func() {
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			handle.mu.Lock()
			_ = ptyFile.Close()
			handle.closed = true
			handle.ptyFile = nil
			handle.mu.Unlock()
			// s.Exit sends the exit-status request and closes the SSH channel.
			// gliderlabs/ssh guards it with an `exited` flag, so a repeat call
			// returns an error rather than panicking — irrelevant here because
			// the Once guarantees we only call it once.
			_ = s.Exit(code)
		})
	}

	// ptyMu used to wrap every ptyFile Read/Write, but that serialized the
	// stdin->pty writer behind a blocking pty->client Read: when the PTY had no
	// output ready, the read goroutine held the lock forever, so input written
	// from the client could never reach bash — and bash could therefore never
	// produce output, deadlocking the round-trip. Concurrent Read/Write on a
	// creack/pty *os.File (the PTY master) are safe at the kernel level; we
	// therefore run them WITHOUT the mutex. (Race-detector noise about
	// *os.File internals is not observed in practice for Read/Write, which
	// route through poll.FD's own locking.) If a future Go version flags
	// concurrent Read/Write here, switch to using two separate files (dup the
	// master for read vs write) instead of reintroducing a coarse lock.

	// client -> pty (stdin)
	go func() {
		defer copyDone.Done()
		buf := make([]byte, 4096)
		for {
			n, err := s.Read(buf)
			if n > 0 {
				if _, werr := ptyFile.Write(buf[:n]); werr != nil {
					break
				}
			}
			if err != nil {
				break
			}
		}
		// Client closed stdin — signal teardown. We do NOT close ptyFile here:
		// tearing down the PTY is the single teardown path's job (guarding
		// against double-close and ordering the close BEFORE copyDone.Wait so
		// the pty->client read unblocks).
		teardown(0)
	}()

	// pty -> client (stdout/stderr). Concurrency with the stdin->pty writer is
	// safe at the kernel level: concurrent Read/Write on a creack/pty *os.File
	// (the PTY master) route through poll.FD's own locking. (A prior version
	// wrapped every Read/Write in a mutex, but that serialized the stdin writer
	// behind a blocking pty-side Read and deadlocked the round-trip.)
	//
	// Server-side resize is NOT handled here: it is applied directly to the PTY
	// by Server.Resize (via the resizer registry) on the pty.Manager's request,
	// bypassing gliderlabs's racing winch channel. This copy goroutine only
	// forwards bytes.
	go func() {
		defer copyDone.Done()
		buf := make([]byte, 4096)
		for {
			n, err := ptyFile.Read(buf)
			if n > 0 {
				if _, werr := s.Write(buf[:n]); werr != nil {
					break
				}
			}
			if err != nil {
				break
			}
		}
		teardown(0)
	}()

	// Wait for the shell process to exit. We check it in a goroutine so we can
	// also react to the stop signal (which fires when a copy ends, e.g. the
	// client ran `exit` and the pty reached EOF).
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()

	// select: when EITHER the shell exits on its own OR a copy goroutine signals
	// stop (client disconnect / stdin EOF), we run teardown(code) FIRST and only
	// then wait for the copy goroutines. Ordering matters: teardown closes the
	// PTY (unblocking pty->client read) and closes the SSH session (unblocking
	// client->pty s.Read); closing BEFORE the waits is what lets copyDone.Wait()
	// return. Reversing the order deadlocks.
	select {
	case err := <-waitCh:
		// Shell exited on its own (e.g. user typed `exit`). Compute the exit
		// code, then teardown(code) closes ptyFile + s.Exit(code) (which sends
		// exit-status to the client and closes the SSH channel, unblocking the
		// client->pty s.Read). Both copy goroutines then return and the waits
		// complete.
		code := 0
		if err != nil {
			code = 1
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				code = ee.ExitCode()
			}
		}
		teardown(code)
		copyDone.Wait()
		return
	case <-stop:
		// A copy ended (client disconnected, or stdin EOF). Kill the shell so
		// cmd.Wait() returns (reaping the child), then teardown(0) closes the
		// PTY (unblocking pty->client read) and the SSH session (unblocking any
		// lingering client->pty s.Read if the client vanished without a clean
		// EOF). Then — and only then — wait for the copy goroutines.
		// teardown also kills the process inside its Once, but we kill here
		// FIRST (best-effort) so cmd.Wait() returns promptly: teardown's Once
		// may not have run yet from this branch, and we must reap the child
		// before tearing down. The second kill inside teardown is a harmless
		// no-op on an already-dead process.
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		<-waitCh
		teardown(0)
		copyDone.Wait()
		return
	}
}

// determineShell returns the shell to run: $SHELL of the server process if
// set and non-empty, else /bin/bash.
func determineShell() string {
	if sh := os.Getenv("SHELL"); sh != "" {
		return sh
	}
	return "/bin/bash"
}

// envValue returns the value of key in the "KEY=VALUE" env slice, or "" if
// absent. Used to read WEBSESSION_ID (the webshell session id the pty.Manager
// passes so the server can key its in-process PTY resize registry).
func envValue(env []string, key string) string {
	prefix := key + "="
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			return e[len(prefix):]
		}
	}
	return ""
}

// ensureLoginEnv ensures HOME, USER, LOGNAME, and PATH are present in env,
// appending sensible defaults for any that are missing. It returns the
// (possibly extended) env slice. If env is empty it returns os.Environ().
func ensureLoginEnv(env []string) []string {
	if len(env) == 0 {
		env = os.Environ()
	}
	have := func(key string) bool {
		for _, e := range env {
			if strings.HasPrefix(e, key+"=") {
				return true
			}
		}
		return false
	}
	set := func(key, val string) {
		if val == "" {
			return
		}
		if !have(key) {
			env = append(env, key+"="+val)
		}
	}

	if u, err := user.Current(); err == nil {
		set("HOME", u.HomeDir)
		set("USER", u.Username)
		set("LOGNAME", u.Username)
	}
	set("PATH", "/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin")
	return env
}

// resolveHostSigner loads a host signer from keyFile if present, otherwise
// generates an ed25519 key. When keyFile is non-empty and missing, the
// generated key is persisted (PEM OpenSSH format) to keyFile for reuse across
// restarts.
func resolveHostSigner(keyFile string) (gossh.Signer, error) {
	if keyFile != "" {
		if signer, err := loadSignerFromFile(keyFile); err == nil {
			return signer, nil
		} else if !os.IsNotExist(err) {
			return nil, err
		}
		// file missing -> generate and persist below
	}

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate ed25519: %w", err)
	}

	if keyFile != "" {
		if err := persistEd25519Key(keyFile, priv); err != nil {
			return nil, fmt.Errorf("persist host key %s: %w", keyFile, err)
		}
	}

	signer, err := gossh.NewSignerFromKey(priv)
	if err != nil {
		return nil, fmt.Errorf("new signer: %w", err)
	}
	return signer, nil
}

// loadSignerFromFile parses a PEM-encoded private key at path and returns a
// Signer. It supports OpenSSH and PKCS8 PEM blocks (x/crypto/ssh ParseRawPrivateKey
// handles both).
func loadSignerFromFile(path string) (gossh.Signer, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	signer, err := gossh.ParsePrivateKey(raw)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return signer, nil
}

// persistEd25519Key writes the ed25519 private key to path in OpenSSH PEM
// format with restrictive permissions, creating parent dirs as needed.
func persistEd25519Key(path string, priv ed25519.PrivateKey) error {
	// Serialize to OpenSSH PEM format. golang.org/x/crypto/ssh provides
	// MarshalPrivateKey for this.
	block, err := gossh.MarshalPrivateKey(priv, "")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	data := pem.EncodeToMemory(block)

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	// Write atomically with 0600 perms.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".hostkey-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return err
	}
	return nil
}

// resizerRegistry maps SSH session ids to live PTY resize handles. It is
// concurrency-safe; register/unregister run in the session handler, get runs
// in Server.Resize (called from the pty.Manager).
type resizerRegistry struct {
	mu sync.Mutex
	m  map[string]*ptyHandle
}

func (r *resizerRegistry) register(id string, h *ptyHandle) {
	r.mu.Lock()
	if r.m == nil {
		r.m = make(map[string]*ptyHandle)
	}
	r.m[id] = h
	r.mu.Unlock()
}

func (r *resizerRegistry) unregister(id string) {
	r.mu.Lock()
	delete(r.m, id)
	r.mu.Unlock()
}

func (r *resizerRegistry) get(id string) *ptyHandle {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.m[id]
}
