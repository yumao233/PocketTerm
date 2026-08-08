// Package pty implements the Webshell terminal session pool.
//
// Each terminal session is a real SSH session against the in-process local SSH
// server (see internal/ssh): the Manager dials that server with an SSH *client*
// (golang.org/x/crypto/ssh), opens a session with a PTY, runs the template's
// command (or an interactive shell), and bridges the SSH session's stdin/stdout
// so that multiple WebSocket subscribers can be attached to the same terminal.
//
// Broadcast model: the SSH session's Stdout/Stderr are wired to a writer that
// fans every byte chunk out to all subscribers on buffered channels (cap 64).
// When a subscriber's channel is full, the frame is DROPPED (with a log) so the
// pump never blocks on a slow reader. When the session ends (the remote command
// exits or the connection drops), Done() is closed and every subscriber channel
// is closed — so the downstream WS layer can detect session end by either
// selecting on Done() or observing a closed receive on its data channel.
package pty

import (
	"errors"
	"fmt"
	"io"
	"log"
	"sync"
	"time"

	ssh "golang.org/x/crypto/ssh" // crypto ssh client — aliased to avoid clashing with internal/ssh (the server)
)

// subscriberBuf is the per-subscriber channel buffer size. When full, frames are
// dropped (with a log) rather than blocking the broadcast pump.
const subscriberBuf = 64

// ErrSessionNotFound is returned by Manager methods when the requested session
// is not present in the live pool (unknown id, or already closed and removed).
var ErrSessionNotFound = errors.New("pty: session not found")

// Session wraps one SSH client+session to the local SSH server. It is created
// exclusively by the Manager and is safe for concurrent use.
//
// Lifecycle:
//   - newSession registers the SSH primitives.
//   - A pump goroutine (started by the Manager) calls sess.Wait(); when the
//     remote command/shell exits, it calls Close, which (via sync.Once) closes
//     stdin, the SSH session, the SSH client, and every subscriber channel, and
//     closes Done.
//   - WebSocket subscribers call Subscribe to get a buffered channel of output
//     bytes, and Unsubscribe when they detach. They may also select on Done() to
//     detect session end; once Done is closed, the data channel will also be
//     closed imminently.
type Session struct {
	ID         string
	UserID     string
	TemplateID string
	Title      string
	CreatedAt  time.Time

	client *ssh.Client
	sess   *ssh.Session
	stdin  io.WriteCloser

	// sizer, when set, applies window-size changes directly to the local SSH
	// server's PTY (same process). Set by the Manager when the session is
	// created; nil in tests that do not wire the server.
	sizer PTYSizer

	mu          sync.RWMutex
	subscribers map[chan []byte]struct{}
	closed      bool
	closeOnce   sync.Once
	done        chan struct{} // closed when the session ends (Close runs)

	// Last successfully resized cols/rows, used for idempotent deduplication. The
	// frontend's trailing debounce may still emit redundant identical frames; every
	// identical resize would trigger backend sizer.Resize + SSH WindowChange,
	// causing bash to receive SIGWINCH. Identical sizes are skipped to avoid
	// meaningless SIGWINCH disruptive to bash readline. Initial value 0, so the
	// first resize is always sent (a terminal size is never 0).
	lastCols, lastRows int
}

// newSession constructs a Session from the SSH primitives the Manager dialed.
// Not exported: callers obtain a *Session via Manager.Get.
func newSession(id, userID, templateID, title string, createdAt time.Time, client *ssh.Client, sess *ssh.Session, stdin io.WriteCloser, sizer PTYSizer) *Session {
	s := &Session{
		ID:          id,
		UserID:      userID,
		TemplateID:  templateID,
		Title:       title,
		CreatedAt:   createdAt,
		client:      client,
		sess:        sess,
		stdin:       stdin,
		sizer:       sizer,
		subscribers: make(map[chan []byte]struct{}),
		done:        make(chan struct{}),
	}
	// Wire stdout+stderr to the broadcast writer. x/crypto/ssh invokes Write as
	// data arrives; we must copy the bytes before forwarding because the caller
	// reuses the buffer between calls.
	sess.Stdout = &broadcastWriter{s: s, label: "stdout"}
	sess.Stderr = &broadcastWriter{s: s, label: "stderr"}
	return s
}

