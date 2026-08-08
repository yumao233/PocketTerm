package ws

import (
	"net/http"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"github.com/wangkun/webshell/internal/pty"
)

// upgrader promotes the HTTP connection to a WebSocket. CheckOrigin accepts all
// origins: cross-origin policy is enforced by the CORS middleware on the HTTP
// layer (the WS handshake carries Origin but cannot set custom headers from the
// browser, so a strict CheckOrigin here would only break legitimate same-site
// deployments behind a proxy).
var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

// closedFrame is the terminal control frame sent to the browser when the PTY
// session ends. It is JSON so the frontend can parse type=="closed" and tear
// down its xterm.js view. Sent as a TextMessage (control frames are JSON; raw
// terminal bytes use BinaryMessage).
var closedFrame = []byte(`{"type":"closed","reason":"session ended"}`)

// ServeWS upgrades the HTTP connection to a WebSocket and bridges it to the
// given pty.Session. It blocks until either the WebSocket or the PTY session
// ends, then tears the bridge down (sending a single {"type":"closed"} control
// frame to the browser and closing the WS).
//
// onExplicitClose is invoked when the browser sends a {"type":"close"} control
// frame — the caller wires this to manager.Kill(sessionID) so the PTY is
// actually destroyed. ServeWS does NOT call Kill itself on natural session end:
// when the PTY exits on its own the pump has already called Session.Close, and
// the writer detects the closed subscriber channel.
//
// Goroutine model (no leaks, no concurrent writers):
//
//	reader  — runs on the calling goroutine. Reads WS frames, classifies them,
//	          writes data to the PTY stdin, calls onExplicitClose on a close
//	          control frame. NEVER writes to the conn.
//	writer  — a goroutine. Owns ALL conn writes: data frames from the PTY
//	          broadcast channel, plus the terminal {"type":"closed"} control
//	          frame it emits when the session ends. Also closes the conn when
//	          the PTY ends so the reader's ReadMessage unblocks.
//
// Coordination: a sync.WaitGroup joins the writer with the reader's exit, so
// ServeWS returns only after the writer has fully drained — guaranteeing no
// goroutine leak even when the browser disconnects abruptly. The reader does
// NOT use a context to unblock ReadMessage (gorilla/websocket ReadMessage is
// not context-aware); instead the writer closes the conn when the PTY ends,
// which makes the reader's ReadMessage return an error.
func ServeWS(c *gin.Context, sess *pty.Session, onExplicitClose func()) {
	// Upgrade. On error gin/gorilla have already written an HTTP response, so
	// there is nothing more to do.
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}

	// Subscribe to PTY output before starting the writer so no early frames are
	// lost. Unsubscribe on exit so the session's subscriber map stays clean
	// even if the session outlives this WS connection.
	ch := sess.Subscribe()
	defer sess.Unsubscribe(ch)

	// stop is closed by the reader when it exits (browser disconnected or the
	// writer tore the conn down after the PTY ended). The writer selects on it
	// so it can return promptly even when the PTY is still alive and its
	// subscriber channel is still open — without this, an abrupt browser
	// disconnect would leave the writer blocked forever on <-ch, leaking the
	// goroutine.
	stop := make(chan struct{})

	// writerDone joins the writer goroutine with the reader's exit. ServeWS
	// must not return before the writer has stopped touching conn (otherwise a
	// gin recovery / response flush could race a final WriteMessage).
	var writerDone sync.WaitGroup
	writerDone.Add(1)

	// writer owns every conn.WriteMessage call (gorilla/websocket forbids
	// concurrent writers, so the reader must NOT touch conn). When the PTY
	// session ends the subscriber channel is closed (ok==false); the writer
	// then sends exactly one {"type":"closed"} frame, closes the conn to
	// unblock the reader's ReadMessage, and returns. The closed frame is sent
	// only here, so it is sent exactly once. The writer also selects on stop so
	// it can exit when the browser disconnects while the PTY is still alive.
	writer := func() {
		defer writerDone.Done()
		for {
			select {
			case <-stop:
				// Reader is gone (browser disconnected). No point writing; just
				// exit. Do NOT send a closed frame — the browser is gone, and
				// the PTY keeps running (abrupt disconnect does not kill it;
				// only an explicit {"type":"close"} frame does).
				return
			case data, ok := <-ch:
				if !ok {
					// Session ended (pump called Close, which closed every
					// subscriber channel, OR an explicit close frame triggered
					// manager.Kill -> Session.Close). Emit the terminal control
					// frame best-effort, then close the conn to unblock the
					// reader's ReadMessage. conn.Close is safe to call from
					// here: gorilla/websocket allows Close concurrently with a
					// blocked ReadMessage (Close sets a deadline / closes the
					// underlying fd), and the reader recovers from the
					// resulting error.
					_ = conn.WriteMessage(websocket.TextMessage, closedFrame)
					_ = conn.Close()
					return
				}
				if werr := conn.WriteMessage(websocket.BinaryMessage, data); werr != nil {
					// Write failed (browser gone). The reader will see a read
					// error too; nothing more to write. Closing the conn here
					// is redundant but harmless. Close stop so the writer
					// cannot be re-entered (it is about to return). No need to
					// close stop — the reader will close it on its way out.
					return
				}
			}
		}
	}
	go writer()

	// reader runs on the calling goroutine. It owns no conn writes; it forwards
	// data/resize to the session and invokes onExplicitClose on a close frame.
	// It loops until ReadMessage errors (browser closed/disconnected, or the
	// writer closed the conn after the PTY ended). On exit it closes stop to
	// unblock the writer (covering the abrupt-disconnect-while-PTY-alive case).
	reader := func() {
		defer close(stop)
		for {
			messageType, p, rerr := conn.ReadMessage()
			if rerr != nil {
				// WS closed by the browser, an abrupt disconnect, a read error,
				// or the writer closed the conn after the PTY ended. Either way
				// the loop exits; close(stop) unblocks the writer, then
				// writerDone.Wait() reaps it.
				_ = messageType
				return
			}

			kind, data, ctrl := ParseInputFrame(p)
			switch kind {
			case InputData:
				if _, werr := sess.WriteStdin(data); werr != nil {
					// stdin closed (session dying). The writer will observe the
					// closed subscriber channel, send the closed frame, and
					// close the conn — which unblocks our next ReadMessage.
					continue
				}
			case InputResize:
				// Best-effort: a resize on a closing session is harmless.
				_ = sess.Resize(ctrl.Cols, ctrl.Rows)
			case InputClose:
				// Explicit teardown requested by the browser. Destroy the PTY
				// via the caller's callback; Session.Close closes the
				// subscriber channel, the writer emits the closed frame and
				// closes the conn, and our next ReadMessage errors out. Do NOT
				// return here: returning would race an in-flight writer
				// WriteMessage against conn.Close. Looping lets the writer tear
				// us down deterministically.
				if onExplicitClose != nil {
					onExplicitClose()
				}
			}
		}
	}
	reader()

	// Reader is done (stop is now closed). Close the conn (idempotent) to
	// guarantee the writer unblocks even if it was mid-WriteMessage, then wait
	// for the writer to fully exit so no goroutine leaks and no WriteMessage
	// outlives ServeWS.
	_ = conn.Close()
	writerDone.Wait()
}
