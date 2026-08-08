// useSystemStats — polling hook for the monitor bar data.
//
// Fetches GET /api/v1/system/stats every POLL_INTERVAL_MS, returning a CPU/RAM/RTT
// snapshot.
// Design principles:
//   - Silent degradation: network jitter / server errors do not pop an error bar;
//     the last valid value is retained. Repeated failures also do NOT trigger a 401
//     logout (missing monitoring data should not kick the user out — the 401
//     semantics here differ from session operations).
//   - Stop on unmount: clear the timer when the component unmounts to avoid leaks.
//   - No polling when hidden: pause while document.hidden is true to save requests
//     (visibilitychange).
import { useCallback, useEffect, useRef, useState } from "react"
import { fetchSystemStats, type SystemStatsView } from "@/lib/systemStats"
import { useAuth } from "@/context/AuthContext"

/** Polling interval: the monitor bar does not need second-level precision; 5s
 *  balances real-time feel with backend load. */
const POLL_INTERVAL_MS = 5000

/** Initial empty snapshot: progress bars and degraded state share the "not yet
 *  collected" semantics. */
const EMPTY_STATS: SystemStatsView = {
  cpuPercent: 0,
  cpuCores: 0,
  memUsedGb: 0,
  memTotalGb: 0,
  memPercent: 0,
  rttMs: 0,
}

export interface UseSystemStatsResult {
  /** Latest snapshot; EMPTY_STATS while loading or on first failure. */
  stats: SystemStatsView
  /** Whether at least one valid sample has been fetched (distinguishes "loading"
   *  from "actually zero"). */
  ready: boolean
}

export function useSystemStats(): UseSystemStatsResult {
  const { token } = useAuth()
  const [stats, setStats] = useState<SystemStatsView>(EMPTY_STATS)
  const [ready, setReady] = useState(false)

  // Read token / hidden state via refs; the timer is installed only once.
  const tokenRef = useRef(token)
  tokenRef.current = token
  const hiddenRef = useRef(false)

  const poll = useCallback(async () => {
    const t = tokenRef.current
    if (!t) return
    // Skip while the page is hidden; on becoming visible, immediately catch up.
    if (hiddenRef.current) return
    try {
      const next = await fetchSystemStats(t)
      setStats(next)
      setReady(true)
    } catch {
      // Silent: keep the previous stats, do not log out (see file header).
    }
  }, [])

  useEffect(() => {
    void poll()
    const timer = window.setInterval(() => void poll(), POLL_INTERVAL_MS)

    // Visibility change: pause when hidden, catch up immediately when visible.
    const onVisibility = () => {
      hiddenRef.current = document.hidden
      if (!document.hidden) void poll()
    }
    document.addEventListener("visibilitychange", onVisibility)

    return () => {
      window.clearInterval(timer)
      document.removeEventListener("visibilitychange", onVisibility)
    }
  }, [poll])

  return { stats, ready }
}