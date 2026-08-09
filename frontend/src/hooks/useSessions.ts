// useSessions — orchestration hook for the terminal session list and focus.
//
// Design notes (AGENTS.md §8):
//   1. Backend sessions are purely in-memory: a restart empties them. The frontend
//      keeps an in-memory list of "sessions started during this browser session",
//      layered with periodic polling to verify liveness — disappearance from the
//      backend list is treated as "exited".
//   2. "Disconnect ≠ Kill": switching tabs only changes focus; a WS disconnect does
//      not destroy the PTY. A session is removed only on explicit close or when the
//      PTY exits naturally (WS receives the closed frame).
//   3. Single focus: at any moment exactly one activeId maps to the visible terminal;
//      switching tabs does not destroy existing XTerm instances (lazy creation +
//      cache), handled inside the XTerminal component.
//
// This hook only owns "list + focus"; WS I/O and xterm rendering live in the
// XTerminal component, which reports "PTY ended" back here via the markClosed
// callback.
import { useCallback, useEffect, useRef, useState } from "react"
import { useSearchParams } from "react-router-dom"
import type { Session, Template } from "@api/api"
import { ApiError, Err } from "@/lib/api"
import { createSession, killSession, listSessions } from "@/lib/sessions"
import { useAuth } from "@/context/AuthContext"
import { $t } from "@/lib/i18n"

/** Interval for polling the backend list: used to sync "naturally exited" sessions. */
const REFRESH_INTERVAL_MS = 5000

export interface UseSessionsResult {
  /** Currently maintained session list (in creation order). */
  sessions: Session[]
  /** Current focused session id; null when the list is empty. */
  activeId: string | null
  /** Whether a load/create is in progress (for button loading state). */
  busy: boolean
  /** Most recent operation error (shown near the Header, clearable). */
  error: string | null
  /** Clear the error. */
  clearError: () => void
  /** Create a new terminal from a template and switch focus to it. */
  create: (template: Template) => Promise<void>
  /** Close the given session (backend Kill + local removal). */
  remove: (id: string) => Promise<void>
  /** Switch focus to the given session. */
  setActive: (id: string) => void
  /** Mark a session as ended (PTY natural exit / WS received closed frame). Local removal only. */
  markClosed: (id: string) => void
  /** Manually refresh once (for the button). */
  refresh: () => Promise<void>
}

