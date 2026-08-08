// TerminalCard —— single terminal card: terminal body (XTerminal) + bottom command input bar.
//
// Visually aligned with Figma node `terminal-card` (dark draft): #282828 background + #3a3a3a border +
// 8px rounded corners + heavy shadow; dark terminal area #1a1a1a. The card header (status dot/title/close) has been pushed down to
// the outer SessionTabs; this card only carries the terminal display and command input, and no longer duplicates a close entry.
//
// Lifecycle contract: this card is controlled by the outer TerminalArea's "mount only the focused session at any time" strategy;
// switching tabs unmounts the old card / mounts a new one. Therefore when this card is mounted it is necessarily the currently sole visible terminal,
// and there is no longer an active show/hide concept — the command input box is always available.
import { useRef, useState } from "react";
import { ChevronRightIcon } from "lucide-react";
import type { Session } from "@api/api";
import { useI18n } from "@/hooks/useI18n";
import { XTerminal, type XTerminalHandle } from "./XTerminal";

interface TerminalCardOwnProps {
  /** Session metadata for this card. */
  session: Session;
  /** JWT, passed through to XTerminal as the WS query token. */
  token: string;
  /** PTY natural-exit (WS closed frame) callback, passed through to XTerminal. */
  onClosed: (sessionId: string) => void;
}

export function TerminalCard({
  session,
  token,
  onClosed,
}: TerminalCardOwnProps) {
  const { t } = useI18n();
  // Imperative handle of XTerminal: the input box sends commands into PTY stdin through it (sharing the same WS).
  const termRef = useRef<XTerminalHandle>(null);
  const [command, setCommand] = useState("");

  // Submit command: sends the content as raw terminal bytes to the PTY, appending a CR (matching xterm keyboard Enter,
  // converted to NL via PTY ICRNL) so the shell executes it, then clears the input. The PTY echoes input in cooked mode,
  // so the command appears naturally in the terminal — no extra front-end echo needed.
  const submitCommand = () => {
    if (!command) return;
    termRef.current?.sendInput(command + "\r");
    setCommand("");
  };

  return (
    // a11y: uses region + title.
    <div
      role="region"
      aria-label={t({
        id: "termcard.region.aria",
        dm: "Terminal {title}",
        vars: { title: session.title },
      })}
      className="TerminalCard flex min-h-0 w-full flex-1 flex-col gap-3 "
    >
      <div className="min-h-0 flex-1 flex-col flex overflow-clip rounded-lg border border-[#3a3a3a] bg-[#282828] shadow-[0_10px_24px_0_rgba(0,0,0,0.4)]">
        {/* Terminal area: dark background, padding matches Figma; inner XTerminal fills it */}
        <div className="min-h-0 flex-1 bg-[#1a1a1a] p-2">
          {/* XTerminal's root div carries h-full w-full; sized by the terminal area container here */}
          <XTerminal
            ref={termRef}
            sessionId={session.id}
            token={token}
            onClosed={onClosed}
          />
        </div>
      </div>
      {/* Command input bar: Enter submits; shares the same WS into PTY stdin via the XTerminal handle.
          This card is only mounted while it is the focus (see TerminalArea), so the input box is always available — no
          disabled state needed. */}
      <div className="flex h-9 items-center gap-2 rounded-lg border border-[#3a3a3a] bg-[#282828] px-3 mb-4">
        <ChevronRightIcon className="size-4 shrink-0 text-[#64748b]" />
        <input
          type="text"
          value={command}
          onChange={(e) => setCommand(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              e.preventDefault();
              submitCommand();
            }
          }}
          aria-label={t({
            id: "termcard.input.aria",
            dm: "Command input for terminal {title}",
            vars: { title: session.title },
          })}
          placeholder={t({
            id: "termcard.input.placeholder",
            dm: "Type a command and press Enter",
          })}
          spellCheck={false}
          autoComplete="off"
          className="min-w-0 w-full  bg-transparent font-mono text-[13px] text-[#e2e8f0] outline-none placeholder:text-[#64748b]"
        />
      </div>
    </div>
  );
}
