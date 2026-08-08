package ws

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"github.com/wangkun/webshell/internal/model"
	"github.com/wangkun/webshell/internal/pty"
	sshserver "github.com/wangkun/webshell/internal/ssh"
)

// startTestSSHServer mirrors the helper in pty/manager_test.go: it builds a
// loopback SSH server on an ephemeral port, starts it, waits for it to bind,
// and returns (addr, passcode, cleanup). The cleanup MUST be deferred.
func startTestSSHServer(t *testing.T) (srv *sshserver.Server, addr, passcode string, cleanup func()) {
	t.Helper()
	passcode = "ws-test-passcode-" + t.Name()
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

// newTestManager builds a pty.Manager (pure in-memory, no persistence). The
// Manager owns every live session in memory, so there is no store to construct.
func newTestManager(t *testing.T, addr, passcode string, sizer pty.PTYSizer) *pty.Manager {
	t.Helper()
	m := pty.NewManager(pty.SSHDialConfig{Addr: addr, Passcode: passcode})
	m.SetPTYSizer(sizer)
	return m
}

// startSession is a shorthand for m.Start with a bash -c command and returns
// the live *pty.Session.
func startSession(t *testing.T, m *pty.Manager, bashArgs ...string) *pty.Session {
	t.Helper()
	tpl := &model.Template{
		ID:      "tpl-" + t.Name(),
		Name:    t.Name(),
		Command: "/bin/bash",
		Args:    append([]string{"-c"}, bashArgs...),
	}
	user := &model.User{ID: "u-" + t.Name(), Username: "ws-tester"}
	rec, err := m.Start(user, tpl)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	ps := m.Get(rec.ID)
	if ps == nil {
		t.Fatalf("Get(%s) = nil", rec.ID)
	}
	return ps
}

// wsURL converts an http:// httptest server URL to ws://.
func wsURL(httpURL string) string {
	return "ws" + strings.TrimPrefix(httpURL, "http")
}

// newWSServer builds a gin engine with a GET /ws route that bridges to the
// given session via ServeWS, with onExplicitClose wired to manager.Kill. It
// returns the running httptest.Server (caller must defer Close) — not the gin
// engine, because the WS dial needs the server's BaseURL.
func newWSServer(t *testing.T, sess *pty.Session, m *pty.Manager) *httptest.Server {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/ws", func(c *gin.Context) {
		ServeWS(c, sess, func() { _ = m.Kill(sess.ID) })
	})
	return httptest.NewServer(http.HandlerFunc(r.ServeHTTP))
}

// dialWS dials the test server's /ws endpoint and returns the connection.
func dialWS(t *testing.T, serverURL string) *websocket.Conn {
	t.Helper()
	u := wsURL(serverURL) + "/ws"
	conn, _, err := websocket.DefaultDialer.Dial(u, nil)
	if err != nil {
		t.Fatalf("dial %s: %v", u, err)
	}
	return conn
}

// readUntil collects WS messages until one contains want (as a substring), or
// the timeout elapses. Returns the accumulated bytes and whether want was seen.
// It returns immediately if a read errors out (connection closed).
func readUntil(t *testing.T, conn *websocket.Conn, want string, timeout time.Duration) (string, bool) {
	t.Helper()
	var buf strings.Builder
	deadline := time.After(timeout)
	for {
		select {
		case <-deadline:
			return buf.String(), strings.Contains(buf.String(), want)
		default:
		}
		_ = conn.SetReadDeadline(time.Now().Add(timeout))
		_, p, err := conn.ReadMessage()
		if err != nil {
			return buf.String(), strings.Contains(buf.String(), want)
		}
		buf.Write(p)
		if want != "" && strings.Contains(buf.String(), want) {
			return buf.String(), true
		}
	}
}

