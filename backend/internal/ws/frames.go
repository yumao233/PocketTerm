// Package ws implements the WebSocket layer of the Webshell backend.
//
// It owns two responsibilities:
//
//  1. Input frame classification (frames.go): bytes arriving from the browser
//     are either raw terminal data (forwarded to the PTY stdin) or a JSON
//     control frame (resize / close). Classification is driven by the first
//     byte: only a leading '{' triggers a JSON parse attempt, so terminal
//     output that happens to contain '{' is never dropped.
//
//  2. The WebSocket <-> PTY bridge (conn.go): ServeWS upgrades an HTTP
//     connection, subscribes to a pty.Session, and runs a reader (browser ->
//     PTY) and a writer (PTY -> browser) goroutine. The writer is the sole
//     goroutine that writes to the websocket.Conn (gorilla/websocket forbids
//     concurrent writers), so the closed control frame is emitted by the writer
//     when it observes the session ending. Teardown coordinates via a context
//     and a WaitGroup so no goroutine leaks when either side disconnects.
package ws

import "encoding/json"

// InputKind classifies a frame received from the browser.
type InputKind int

const (
	// InputData is raw terminal input forwarded to the PTY stdin.
	InputData InputKind = iota
	// InputResize is a {"type":"resize","cols":N,"rows":N} control frame.
	InputResize
	// InputClose is a {"type":"close"} control frame requesting session teardown.
	InputClose
)

// ControlFrame is the JSON shape of a browser -> backend control frame. Only
// frames whose Type is "resize" or "close" are treated as control frames; any
// other JSON (or malformed JSON) is forwarded to the PTY as raw data so that
// terminal bytes beginning with '{' are never lost.
type ControlFrame struct {
	Type   string `json:"type"`             // "resize" | "close"
	Cols   int    `json:"cols,omitempty"`   // valid for Type=="resize"
	Rows   int    `json:"rows,omitempty"`   // valid for Type=="resize"
	Reason string `json:"reason,omitempty"` // optional human-readable hint
}

// ParseInputFrame classifies the bytes received from the browser.
//
// Rules (see the package doc and spec section 8):
//   - If the first byte is '{', attempt to unmarshal as JSON into a ControlFrame.
//   - Type=="resize" -> InputResize (ctrl populated with Cols/Rows).
//   - Type=="close"  -> InputClose (ctrl populated).
//   - Anything else (valid JSON without a known type, malformed JSON, or a
//     leading '{' that is not a control frame) -> InputData, returning the raw
//     bytes so terminal output containing '{' is never dropped.
//   - If the first byte is NOT '{' (or the input is empty) -> InputData with
//     the raw bytes.
//
// ParseInputFrame NEVER returns an error that would block data flow: a
// malformed leading-'{' simply degrades to InputData.
func ParseInputFrame(p []byte) (kind InputKind, data []byte, ctrl *ControlFrame) {
	// Empty input, or any input not starting with '{', is raw terminal data.
	if len(p) == 0 || p[0] != '{' {
		return InputData, p, nil
	}

	// Leading '{': try to parse as a control frame. On any parse error, or when
	// the Type is not a recognized control type, fall back to raw data so we
	// never drop terminal bytes that merely begin with '{'.
	var cf ControlFrame
	if err := json.Unmarshal(p, &cf); err != nil {
		return InputData, p, nil
	}

	switch cf.Type {
	case "resize":
		return InputResize, nil, &cf
	case "close":
		return InputClose, nil, &cf
	default:
		// Valid JSON but unknown/missing type (includes "{}" and
		// `{"type":"unknown"}`) — treat as data.
		return InputData, p, nil
	}
}