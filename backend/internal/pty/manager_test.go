package pty

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/wangkun/webshell/internal/model"
	sshserver "github.com/wangkun/webshell/internal/ssh"
)

// startTestSSHServer builds a local SSH server bound to an ephemeral loopback
// port, starts it in a goroutine, waits for it to be bound, and returns its
// address, the passcode, and a cleanup function the caller MUST defer.
func startTestSSHServer(t *testing.T) (srv *sshserver.Server, addr, passcode string, cleanup func()) {
	t.Helper()
	passcode = "pty-test-passcode-" + t.Name()
	srv, err := sshserver.NewServer(sshserver.Config{
		Listen:   "127.0.0.1:0",
		Passcode: passcode,
	})
	if err != nil {
		t.Fatalf("sshserver.NewServer: %v", err)
	}

	startErr := make(chan error, 1)
	go func() { startErr <- srv.Start() }()

	// Wait up to 2s for the listener to bind.
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

// newTestManager builds a Manager (pure in-memory, no persistence) backed by
// the test SSH server. sizer (typically the local SSH server, same process)
// wires direct in-process PTY resizing; pass nil to fall back to the SSH
// window-change request only.
func newTestManager(t *testing.T, addr, passcode string, sizer PTYSizer) *Manager {
	t.Helper()
	m := NewManager(SSHDialConfig{Addr: addr, Passcode: passcode})
	m.SetPTYSizer(sizer)
	return m
}

// drainChannel collects everything received on ch until either want is contained
// in the accumulated output, or the timeout elapses. Returns the accumulated
// bytes and whether want was seen.
func drainChannel(t *testing.T, ch <-chan []byte, want string, timeout time.Duration) (string, bool) {
	t.Helper()
	var buf bytes.Buffer
	deadline := time.After(timeout)
	for {
		select {
		case p, ok := <-ch:
			if !ok {
				// Channel closed (session ended). Return what we have.
				return buf.String(), strings.Contains(buf.String(), want)
			}
			buf.Write(p)
			if want != "" && strings.Contains(buf.String(), want) {
				return buf.String(), true
			}
		case <-deadline:
			return buf.String(), strings.Contains(buf.String(), want)
		}
	}
}

// pollSessionStatus polls the in-memory pool until the session's observable
// state matches want, or timeout elapses. With the purely in-memory model a
// session is either live in the pool (status "running") or removed entirely
// (status "closed" — the pump's natural-exit path and Kill both delete it).
// Returns the final status: "running" while still in the pool, "closed" once
// removed.
func pollSessionStatus(t *testing.T, m *Manager, id, want string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if rec := m.GetMeta(id); rec != nil {
			if want == "running" {
				return "running"
			}
		} else {
			// Removed from the pool — natural exit or Kill.
			return "closed"
		}
		time.Sleep(20 * time.Millisecond)
	}
	if rec := m.GetMeta(id); rec != nil {
		return "running"
	}
	return "closed"
}

// TestManager_StartAndOutput verifies the full chain: Start dials the local SSH
// server, runs `bash -c "for i in 1 2 3; do echo line$i; ..."`, and the
// subscribed channel receives line1..line3. When the command exits naturally
// the pump calls Close and deletes the session from the in-memory pool
// (pollSessionStatus then reports "closed" — i.e. GetMeta returns nil).
func TestManager_StartAndOutput(t *testing.T) {
	srv, addr, passcode, cleanup := startTestSSHServer(t)
	defer cleanup()
	m := newTestManager(t, addr, passcode, srv)

	tpl := &model.Template{
		ID:      "t1",
		Name:    "echo-test",
		Command: "/bin/bash",
		Args:    []string{"-c", "for i in 1 2 3; do echo line$i; sleep 0.05; done"},
	}
	user := &model.User{ID: "u1", Username: "tester"}
	rec, err := m.Start(user, tpl)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	ps := m.Get(rec.ID)
	if ps == nil {
		t.Fatalf("Get(%s) = nil; want live session", rec.ID)
	}
	ch := ps.Subscribe()
	defer ps.Unsubscribe(ch)

	got, ok := drainChannel(t, ch, "line3", 2*time.Second)
	if !ok {
		t.Fatalf("expected output to contain line3 within 2s; got:\n%s", got)
	}
	for _, want := range []string{"line1", "line2", "line3"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected output to contain %q; got:\n%s", want, got)
		}
	}

	// Command exits naturally -> pump calls Close and deletes the session from the pool.
	if status := pollSessionStatus(t, m, rec.ID, "closed", time.Second); status != "closed" {
		t.Fatalf("expected session gone from pool (status closed) after natural exit; got %q", status)
	}
	// The live Session is also Closed (Kill and the natural-exit pump both Close it
	// before removing it from the pool).
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && !ps.Closed() {
		time.Sleep(10 * time.Millisecond)
	}
	if !ps.Closed() {
		t.Fatalf("session not marked Closed after natural exit")
	}
}