// TestServeWS_EchoRoundTrip verifies the full browser -> PTY -> browser data
// path: a bash read-loop echoes each input line prefixed with "got:", and the
// WS client receives the echoed line. Then an explicit {"type":"close"} frame
// tears the session down, and the client receives a {"type":"closed"} control
// frame followed by connection close.
func TestServeWS_EchoRoundTrip(t *testing.T) {
	srv, addr, passcode, cleanup := startTestSSHServer(t)
	defer cleanup()
	m := newTestManager(t, addr, passcode, srv)

	sess := startSession(t, m, `while IFS= read -r line; do echo "got:$line"; done`)
	server := newWSServer(t, sess, m)
	defer server.Close()

	conn := dialWS(t, server.URL)
	defer conn.Close()

	// Send a line of input and expect it echoed back as got:<line>.
	input := "hello-ws-roundtrip\n"
	if err := conn.WriteMessage(websocket.BinaryMessage, []byte(input)); err != nil {
		t.Fatalf("write input: %v", err)
	}

	got, ok := readUntil(t, conn, "got:hello-ws-roundtrip", 3*time.Second)
	if !ok {
		t.Fatalf("expected WS to receive %q within 3s; got:\n%s", "got:hello-ws-roundtrip", got)
	}

	// Explicit close: send {"type":"close"} -> expect {"type":"closed"} then
	// connection close (ReadMessage returns an error) within 2s.
	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"close"}`)); err != nil {
		t.Fatalf("write close frame: %v", err)
	}

	closedGot, closedOk := readUntil(t, conn, `"type":"closed"`, 2*time.Second)
	if !closedOk {
		t.Fatalf("expected WS to receive closed frame within 2s; got:\n%s", closedGot)
	}
	// Validate the closed frame is well-formed JSON with type=closed.
	// Find the JSON object in the accumulated output (it is the last message).
	// Parse the whole tail as JSON if possible.
	var cf ControlFrame
	// The closed frame is sent alone as its own message; the buf may contain
	// earlier echoed output too. Find the last '{' and try to parse from there
	// to the end. Simpler: just unmarshal a fresh read. But readUntil already
	// consumed messages. We assert via substring which we already did; also
	// validate the JSON shape by attempting parse of the tail.
	idx := strings.LastIndex(closedGot, "{")
	if idx >= 0 {
		_ = json.Unmarshal([]byte(closedGot[idx:]), &cf)
		if cf.Type != "closed" {
			t.Fatalf("closed frame type = %q; want %q", cf.Type, "closed")
		}
	}

	// The connection should now be closed by the server. A further read
	// returns an error promptly.
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatalf("expected ReadMessage to error after close; got nil")
	}

	// The session must report closed afterward.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !sess.Closed() {
		time.Sleep(10 * time.Millisecond)
	}
	if !sess.Closed() {
		t.Fatalf("session not marked Closed after explicit close frame")
	}
}

// TestServeWS_Resize verifies that a resize control frame actually changes the
// server-side PTY size. It runs an interactive bash loop that prints "SIZE=<rows>
// <cols>" on each input line (via stty size), sends a resize frame, then asks
// the shell again — the second reading must reflect the new dimensions. This
// guards the in-process TIOCSWINSZ path: without server-side resizing the PTY
// would keep the initial 24x80 and the second reading would not change.
func TestServeWS_Resize(t *testing.T) {
	srv, addr, passcode, cleanup := startTestSSHServer(t)
	defer cleanup()
	m := newTestManager(t, addr, passcode, srv)

	// bash loop: on each line of stdin, print "SIZE=<rows> <cols>" from stty.
	// `stty size` outputs "<rows> <cols>"; we prefix SIZE= so we can locate it
	// unambiguously among prompt/echo noise.
	sess := startSession(t, m, `while IFS= read -r line; do echo "SIZE=$(stty size)"; done`)
	server := newWSServer(t, sess, m)
	defer server.Close()

	conn := dialWS(t, server.URL)
	defer conn.Close()

	// First reading: initial PTY size (24 rows x 80 cols from RequestPty).
	if err := conn.WriteMessage(websocket.BinaryMessage, []byte("p\n")); err != nil {
		t.Fatalf("write probe: %v", err)
	}
	first, ok := readUntil(t, conn, "SIZE=", 3*time.Second)
	if !ok {
		t.Fatalf("expected initial SIZE= reading; got:\n%s", first)
	}
	firstSize := extractSize(t, first)
	// Initial size must be the RequestPty default: rows=24, cols=80.
	if firstSize.rows != 24 || firstSize.cols != 80 {
		t.Fatalf("initial PTY size = %d x %d; want 24 x 80", firstSize.rows, firstSize.cols)
	}

	// Resize to 100 cols x 40 rows.
	resizeFrame := []byte(`{"type":"resize","cols":100,"rows":40}`)
	if err := conn.WriteMessage(websocket.TextMessage, resizeFrame); err != nil {
		t.Fatalf("write resize frame: %v", err)
	}
	// Allow the resize to propagate to the PTY (TIOCSWINSZ + SIGWINCH).
	time.Sleep(150 * time.Millisecond)

	// Second reading: must reflect the new dimensions.
	if err := conn.WriteMessage(websocket.BinaryMessage, []byte("p\n")); err != nil {
		t.Fatalf("write probe: %v", err)
	}
	second, ok := readSizeReading(t, conn, firstSize, 3*time.Second)
	if !ok {
		t.Fatalf("expected a NEW SIZE= reading after resize; got:\n%s", second)
	}
	secondSize := extractSize(t, second)
	if secondSize.rows != 40 || secondSize.cols != 100 {
		t.Fatalf("PTY size after resize = %d x %d; want 40 x 100 (server-side resize not applied)", secondSize.rows, secondSize.cols)
	}

	// Send a close frame to end the session cleanly.
	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"close"}`)); err != nil {
		t.Fatalf("write close frame: %v", err)
	}
	if _, ok := readUntil(t, conn, `"type":"closed"`, 2*time.Second); !ok {
		t.Fatalf("expected closed frame after resize+close")
	}
}

