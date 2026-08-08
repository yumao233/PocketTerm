package ssh

import (
	"bytes"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	gossh "golang.org/x/crypto/ssh"
)

// syncBuffer is a bytes.Buffer safe for concurrent reads/writes. The SSH
// client's stdout and stderr copy goroutines both write to the same buffer in
// our tests, so the underlying bytes.Buffer (which is NOT concurrency-safe)
// must be guarded.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// startTestServer builds a Server bound to an ephemeral loopback port,
// starts it in a goroutine, waits for Addr() to be non-nil, and returns the
// server plus its "user:passcode" connection coordinates. The caller MUST
// defer Close.
func startTestServer(t *testing.T, passcode string) (*Server, string) {
	t.Helper()
	srv, err := NewServer(Config{
		Listen:   "127.0.0.1:0",
		Passcode: passcode,
		// HostKeyFile empty -> in-memory ed25519 key (no file dependency).
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	startErr := make(chan error, 1)
	go func() { startErr <- srv.Start() }()

	// Wait up to 2s for the listener to be bound.
	deadline := time.Now().Add(2 * time.Second)
	var addr net.Addr
	for time.Now().Before(deadline) {
		if addr = srv.Addr(); addr != nil {
			break
		}
		select {
		case err := <-startErr:
			t.Fatalf("server exited before binding: %v", err)
		default:
		}
		time.Sleep(5 * time.Millisecond)
	}
	if addr == nil {
		t.Fatalf("server did not bind within 2s")
	}
	return srv, addr.String()
}

// dialClient connects a golang.org/x/crypto/ssh client to the test server with
// the given password. HostKeyCallback is insecure-ignore because the test
// server uses a fresh random in-memory host key each run.
func dialClient(t *testing.T, addr, passcode string) *gossh.Client {
	t.Helper()
	cfg := &gossh.ClientConfig{
		User:            "anybody", // username is arbitrary; only passcode matters
		Auth:            []gossh.AuthMethod{gossh.Password(passcode)},
		HostKeyCallback: gossh.InsecureIgnoreHostKey(),
		Timeout:         2 * time.Second,
	}
	client, err := gossh.Dial("tcp", addr, cfg)
	if err != nil {
		t.Fatalf("ssh.Dial: %v", err)
	}
	return client
}

// TestSSHServer_EchoInteractive verifies the full round-trip: the client opens
// a PTY, gets an interactive shell, sends `echo hi-ssh` and reads it back from
// the PTY output. This proves the server allocates a real PTY and bridges SSH
// I/O to it in both directions.
func TestSSHServer_EchoInteractive(t *testing.T) {
	passcode := "test-passcode-echo"
	srv, addr := startTestServer(t, passcode)
	defer func() {
		_ = srv.Close()
	}()

	client := dialClient(t, addr, passcode)
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer session.Close()

	modes := gossh.TerminalModes{
		gossh.ECHO:          1,
		gossh.TTY_OP_ISPEED: 14400,
		gossh.TTY_OP_OSPEED: 14400,
	}
	if err := session.RequestPty("xterm-256color", 24, 80, modes); err != nil {
		t.Fatalf("RequestPty: %v", err)
	}

	stdin, err := session.StdinPipe()
	if err != nil {
		t.Fatalf("StdinPipe: %v", err)
	}
	// Use a thread-safe writer so the test does not race between the SSH
	// client's stdout and stderr copy goroutines. (They are launched as
	// separate goroutines by x/crypto/ssh when both fields are set.)
	out := &syncBuffer{}
	session.Stdout = out
	session.Stderr = out

	if err := session.Shell(); err != nil {
		t.Fatalf("Shell: %v", err)
	}

	// Send `echo <marker>` through the PTY and poll until the marker is echoed
	// back. This proves the SSH server allocated a real PTY and bridges I/O in
	// both directions. We deliberately do NOT wait for `session.Wait` / `exit`
	// here: the server process may use any $SHELL (e.g. an interactive zsh
	// with a fancy prompt theme) whose exit semantics are non-deterministic;
	// the PTY echo round-trip is the invariant we want to assert.
	marker := "hi-ssh-webshell"
	if _, err := io.WriteString(stdin, "echo "+marker+"\n"); err != nil {
		t.Fatalf("write echo: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(out.String(), marker) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	got := out.String()
	if !strings.Contains(got, marker) {
		t.Fatalf("expected stdout to contain %q within 5s; got:\n%s", marker, got)
	}

	// Tear down: closing stdin/the session causes the server-side shell to exit
	// and the session handler to clean up all its goroutines.
	_ = stdin.Close()
	_ = session.Close()
	_ = session.Wait()
}

// TestSSHServer_WrongPasscodeRejected verifies that a client presenting the
// wrong passcode cannot authenticate.
func TestSSHServer_WrongPasscodeRejected(t *testing.T) {
	srv, addr := startTestServer(t, "correct-passcode")
	defer func() {
		_ = srv.Close()
	}()

	cfg := &gossh.ClientConfig{
		User:            "anybody",
		Auth:            []gossh.AuthMethod{gossh.Password("WRONG-passcode")},
		HostKeyCallback: gossh.InsecureIgnoreHostKey(),
		Timeout:         2 * time.Second,
	}
	_, err := gossh.Dial("tcp", addr, cfg)
	if err == nil {
		t.Fatalf("ssh.Dial with wrong passcode succeeded; expected an auth error")
	}
	// Confirm it's an authentication failure rather than a network error.
	if !strings.Contains(err.Error(), "ssh") && !strings.Contains(err.Error(), "unable to authenticate") && !strings.Contains(err.Error(), "handshake") {
		t.Fatalf("expected ssh auth error, got: %v", err)
	}
}

// TestSSHServer_AddrAfterClose ensures Addr is safe to call after Close and
// Close is idempotent.
func TestSSHServer_AddrAfterClose(t *testing.T) {
	srv, addr := startTestServer(t, "p")
	if addr == "" {
		t.Fatalf("empty addr")
	}
	if err := srv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Idempotent close.
	if err := srv.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// TestSSHServer_NaturalExitDoesNotLeak is a regression test for a deadlock in
// the session handler's natural-exit path. When the shell exits on its own
// (user types `exit`), the server must tear down both copy goroutines and
// report the exit status to the client; the client's session.Wait() must
// return promptly. Previously the handler blocked on copyDone.Wait() before
// closing the SSH session, which deadlocked because the client->pty copy
// goroutine was stuck on s.Read until the session was closed — so
// session.Wait() hung forever.
//
// This test connects a real SSH client, requests a PTY, opens a shell, writes
// `exit\n` to stdin, and asserts that session.Wait() returns within 5s (i.e.
// no deadlock). We deliberately run the Wait in a goroutine guarded by a
// timer; if the deadlock regresses, the timer fires and the test fails
// loudly instead of hanging the whole suite.
func TestSSHServer_NaturalExitDoesNotLeak(t *testing.T) {
	passcode := "test-passcode-natural-exit"
	srv, addr := startTestServer(t, passcode)
	defer func() {
		_ = srv.Close()
	}()

	client := dialClient(t, addr, passcode)
	defer func() { _ = client.Close() }()

	session, err := client.NewSession()
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer func() { _ = session.Close() }()

	modes := gossh.TerminalModes{
		gossh.ECHO:          1,
		gossh.TTY_OP_ISPEED: 14400,
		gossh.TTY_OP_OSPEED: 14400,
	}
	if err := session.RequestPty("xterm-256color", 24, 80, modes); err != nil {
		t.Fatalf("RequestPty: %v", err)
	}

	// Discard stdout/stderr so the client's copy goroutines have somewhere to
	// write; we do not assert on output here — only on Wait() returning.
	session.Stdout = io.Discard
	session.Stderr = io.Discard

	stdin, err := session.StdinPipe()
	if err != nil {
		t.Fatalf("StdinPipe: %v", err)
	}

	if err := session.Shell(); err != nil {
		t.Fatalf("Shell: %v", err)
	}

	// Give the shell a moment to start, then tell it to exit. `exit\n` causes
	// the login shell to terminate, which makes cmd.Wait() on the server
	// return — the natural-exit path we are regression-testing.
	time.Sleep(150 * time.Millisecond)
	if _, err := io.WriteString(stdin, "exit\n"); err != nil {
		t.Fatalf("write exit: %v", err)
	}

	// Wait with a timeout: run session.Wait() in a goroutine and select
	// against a 5s timer. If the deadlock regresses, the timer wins and we
	// fail loudly.
	waitErr := make(chan error, 1)
	go func() { waitErr <- session.Wait() }()

	select {
	case err := <-waitErr:
		// session.Wait returned before the timeout — the regression is NOT
		// present. A nil or non-nil error are both acceptable: the exit code
		// of an interactive shell can vary, but the wait must RETURN.
		_ = err
	case <-time.After(5 * time.Second):
		t.Fatalf("session.Wait() did not return within 5s after `exit` — " +
			"session handler is deadlocked on teardown")
	}

	// Close stdin defensively; with `exit` already sent this is a no-op in
	// the happy path but keeps things tidy if the shell ignored `exit`.
	_ = stdin.Close()
}
