// Session lifecycle API client.
//
// Backend sessions are in-memory PTY metadata: GET list returns only currently
// online sessions and is empty after restart; POST starts a real local SSH/PTY
// session from a template; DELETE triggers Kill (PTY exit + metadata marked
// closed). WS bridging is handled by /api/v1/ws/:id, see lib/terminal.ts.
//
// This file only does HTTP wrapping; business orchestration (list focus
// management, tab switching after creation) lives in the hooks layer.
import type {
  Session,
  SessionCreateRequest,
  SessionListResponse,
  SessionGetResponse,
} from "@api/api"
import { apiFetch } from "@/lib/api"

/** List all online sessions of the current user (metadata only, in-memory). */
export function listSessions(token: string): Promise<Session[]> {
  // Backend wraps the payload as {sessions: [...]}, see handler/session.go.
  return apiFetch<SessionListResponse>("/api/v1/sessions", { token }).then(
    (r) => r.sessions,
  )
}

/** Start a new terminal session from a template; returns metadata including id (used as the WS channel id). */
export function createSession(
  token: string,
  req: SessionCreateRequest,
): Promise<Session> {
  return apiFetch<Session>("/api/v1/sessions", {
    method: "POST",
    body: req,
    token,
  })
}

/** Query a single session's metadata (includes ownership check; non-owner gets Forbidden/NotFound). */
export function getSession(token: string, id: string): Promise<SessionGetResponse> {
  return apiFetch<SessionGetResponse>(`/api/v1/sessions/${id}`, { token })
}

/** Kill the given session (idempotent: succeeds even if it has already exited naturally). */
export function killSession(token: string, id: string): Promise<void> {
  return apiFetch<null>(`/api/v1/sessions/${id}`, {
    method: "DELETE",
    token,
  }).then(() => undefined)
}