// TestManager_Kill starts a long-running command and verifies Kill closes the
// session and removes it from the in-memory pool (pollSessionStatus then
// reports "closed").
func TestManager_Kill(t *testing.T) {
	srv, addr, passcode, cleanup := startTestSSHServer(t)
	defer cleanup()
	m := newTestManager(t, addr, passcode, srv)

	tpl := &model.Template{
		ID:      "t2",
		Name:    "long-runner",
		Command: "/bin/bash",
		Args:    []string{"-c", "sleep 30"},
	}
	user := &model.User{ID: "u2", Username: "tester2"}
	rec, err := m.Start(user, tpl)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	if err := m.Kill(rec.ID); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if m.Get(rec.ID) != nil {
		t.Fatalf("Get after Kill = non-nil; want nil (removed from pool)")
	}
	if status := pollSessionStatus(t, m, rec.ID, "closed", time.Second); status != "closed" {
		t.Fatalf("expected session gone from pool (status closed) after Kill; got %q", status)
	}
}

// TestManager_KillNotFound verifies Kill on an unknown id returns
// ErrSessionNotFound.
func TestManager_KillNotFound(t *testing.T) {
	srv, addr, passcode, cleanup := startTestSSHServer(t)
	defer cleanup()
	m := newTestManager(t, addr, passcode, srv)

	if err := m.Kill("does-not-exist"); err != ErrSessionNotFound {
		t.Fatalf("Kill(unknown) = %v; want ErrSessionNotFound", err)
	}
}

// TestManager_WriteStdinPing verifies that input written to the PTY stdin is
// echoed back on the broadcast channel. It runs a bash read-loop that prints
// each input line prefixed with "got:" — the marker is produced by the command
// itself (not by relying on kernel PTY echo, which is unreliable when bash
// execs a subprocess via -c). This is the most stable way to assert the
// stdin->PTY->stdout round-trip end to end.
func TestManager_WriteStdinPing(t *testing.T) {
	srv, addr, passcode, cleanup := startTestSSHServer(t)
	defer cleanup()
	m := newTestManager(t, addr, passcode, srv)

	tpl := &model.Template{
		ID:      "t3",
		Name:    "read-loop-echo",
		Command: "/bin/bash",
		Args:    []string{"-c", `while IFS= read -r line; do echo "got:$line"; done`},
	}
	user := &model.User{ID: "u3", Username: "tester3"}
	rec, err := m.Start(user, tpl)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	ps := m.Get(rec.ID)
	if ps == nil {
		t.Fatalf("Get = nil")
	}
	ch := ps.Subscribe()
	defer ps.Unsubscribe(ch)

	marker := "hello-pty-from-stdin"
	if _, err := ps.WriteStdin([]byte(marker + "\n")); err != nil {
		t.Fatalf("WriteStdin: %v", err)
	}

	got, ok := drainChannel(t, ch, "got:"+marker, 3*time.Second)
	if !ok {
		t.Fatalf("expected stdout to contain %q within 3s; got:\n%s", "got:"+marker, got)
	}
	_ = m.Kill(rec.ID)
}