// WriteStdin writes user input from the browser to the PTY stdin. It returns an
// error if the session has already been closed.
func (s *Session) WriteStdin(p []byte) (int, error) {
	s.mu.RLock()
	if s.closed {
		s.mu.RUnlock()
		return 0, errors.New("pty: session closed")
	}
	stdin := s.stdin
	s.mu.RUnlock()
	if stdin == nil {
		return 0, errors.New("pty: session has no stdin")
	}
	return stdin.Write(p)
}

// Subscribe returns a channel that receives stdout/stderr byte frames broadcast
// to all subscribers. The channel is buffered (cap subscriberBuf); when full, a
// frame is dropped (and logged) rather than blocking the pump.
//
// To detach, call Unsubscribe with the returned channel (it is closed). To
// detect session end, select on Done(); once the session ends all subscriber
// channels are closed, so a closed receive on the data channel also signals end.
//
// The returned channel is bidirectional (chan []byte) so it can be passed back
// to Unsubscribe without any conversion. Callers must only READ from it.
func (s *Session) Subscribe() chan []byte {
	ch := make(chan []byte, subscriberBuf)
	s.mu.Lock()
	if s.closed {
		// Already closed: hand back a closed channel so the caller's read
		// returns immediately. No need to track it in the map.
		s.mu.Unlock()
		close(ch)
		return ch
	}
	s.subscribers[ch] = struct{}{}
	s.mu.Unlock()
	return ch
}

// Unsubscribe removes and closes the given subscription channel. Safe to call
// with an already-removed channel (no-op). The channel must be one returned by
// Subscribe; passing any other channel is a no-op (it will not be found in the
// subscriber set and therefore never double-closed).
func (s *Session) Unsubscribe(ch chan []byte) {
	s.mu.Lock()
	if _, exists := s.subscribers[ch]; exists {
		delete(s.subscribers, ch)
		// Close under the lock so a concurrent Close cannot close it a second
		// time (Close also deletes before closing).
		close(ch)
	}
	s.mu.Unlock()
}

// Resize changes the PTY window size. If a direct in-process sizer is available
// (the local SSH server, same process as the Manager), it applies TIOCSWINSZ to
// the real PTY immediately — the SSH window-change request alone is not enough
// because gliderlabs/ssh v0.3.8 does not apply it to the actual PTY. The SSH
// WindowChange request is still sent afterwards so the server's own record (and
// any true-remote server in the future) stays in sync. Both calls are
// best-effort; an error from one does not block the other.
func (s *Session) Resize(cols, rows int) error {
	// Idempotent deduplication: repeated resizes of the same size are skipped. The
	// frontend's trailing debounce may still emit redundant identical frames; every
	// resize makes bash receive SIGWINCH (backend sizer.Resize + SSH WindowChange
	// doubly trigger the same PTY), and re-triggering on an identical size only
	// needlessly disrupts bash readline. A write lock guards the tiny "read last
	// value + compare + write new value" section; the actual resize is performed
	// outside the lock (not calling sizer.Resize/WindowChange while holding the
	// lock, to avoid contending with Close etc.).
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errors.New("pty: session closed")
	}
	if s.lastCols == cols && s.lastRows == rows {
		// Size unchanged: skip, do not send SIGWINCH. Need to confirm sess still
		// exists (the first identical frame is also safe in uninitialized
		// scenarios, since the initial value 0 will never equal a real terminal
		// size).
		s.mu.Unlock()
		return nil
	}
	sess := s.sess
	s.lastCols = cols
	s.lastRows = rows
	s.mu.Unlock()
	if sess == nil {
		return errors.New("pty: session has no ssh session")
	}

	// Direct in-process resize: keyed by the webshell session id, which the
	// Manager passes to the server via the WEBSESSION_ID env var (see Start) so
	// both ends share the same key without depending on SSH session-id access
	// that x/crypto/ssh does not expose on the client side.
	//
	// When the direct sizer is available (the local SSH server, same process) it
	// applies TIOCSWINSZ to the real PTY immediately AND we must NOT also send the
	// SSH WindowChange request. gliderlabs/ssh v0.3.8's session.handleRequests
	// pushes every window-change request onto an internal `winch` channel
	// (cap 1) that this server never consumes — we resize the PTY directly via
	// the registry to sidestep gliderlabs's racing s.Pty(). An unconsumed winch
	// fills its buffer after the first window-change; subsequent window-change
	// requests then block handleRequests, which stops draining the SSH channel's
	// incomingRequests, which (once that 16-deep buffer fills) blocks the
	// connection-wide mux read loop — stalling ALL channel data, including the
	// user's stdin. The direct Setsize already resizes the real PTY, so the SSH
	// window-change request is redundant for the local server. Only fall back to
	// WindowChange when there is no direct sizer (a future true-remote server has
	// no in-process PTY to resize and must rely on the SSH request).
	if s.sizer != nil && s.ID != "" {
		_ = s.sizer.Resize(s.ID, rows, cols)
		return nil
	}

	// No direct in-process sizer: fall back to the standard SSH window-change
	// request (this is what a true-remote server would rely on).
	if err := sess.WindowChange(rows, cols); err != nil {
		return fmt.Errorf("pty: window change: %w", err)
	}
	return nil
}

