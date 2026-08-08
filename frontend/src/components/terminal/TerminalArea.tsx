// TerminalArea —— terminal display area: only the TerminalCard of the "currently focused session" is mounted at any time.
//
// Layout: flex-1 fills the remaining space outside Header/MonitorBar; min-h-0 allows shrinking; the single
// card participates in normal document flow to fill this area (no more absolute-positioned stacked cards).
//
// Show/hide strategy (key change; the old opacity-stacking approach is deprecated):
//   The old approach mounted all sessions' cards simultaneously and toggled visibility via opacity/z/pointer-events.
//   The cost was: (1) all sessions' xterm instances and WS connections stayed resident (memory/connection pressure
//   grew linearly with tab count); (2) ResizeObserver did not fire on tab switch (opacity does not change size), forcing
//   a compensation hack in XTerminal of "re-send resize on active change + a 100×100 intermediate frame".
//
//   Now replaced with true React component lifecycle: only the active session's card is rendered, driven by `key=active.id`
//   — switching tabs means "unmount old card → mount new card". Aligned with backend §8 "disconnect ≠ Kill": unmount
//   only closes WS and does not proactively send a close frame; the backend PTY stays alive. When switching back, a new XTerminal
//   reconnects with the same sessionId and re-subscribes to that PTY.
//
//   Trade-off (expected behavior, not a bug): incremental PTY output during the WS disconnect period is not replayed by the backend;
//   after reconnection xterm starts from blank and only receives "post-reconnect" new output (consistent with reconnecting to an
//   existing session after a page refresh). To preserve the pre-switch screen history, you could later serialize with @xterm/addon-serialize
//   before unmount and replay after reconnect (this component does not depend on that capability).
//
//   Empty state: when there are no sessions, a guide is shown; Figma does not specify an empty state, so a restrained neutral hint is used here.
import { TerminalSquare } from "lucide-react";
import type { Session } from "@api/api";
import { useI18n } from "@/hooks/useI18n";
import { TerminalCard } from "./TerminalCard";

interface TerminalAreaProps {
  /** List of all launched sessions. */
  sessions: Session[];
  /** Currently focused session id; null shows empty state (useSessions' focus-following guarantees non-null). */
  activeId: string | null;
  /** JWT, passed through to TerminalCard → XTerminal as the WS query token. */
  token: string;
  /** PTY natural-exit callback (WS closed frame). */
  onClosed: (sessionId: string) => void;
}

export function TerminalArea({
  sessions,
  activeId,
  token,
  onClosed,
}: TerminalAreaProps) {
  const { t } = useI18n();
  // Empty state: no terminal launched.
  if (sessions.length === 0) {
    return (
      <div className="flex min-h-0 flex-1 items-center justify-center p-6">
        <div className="flex flex-col items-center gap-3 text-center">
          <TerminalSquare className="text-muted-foreground/40 size-10" />
          <p className="text-muted-foreground text-sm">
            {t({
              id: "termarea.empty.hint",
              dm: 'No terminal sessions yet — click "New Terminal" in the top-left to start',
            })}
          </p>
        </div>
      </div>
    );
  }

  // Render only the "currently focused" session's card at any time. useSessions' focus-following effect
  // guarantees that when sessions is non-empty, activeId points to one of them; defensively fall back to the last item.
  const active =
    sessions.find((s) => s.id === activeId) ?? sessions[sessions.length - 1];

  // key=active.id is the lifecycle driver: an activeId change triggers a switch → React unmounts the old
  // TerminalCard (with XTerminal dispose + WS close; backend PTY stays alive) and mounts a new card
  // (new XTerminal + WS reconnect). Create → switch → destroy happens naturally this way.
  return (
    <div className="TerminalArea flex min-h-0 flex-1 flex-col">
      <TerminalCard
        key={active.id}
        session={active}
        token={token}
        onClosed={onClosed}
      />
    </div>
  );
}