// TestManager_Resize verifies Resize on a live session returns no error.
func TestManager_Resize(t *testing.T) {
	srv, addr, passcode, cleanup := startTestSSHServer(t)
	defer cleanup()
	m := newTestManager(t, addr, passcode, srv)

	tpl := &model.Template{
		ID:      "t4",
		Name:    "resize-target",
		Command: "/bin/bash",
		Args:    []string{"-c", "sleep 30"},
	}
	user := &model.User{ID: "u4", Username: "tester4"}
	rec, err := m.Start(user, tpl)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := m.Resize(rec.ID, 100, 40); err != nil {
		t.Fatalf("Resize: %v", err)
	}
	_ = m.Kill(rec.ID)
}

// TestManager_ResizeNotFound verifies Resize on an unknown id returns
// ErrSessionNotFound.
func TestManager_ResizeNotFound(t *testing.T) {
	srv, addr, passcode, cleanup := startTestSSHServer(t)
	defer cleanup()
	m := newTestManager(t, addr, passcode, srv)

	if err := m.Resize("nope", 80, 24); err != ErrSessionNotFound {
		t.Fatalf("Resize(unknown) = %v; want ErrSessionNotFound", err)
	}
}

// TestManager_MultipleSubscribers verifies two subscribers to the same session
// both receive output from the broadcast.
func TestManager_MultipleSubscribers(t *testing.T) {
	srv, addr, passcode, cleanup := startTestSSHServer(t)
	defer cleanup()
	m := newTestManager(t, addr, passcode, srv)

	tpl := &model.Template{
		ID:      "t5",
		Name:    "multi-sub",
		Command: "/bin/bash",
		Args:    []string{"-c", "for i in 1 2 3; do echo x$i; sleep 0.05; done"},
	}
	user := &model.User{ID: "u5", Username: "tester5"}
	rec, err := m.Start(user, tpl)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	ps := m.Get(rec.ID)
	if ps == nil {
		t.Fatalf("Get = nil")
	}
	ch1 := ps.Subscribe()
	ch2 := ps.Subscribe()
	defer ps.Unsubscribe(ch1)
	defer ps.Unsubscribe(ch2)

	got1, ok1 := drainChannel(t, ch1, "x1", time.Second)
	got2, ok2 := drainChannel(t, ch2, "x1", time.Second)
	if !ok1 {
		t.Fatalf("subscriber 1 did not receive output within 1s; got:\n%s", got1)
	}
	if !ok2 {
		t.Fatalf("subscriber 2 did not receive output within 1s; got:\n%s", got2)
	}
	// Wait for natural exit + store update so the test leaves no live session.
	pollSessionStatus(t, m, rec.ID, "closed", time.Second)
}

// TestManager_CwdHonored verifies that when a template specifies Cwd, the
// command runs in that directory: `bash -c "pwd"` started with Cwd=/tmp should
// print /tmp (or a symlink-resolved variant on macOS like /private/tmp).
func TestManager_CwdHonored(t *testing.T) {
	srv, addr, passcode, cleanup := startTestSSHServer(t)
	defer cleanup()
	m := newTestManager(t, addr, passcode, srv)

	dir := t.TempDir()
	tpl := &model.Template{
		ID:      "t6",
		Name:    "cwd-test",
		Command: "/bin/bash",
		Args:    []string{"-c", "pwd"},
		Cwd:     dir,
	}
	user := &model.User{ID: "u6", Username: "tester6"}
	rec, err := m.Start(user, tpl)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	ps := m.Get(rec.ID)
	if ps == nil {
		t.Fatalf("Get = nil")
	}
	ch := ps.Subscribe()
	defer ps.Unsubscribe(ch)

	got, _ := drainChannel(t, ch, "", 2*time.Second)
	// macOS resolves /tmp -> /private/tmp; the temp dir may be either path.
	// Assert the directory's basename appears (the temp dir base is unique).
	base := dir
	if i := strings.LastIndex(dir, "/"); i >= 0 {
		base = dir[i+1:]
	}
	if base == "" {
		t.Fatalf("could not derive basename from %q", dir)
	}
	if !strings.Contains(got, base) {
		t.Fatalf("expected pwd output to contain temp dir basename %q; got:\n%s", base, got)
	}
	pollSessionStatus(t, m, rec.ID, "closed", time.Second)
}