// Close terminates the session: closes stdin, the SSH session, the SSH client,
// and every subscriber channel, and closes Done. It is idempotent (sync.Once)
// so the pump goroutine and an explicit Kill may both call it safely.
func (s *Session) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		// Close all subscriber channels under the lock so Unsubscribe (which
		// also closes under the lock) cannot double-close. Map keys are
		// chan []byte, so close is valid here.
		for ch := range s.subscribers {
			delete(s.subscribers, ch)
			close(ch)
		}
		stdin := s.stdin
		sess := s.sess
		client := s.client
		s.mu.Unlock()

		if stdin != nil {
			_ = stdin.Close()
		}
		if sess != nil {
			_ = sess.Close()
		}
		if client != nil {
			_ = client.Close()
		}
		close(s.done)
	})
	return nil
}

// Closed reports whether Close has run.
func (s *Session) Closed() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.closed
}

// Done returns a channel that is closed when the session ends (Close runs).
// Downstream WS layers should select on both their data channel and Done to
// detect session end promptly and unmount without leaking.
func (s *Session) Done() <-chan struct{} {
	return s.done
}

// broadcast copies p (ssh.Session reuses its buffer between Write calls, so the
// copy is mandatory) and non-blockingly sends it to every subscriber. A full
// channel causes the frame to be dropped with a log; this never blocks the pump.
//
// Send is wrapped in a recover: Close may run concurrently and close subscriber
// channels between the snapshot taken under the read lock and the send loop.
// Sending on a closed channel panics in Go, so we recover silently — the
// subscriber is gone, the frame is simply not delivered to it.
func (s *Session) broadcast(p []byte, label string) {
	cp := make([]byte, len(p))
	copy(cp, p)

	s.mu.RLock()
	if s.closed {
		s.mu.RUnlock()
		return
	}
	subs := make([]chan []byte, 0, len(s.subscribers))
	for ch := range s.subscribers {
		subs = append(subs, ch)
	}
	s.mu.RUnlock()

	for _, ch := range subs {
		if !safeSend(ch, cp) {
			// Either the channel was full (dropped) or it was closed between
			// the snapshot and the send (subscriber is gone). Either way we log
			// so a noisy subscriber is visible; the pump is never blocked.
			log.Printf("pty: %s output dropped for a subscriber (channel full or closed)", label)
		}
	}
}

// safeSend attempts a non-blocking send on ch and recovers from the panic of
// sending on a closed channel. Returns true if sent, false if dropped (full) or
// closed (recovered).
func safeSend(ch chan []byte, p []byte) (sent bool) {
	defer func() { _ = recover() }()
	select {
	case ch <- p:
		return true
	default:
		return false
	}
}

// broadcastWriter is an io.Writer that fans every Write out to all subscribers
// via Session.broadcast. It is assigned to ssh.Session.Stdout and Stderr so that
// as data arrives over the SSH channel it is copied and broadcast.
type broadcastWriter struct {
	s     *Session
	label string
}

func (w *broadcastWriter) Write(p []byte) (int, error) {
	w.s.broadcast(p, w.label)
	return len(p), nil
}
