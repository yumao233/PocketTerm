// TerminalPage —— main UI after login: Header + MonitorBar + TerminalArea in a three-section vertical Flex.
//
// Page structure (Figma node `web-terminal-page`):
//   <div h-svh flex-col>
//     <Header h-14 shrink-0>          ← 56px fixed top bar
//     <MonitorBar shrink-0>           ← monitor bar (adapts its height)
//     <TerminalArea flex-1 min-h-0>   ← terminal area fills the rest (key: min-h-0 allows shrinking)
//   </div>
//
// Data orchestration is centralized here:
//   - useSessions: list/focus/create/Kill/natural-exit;
//   - useSystemStats: monitor bar polling;
//   - useAuth: get token to pass through to WS.
// Child components are purely presentational; callbacks are injected from this page.
import { X } from "lucide-react";
import { useAuth } from "@/context/AuthContext";
import { useI18n } from "@/hooks/useI18n";
import { useSessions } from "@/hooks/useSessions";
import { useSystemStats } from "@/hooks/useSystemStats";
import { Button } from "@/components/ui/button";
import { Header } from "./Header";
import { MonitorBar } from "./MonitorBar";
import { TerminalArea } from "./TerminalArea";

export function TerminalPage() {
  const { t } = useI18n();
  const { token } = useAuth();
  const {
    sessions,
    activeId,
    busy,
    error,
    clearError,
    create,
    remove,
    setActive,
    markClosed,
  } = useSessions();
  const { stats, ready } = useSystemStats();

  // token theoretically should never be missing (this page is inside a protected route), but guard defensively.
  if (!token) return null;

  return (
    // h-svh: fills the viewport; flex-col: three vertical sections; dark bg aligned with Figma (dark draft #202020).
    <div className="flex h-svh flex-col bg-[#202020]">
      <Header
        sessions={sessions}
        activeId={activeId}
        busy={busy}
        onSelectTab={setActive}
        onCloseSession={(id) => void remove(id)}
        onLaunch={(t) => void create(t)}
      />

      {/* Error bar: brief prompt below the Header on operation failure; dismissible */}
      {error && (
        <div className="flex shrink-0 items-center justify-between gap-3 border-b border-[#5b2a2a] bg-[#3a2020] px-6 py-2">
          <span className="text-[12px] text-[#fca5a5]">{error}</span>
          <button
            type="button"
            onClick={clearError}
            className="text-[12px] text-[#fca5a5] underline"
          >
            {t({ id: "termpage.error.close", dm: "Close" })}
          </button>
        </div>
      )}

      {/* min-h-0: break the "grows but won't shrink" loop. Without it, min-height:auto resolves to the
          container's min-content height, which carries xterm's rendered pixel height (rows × cellHeight).
          That floor tracks the last fit(), so enlarging the window grows the terminal (floor < free space)
          but shrinking can't get below the floor → ResizeObserver never re-fires → fit reads the stale tall
          size → terminal stays oversized and overflows. Width is unaffected (these are flex-col, so the
          auto-min applies on the vertical main axis only). h-full is redundant under flex-1 but harmless. */}
      <div className="flex min-h-0 flex-col gap-4 ml-6 mr-6 mt-4 flex-1 h-full">
        <div className="flex justify-between">
          <MonitorBar stats={stats} ready={ready} />
          {/* Close the currently active terminal: same source as the close in the Header tab — both call useSessions.remove,
              which goes through backend Kill + local removal. Disabled when there is no active session. */}
          <Button
            variant="outline"
            size="sm"
            disabled={!activeId}
            onClick={() => {
              if (activeId) void remove(activeId);
            }}
          >
            <X className="size-4" />
            {t({ id: "termpage.close.terminal", dm: "Close terminal" })}
          </Button>
        </div>

        {/* Terminal area: flex-1 fills the rest; min-h-0 is the key to allowing the flex child to shrink */}
        <TerminalArea
          sessions={sessions}
          activeId={activeId}
          token={token}
          onClosed={markClosed}
        />
      </div>
    </div>
  );
}