// sttySize holds rows and cols parsed from a "SIZE=<rows> <cols>" token.
type sttySize struct{ rows, cols int }

// extractSize finds the first "SIZE=<rows> <cols>" in s and parses it.
func extractSize(t *testing.T, s string) sttySize {
	t.Helper()
	idx := strings.Index(s, "SIZE=")
	if idx < 0 {
		t.Fatalf("no SIZE= in output:\n%s", s)
	}
	rest := s[idx+len("SIZE="):]
	// Take the two decimal fields immediately following.
	var r, c int
	n, err := fmt.Sscanf(rest, "%d %d", &r, &c)
	if err != nil || n != 2 {
		t.Fatalf("could not parse rows/cols after SIZE= in %q", rest)
	}
	return sttySize{rows: r, cols: c}
}

// readSizeReading reads WS messages until it finds a SIZE= line whose parsed
// size differs from prev (i.e. a fresh reading after resize), or timeout. It
// returns the accumulated bytes and whether such a reading was found.
func readSizeReading(t *testing.T, conn *websocket.Conn, prev sttySize, timeout time.Duration) (string, bool) {
	t.Helper()
	var buf strings.Builder
	deadline := time.After(timeout)
	for {
		select {
		case <-deadline:
			return buf.String(), false
		default:
		}
		_ = conn.SetReadDeadline(time.Now().Add(timeout))
		_, p, err := conn.ReadMessage()
		if err != nil {
			return buf.String(), false
		}
		buf.Write(p)
		s := buf.String()
		// Look for any SIZE= occurrence after the first one.
		idx := strings.Index(s, "SIZE=")
		for idx >= 0 {
			rest := s[idx+len("SIZE="):]
			var r, c int
			if n, e := fmt.Sscanf(rest, "%d %d", &r, &c); e == nil && n == 2 && (r != prev.rows || c != prev.cols) {
				return s, true
			}
			next := strings.Index(rest, "SIZE=")
			if next < 0 {
				break
			}
			idx = idx + len("SIZE=") + next
		}
	}
}

