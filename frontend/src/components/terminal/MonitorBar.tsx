// MonitorBar —— system monitor bar: CPU / RAM / R-TT metrics.
//
// Visually aligned with Figma node `quick-stats` (dark draft): horizontal layout, each item is "label (semibold #94a3b8) +
// value (JetBrains Mono #e2e8f0) + progress bar (48×4 #3a3a3a track)". R-TT is a latency metric,
// showing only the value (accent color #10b981) with no progress bar.
//
// Data comes from the useSystemStats hook (polls /api/v1/system/stats); when not ready (ready=false)
// a placeholder dash is shown to avoid flashing 0%.
import type { ReactNode } from "react";
import type { SystemStatsView } from "@/lib/systemStats";
import { useI18n } from "@/hooks/useI18n";

interface MonitorBarProps {
  stats: SystemStatsView;
  ready: boolean;
}

/** Progress bar: #3a3a3a track, fill grows with percentage; >80% turns orange as a warning. */
function StatBar({ percent }: { percent: number }) {
  const clamped = Math.max(0, Math.min(100, percent));
  const warn = clamped >= 80;
  return (
    <div className="h-1 w-12 overflow-hidden rounded-[2px] bg-[#3a3a3a]">
      <div
        className={`h-full rounded-[2px] transition-all ${
          warn ? "bg-[#f59e0b]" : "bg-[#10b981]"
        }`}
        style={{ width: `${clamped}%` }}
      />
    </div>
  );
}

/** Single metric item: label + value + optional progress bar. */
function StatItem({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex shrink-0 items-center gap-2">
      {/* Label: 12px semibold neutral gray */}
      <span className="text-[12px] font-semibold text-[#94a3b8]">{label}</span>
      {/* Value: monospace font matching Figma */}
      <span className="font-[JetBrains_Mono_Variable,monospace] text-[12px] font-medium text-[#e2e8f0]">
        {children}
      </span>
    </div>
  );
}

/** Value formatter: 1 decimal place, avoids 12.4% jittering to 12.4000%. */
function fmt1(n: number): string {
  return n.toFixed(1);
}

export function MonitorBar({ stats, ready }: MonitorBarProps) {
  const { t } = useI18n();
  // When not ready, the value shows a placeholder and the progress bar is not rendered to avoid misleading the user.
  const cpuText = ready ? `${fmt1(stats.cpuPercent)}%` : "—";
  const ramText = ready
    ? stats.memTotalGb > 0
      ? `${fmt1(stats.memUsedGb)}GB / ${fmt1(stats.memTotalGb)}GB`
      : `${fmt1(stats.memUsedGb)}GB`
    : "—";
  // RTT sub-ms is truncated to 0 by the backend; "<1ms" is more intuitive, avoiding the misleading "0ms".
  const rttText = ready ? (stats.rttMs > 0 ? `${stats.rttMs}ms` : "<1ms") : "—";

  return (
    <div className="flex shrink-0 items-center gap-6">
      <StatItem label={t({ id: "monitor.cpu", dm: "CPU:" })}>
        {cpuText}
        {ready && <StatBar percent={stats.cpuPercent} />}
      </StatItem>

      <StatItem label={t({ id: "monitor.ram", dm: "RAM:" })}>
        {ramText}
        {ready && <StatBar percent={stats.memPercent} />}
      </StatItem>

      {/* R-TT: value only, green, no progress bar */}
      <div className="flex shrink-0 items-center gap-2">
        <span className="text-[12px] font-semibold text-[#94a3b8]">{t({ id: "monitor.rtt", dm: "R-TT:" })}</span>
        <span className="font-[JetBrains_Mono_Variable,monospace] text-[12px] font-medium text-[#10b981]">
          {rttText}
        </span>
      </div>
    </div>
  );
}