export function useSessions(): UseSessionsResult {
  const { token, logout } = useAuth()
  const [sessions, setSessions] = useState<Session[]>([])
  const [activeId, setActiveId] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  // --- URL nav: write the focused terminal id into the URL as ?nav=<id>, restore focus on refresh. ---
  const [searchParams, setSearchParams] = useSearchParams()
  // Snapshot once on mount the nav carried over from a refresh; afterwards it does not follow URL changes (our own writes must not trigger a re-restore).
  const [initialNav] = useState(() => searchParams.get("nav"))
  const navConsumedRef = useRef(false)
  // Mirror searchParams/setSearchParams via refs so the URL-writing effect depends only on
  // [activeId], breaking the "write URL → searchParams changes → effect re-runs" loop (§13:
  // read-latest-value-via-ref pattern).
  const searchParamsRef = useRef(searchParams)
  searchParamsRef.current = searchParams
  const setSearchParamsRef = useRef(setSearchParams)
  setSearchParamsRef.current = setSearchParams

  // Mirror token into a ref so the polling effect does not depend on token, which
  // would otherwise reset the timer on every logout/login.
  const tokenRef = useRef(token)
  tokenRef.current = token

  const clearError = useCallback(() => setError(null), [])

  /** Unified 401 handler: an invalid token triggers logout and the route guard
   *  redirects to the login page. */
  const handleAuthError = useCallback(
    (e: unknown) => {
      if (e instanceof ApiError && e.code === Err.Unauthenticated) {
        logout()
        return true
      }
      return false
    },
    [logout],
  )

  /**
   * Fetch the backend list and merge with the local one:
   *   - Sessions present on the backend: backend wins (update status/title);
   *   - Locally present but missing on the backend: treated as natural exit, removed;
   *   - If the focused session is removed, auto-switch to the tail (last) of the list.
   */
  const refresh = useCallback(async () => {
    const t = tokenRef.current
    if (!t) return
    try {
      const remote = await listSessions(t)
      setSessions((prev) => {
        const remoteMap = new Map(remote.map((s) => [s.id, s]))
        // Preserve local order, update with the latest backend data; remove ones no
        // longer present on the backend.
        const next = prev
          .filter((s) => remoteMap.has(s.id))
          .map((s) => remoteMap.get(s.id) as Session)
        // Append sessions the frontend hasn't recorded yet (e.g. created in another
        // tab — edge case).
        for (const r of remote) {
          if (!next.some((s) => s.id === r.id)) next.push(r)
        }
        return next
      })
    } catch (e) {
      // A polling failure does not interrupt usage; only 401 triggers logout.
      handleAuthError(e)
    }
  }, [handleAuthError])

  // Fetch once on mount + poll on a timer to sync liveness.
  useEffect(() => {
    void refresh()
    const timer = window.setInterval(() => void refresh(), REFRESH_INTERVAL_MS)
    return () => window.clearInterval(timer)
  }, [refresh])

  /**
   * Focus follows the list: correct an invalid activeId.
   *   - Empty list → activeId set to null;
   *   - activeId not in the list (removed / never initialized) → fall back to the
   *     last session;
   *   - Otherwise leave it (preserve the user's current focus).
   * Only reads sessions and writes activeId; never mutates sessions, to avoid
   * nested setter side effects.
   */
  useEffect(() => {
    if (sessions.length === 0) {
      if (activeId !== null) setActiveId(null)
      return
    }
    const exists = activeId !== null && sessions.some((s) => s.id === activeId)
    if (exists) return // current focus is valid, keep it (preserve the user's tab switch)

    // Focus invalid: only on the first attempt try to restore from the nav carried in the URL; if not found, fall back to the last (most recent) session.
    const desired = !navConsumedRef.current ? initialNav : null
    navConsumedRef.current = true
    if (desired && sessions.some((s) => s.id === desired)) {
      setActiveId(desired)
    } else {
      setActiveId(sessions[sessions.length - 1].id)
    }
  }, [sessions, activeId, initialNav])

  /** activeId changed → write the focused terminal back to the URL (replace, to avoid polluting back-history). */
  useEffect(() => {
    const sp = new URLSearchParams(searchParamsRef.current)
    if (activeId) sp.set("nav", activeId)
    else sp.delete("nav")
    setSearchParamsRef.current(sp, { replace: true })
  }, [activeId])

  /** Create a new terminal from a template and switch focus to it. */
  const create = useCallback(
    async (template: Template) => {
      const t = tokenRef.current
      if (!t) return
      setBusy(true)
      setError(null)
      try {
        const rec = await createSession(t, { template_id: template.id })
        setSessions((prev) => [...prev, rec])
        setActiveId(rec.id)
      } catch (e) {
        if (handleAuthError(e)) return
        setError(e instanceof ApiError ? e.message : $t({ id: "sessmsg.startFailed", dm: "Failed to start terminal" }))
      } finally {
        setBusy(false)
      }
    },
    [handleAuthError],
  )

  /** Close the given session: backend Kill (idempotent) + local removal. */
  const remove = useCallback(
    async (id: string) => {
      const t = tokenRef.current
      if (!t) return
      // Optimistic removal: drop from the list first so the UI reacts instantly; if
      // backend Kill fails, surface an error afterwards.
      setSessions((prev) => prev.filter((s) => s.id !== id))
      try {
        await killSession(t, id)
      } catch (e) {
        if (handleAuthError(e)) return
        setError(e instanceof ApiError ? e.message : $t({ id: "sessmsg.closeFailed", dm: "Failed to close terminal" }))
      }
    },
    [handleAuthError],
  )

  const setActive = useCallback((id: string) => setActiveId(id), [])

  /** PTY natural exit (WS closed) or external mark: local removal only; does not
   *  call backend Kill again. */
  const markClosed = useCallback((id: string) => {
    setSessions((prev) => prev.filter((s) => s.id !== id))
  }, [])

  return {
    sessions,
    activeId,
    busy,
    error,
    clearError,
    create,
    remove,
    setActive,
    markClosed,
    refresh,
  }
}