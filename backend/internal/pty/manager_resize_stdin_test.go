package pty

import (
	"sync"
	"testing"
	"time"

	"github.com/wangkun/webshell/internal/model"
)

// TestManager_FrequentResizeKeepsStdinWorking is the regression guard for a bug
// where frequent Resize calls stalled the user's stdin.
//
// Root cause (fixed in Session.Resize): when a direct in-process sizer was
// available, Session.Resize applied TIOCSWINSZ to the real PTY AND ALSO sent the
// SSH WindowChange request. The local SSH server (gliderlabs/ssh v0.3.8) pushes
// every window-change request onto an internal `winch` channel (cap 1) that this
// server never consumes — it resizes the PTY directly via the registry to avoid
// gliderlabs's racing s.Pty(). That unconsumed channel fills after the first
// window-change; subsequent window-change requests block session.handleRequests,
// which stops draining the SSH channel's incomingRequests buffer (cap 16), which
// (once full) blocks the connection-wide mux read loop — stalling ALL channel
// data on that session, including the user's stdin. Even though WriteStdin kept
// returning success (the bytes entered the SSH client's send buffer), the server
// never read them, so bash's read-loop never echoed them back.
//
// Fix: when the direct sizer is present, do NOT send WindowChange — the direct
// TIOCSWINSZ already resizes the real PTY. This test hammers Resize with many
// varying sizes (so the session's lastCols/lastRows dedup never skips a frame)
// and asserts the stdin->PTY->stdout round-trip still works during and after the
// storm. Before the fix it failed (WriteStdin's echo never arrived within 5s);
// after the fix it passes in ~0.1s.
func TestManager_FrequentResizeKeepsStdinWorking(t *testing.T) {
	srv, addr, passcode, cleanup := startTestSSHServer(t)
	defer cleanup()
	m := newTestManager(t, addr, passcode, srv)

	tpl := &model.Template{
		ID:      "resize-stdin",
		Name:    "resize-stdin-regression",
		Command: "/bin/bash",
		Args:    []string{"-c", `while IFS= read -r line; do echo "got:$line"; done`},
	}
	user := &model.User{ID: "u-regression", Username: "tester"}
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

	// Drain the bash startup banner / prompt so later reads are clean.
 drainStartup:
	for {
		select {
		case <-ch:
		case <-time.After(100 * time.Millisecond):
			break drainStartup
		}
	}

	// Baseline: a stdin write BEFORE any resize must echo back. If this fails the
	// test environment itself is broken, not the regression.
	marker0 := "before-resize"
	if _, err := ps.WriteStdin([]byte(marker0 + "\n")); err != nil {
		t.Fatalf("WriteStdin(baseline): %v", err)
	}
	if _, ok := drainChannel(t, ch, "got:"+marker0, 3*time.Second); !ok {
		t.Fatalf("baseline stdin round-trip already broken (no resize yet) — test env issue")
	}

	// Hammer Resize with MANY different sizes on a hot loop, mimicking a user
	// dragging the window / ResizeObserver firing rapidly. Sizes vary so the
	// session's lastCols/lastRows dedup never skips a frame (which would mask the
	// bug by reducing the number of SSH window-change requests).
	const resizeN = 200
	var wg sync.WaitGroup
	stopResize := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < resizeN; i++ {
			select {
			case <-stopResize:
				return
			default:
			}
			cols := 30 + (i % 200) // 30..229
			rows := 10 + (i % 80)  // 10..89
			_ = ps.Resize(cols, rows)
		}
	}()
	// Let the resize storm get ahead of the stdin write.
	time.Sleep(20 * time.Millisecond)

	// While the resize storm is in flight, write to stdin and assert the
	// round-trip survives. This is exactly where the bug manifested as a missing
	// echo (the bytes were sent but never reached bash).
	marker := "after-resize-marker"
	writeDone := make(chan error, 1)
	go func() {
		_, err := ps.WriteStdin([]byte(marker + "\n"))
		writeDone <- err
	}()
	select {
	case <-writeDone:
		// WriteStdin returned (didn't block) — good. Now check the echo.
	case <-time.After(5 * time.Second):
		close(stopResize)
		wg.Wait()
		t.Fatalf("WriteStdin did not return within 5s during frequent resize — writer blocked?")
	}
	close(stopResize)
	wg.Wait()

	// Bash readline under a PTY may also echo the typed marker; assert the
	// command's explicit "got:" echo shows up.
	got, ok := drainChannel(t, ch, "got:"+marker, 5*time.Second)
	if !ok {
		t.Fatalf("stdin round-trip BROKEN after frequent resize: expected %q within 5s; got:\n%s",
			"got:"+marker, got)
	}
	_ = m.Kill(rec.ID)
}