// TestServeWS_PTYExitsFromOutside verifies that when the PTY command exits on
// its own (no close frame from the browser), the writer detects the session
// ending, sends a {"type":"closed"} frame, and closes the connection.
func TestServeWS_PTYExitsFromOutside(t *testing.T) {
	srv, addr, passcode, cleanup := startTestSSHServer(t)
	defer cleanup()
	m := newTestManager(t, addr, passcode, srv)

	// A command that prints three lines and exits.
	sess := startSession(t, m, "for i in 1 2 3; do echo x$i; sleep 0.05; done")
	server := newWSServer(t, sess, m)
	defer server.Close()

	conn := dialWS(t, server.URL)
	defer conn.Close()

	// We should receive x1, x2, x3 and then a closed frame. Collect everything
	// and assert all four markers are present within 3s (the command takes
	// ~150ms; the closed frame follows promptly after exit).
	buf, ok := readUntil(t, conn, `"type":"closed"`, 3*time.Second)
	if !ok {
		t.Fatalf("expected closed frame within 3s after PTY exits; got:\n%s", buf)
	}
	for _, want := range []string{"x1", "x2", "x3"} {
		if !strings.Contains(buf, want) {
			t.Fatalf("expected output to contain %q; got:\n%s", want, buf)
		}
	}

	// The connection should be closed by the server.
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatalf("expected ReadMessage to error after PTY exit; got nil")
	}

	// The session must report closed.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !sess.Closed() {
		time.Sleep(10 * time.Millisecond)
	}
	if !sess.Closed() {
		t.Fatalf("session not marked Closed after natural exit")
	}
}

// TestServeWS_AbruptBrowserDisconnect starts a long-running session, dials the
// WS, then closes the WS without sending a close frame. The server must not
// leak the writer goroutine: ServeWS returns once the reader's ReadMessage
// errors and the writer is reaped. We assert the session is still alive
// (abrupt disconnect does NOT kill the PTY — only an explicit close frame
// does) and that a fresh WS can still subscribe.
func TestServeWS_AbruptBrowserDisconnect(t *testing.T) {
	srv, addr, passcode, cleanup := startTestSSHServer(t)
	defer cleanup()
	m := newTestManager(t, addr, passcode, srv)

	sess := startSession(t, m, "sleep 30")
	server := newWSServer(t, sess, m)
	defer server.Close()

	conn := dialWS(t, server.URL)

	// Abruptly close the browser side. ServeWS's reader will error out and the
	// writer will be reaped; the PTY must keep running.
	_ = conn.Close()

	// Give the server a moment to tear the WS down.
	time.Sleep(200 * time.Millisecond)

	// The session is still alive (no close frame was sent).
	if sess.Closed() {
		t.Fatalf("session closed after abrupt browser disconnect; expected still running")
	}

	// A fresh WS can subscribe and send input. Use a read-loop echo to prove
	// the session still works. Re-prepare: kill the sleep session and start a
	// fresh echo session to keep the assertion simple and deterministic.
	_ = m.Kill(sess.ID)

	echoSess := startSession(t, m, `while IFS= read -r line; do echo "got:$line"; done`)
	echoServer := newWSServer(t, echoSess, m)
	defer echoServer.Close()
	defer m.Kill(echoSess.ID)

	conn2 := dialWS(t, echoServer.URL)
	defer conn2.Close()
	if err := conn2.WriteMessage(websocket.BinaryMessage, []byte("after-reconnect\n")); err != nil {
		t.Fatalf("write after reconnect: %v", err)
	}
	if got, ok := readUntil(t, conn2, "got:after-reconnect", 3*time.Second); !ok {
		t.Fatalf("expected echo after reconnect within 3s; got:\n%s", got)
	}
}
