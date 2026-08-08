// System resource monitoring API client + frontend ViewModel.
//
// The backend GET /api/v1/system/stats returns a snapshot of the host's
// CPU/RAM/RTT (collected via gopsutil, see backend/internal/handler/sysinfo.go).
// This file defines the normalized frontend type SystemStatsView, aligned with
// the SystemStats fields in api.d.ts but with units/semantics better suited to the UI:
//   - percentages stay in 0-100;
//   - memory is displayed in the UI as "used GB / total GB"; the backend provides GB directly;
//   - RTT is a server-side approximate latency (ms), for trend reference only.
//
// When any field fails to be collected, the backend degrades to 0/-1; the frontend
// grays out the progress bar accordingly rather than raising an error.
import { apiFetch } from "@/lib/api"

/**
 * Host resource snapshot (frontend view). Fields align verbatim with the backend SystemStats json tags.
 * The frontend no longer imports SystemStats directly from api.d.ts, to avoid import
 * churn against the still-evolving backend contract file; this local type is the stable
 * consumption surface for the UI.
 */
export interface SystemStatsView {
  /** Overall host CPU usage 0-100; 0 when collection fails. */
  cpuPercent: number
  /** Number of logical CPU cores. */
  cpuCores: number
  /** Used memory in GB. */
  memUsedGb: number
  /** Total memory in GB; 0 when collection fails. */
  memTotalGb: number
  /** Memory usage percentage 0-100. */
  memPercent: number
  /** Server-side approximate latency (ms), for trend use. */
  rttMs: number
}

/** Raw backend response shape (json tags copied verbatim for the conversion mapping). */
interface RawSystemStats {
  cpu_percent: number
  cpu_cores: number
  mem_used_gb: number
  mem_total_gb: number
  mem_percent: number
  rtt_ms: number
}

/** Map the backend snake_case response to the frontend camelCase view. */
function toView(raw: RawSystemStats): SystemStatsView {
  return {
    cpuPercent: raw.cpu_percent ?? 0,
    cpuCores: raw.cpu_cores ?? 0,
    memUsedGb: raw.mem_used_gb ?? 0,
    memTotalGb: raw.mem_total_gb ?? 0,
    memPercent: raw.mem_percent ?? 0,
    rttMs: raw.rtt_ms ?? 0,
  }
}

/** Fetch one host resource snapshot. Throws ApiError on failure (the caller decides the degraded strategy). */
export function fetchSystemStats(token: string): Promise<SystemStatsView> {
  return apiFetch<RawSystemStats>("/api/v1/system/stats", { token }).then(toView)
}