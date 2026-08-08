// WebSocket terminal bridging constants and helpers.
//
// The backend WS endpoint GET /api/v1/ws/:id authenticates via ?token=<jwt>
// (browser WS upgrade cannot set the Authorization header, see AGENTS.md §5).
// The frame protocol is described in the "WebSocket protocol" section of
// backend/web/types/api.d.ts: any byte whose first byte is not '{' is treated as
// raw terminal bytes (BinaryMessage); resize/close control frames are
// TextMessage (JSON).

/** Control frame type whitelist (mirrors backend ws/frames.go verbatim). */
export const ControlFrameType = {
  Resize: "resize",
  Close: "close",
} as const

/**
 * Build the WS endpoint URL. In dev it is proxied via vite proxy (see
 * vite.config.ts server.proxy["/api"] with ws:true) through to backend:8080;
 * in production it connects same-origin directly. token is injected via query.
 */
export function buildWsUrl(sessionId: string, token: string): string {
  const proto = window.location.protocol === "https:" ? "wss:" : "ws:"
  return `${proto}//${window.location.host}/api/v1/ws/${sessionId}?token=${encodeURIComponent(token)}`
}

/** Build a resize control frame text (JSON, sent as TextMessage). */
export function buildResizeFrame(cols: number, rows: number): string {
  return JSON.stringify({
    type: ControlFrameType.Resize,
    cols,
    rows,
  })
}

/** Build a close control frame text (requests destruction of the current session). */
export function buildCloseFrame(reason?: string): string {
  return JSON.stringify({
    type: ControlFrameType.Close,
    reason,
  })
}

/**
 * Determine whether a server-sent TextMessage is a closed frame.
 * The backend sends exactly one frame {"type":"closed","reason":"session ended"} when the PTY ends.
 * Parse failures are always treated as non-closed (defensive, aligned with the backend's "parse-failure degrades" semantics).
 */
export function isClosedFrame(raw: string): boolean {
  if (!raw || raw[0] !== "{") return false
  try {
    const obj = JSON.parse(raw) as { type?: string }
    return obj.type === "closed"
  } catch {
    return false
  }
}