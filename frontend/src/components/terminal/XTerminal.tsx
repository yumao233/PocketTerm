// XTerminal —— XTerm.js + WebSocket bridge wrapper for a single terminal session.
//
// Responsibilities (high cohesion): bind a sessionId to one xterm instance and one WS, performing:
//   - PTY output (BinaryMessage) → term.write;
//   - Keyboard input (term.onData) → WS BinaryMessage;
//   - Container size change (ResizeObserver) → addon-fit computes cols/rows → send resize control frame;
//   - Server closed frame → onClosed callback to notify the upper layer (PTY has ended).
//
// Key contracts (AGENTS.md §8):
//   - "Disconnect ≠ Kill": component unmount only closes WS and does not proactively send a close frame; the PTY stays alive on the backend;
//     reconnecting later (re-mounting with the same sessionId) recovers it. Tab switching is driven by the outer TerminalArea
//     via "mount only the focused session" (see that component's comments), so this component creates on mount
//     (new xterm + connect WS) and disposes on unmount (dispose + close; PTY still stays alive).
//   - Single writer: WS writes are centralized in send(), xterm writes are centralized in onmessage — no concurrency between them.
//   - Only when the first byte is '{' does it attempt to parse a control frame (closed); all other BinaryMessage/invalid JSON
//     is written as terminal bytes — no bytes dropped (aligned with backend frame classification).
//
// This component is unaware of list/focus; it is only responsible for the "current sessionId". Size adaptation on mount is
// guaranteed by three paths — immediate fit + WS onopen + ResizeObserver — and no longer needs the "re-send resize on active change"
// compensation logic (a hack from the opacity-stacking era, now removed along with the mount/unmount approach).
import { forwardRef, useEffect, useImperativeHandle, useRef } from "react";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";
import { buildResizeFrame, buildWsUrl, isClosedFrame } from "@/lib/terminal";

// "Active handle" composed of term/fit/ws. The init effect stores them in a ref for reuse by WS onopen
// and ResizeObserver callbacks — within the same mount cycle the WS is not rebuilt (depends only on sessionId).
interface LiveHandle {
  term: Terminal;
  fit: FitAddon;
  ws: WebSocket;
}

/**
 * Imperative handle exposed to the outside. TerminalCard's "command input box" and XTerm's keyboard input share
 * the same WS (single-writer semantics preserved): the outer layer only sends raw terminal bytes, via sendInput → sendBinary.
 */
export interface XTerminalHandle {
  /** Send raw terminal bytes to this session's PTY stdin (BinaryMessage). Dropped when WS is not OPEN. */
  sendInput: (data: string) => void;
}

// Non-zero container size check: only then does fit make sense (skip when not laid out, to avoid sending invalid frames).
function hasSize(el: HTMLElement | null): boolean {
  if (!el) return false;
  const r = el.getBoundingClientRect();
  return r.width > 0 && r.height > 0;
}

interface XTerminalProps {
  /** Terminal session id (WS channel id). */
  sessionId: string;
  /** JWT, used as the WS query token. */
  token: string;
  /** Callback when the PTY exits naturally (closed frame received); the upper layer removes the session accordingly. */
  onClosed: (sessionId: string) => void;
}

/**
 * Renders a single terminal. The container hosts xterm's DOM and is responsible for fit; the visual theme (dark background, font)
 * is controlled by the outer TerminalCard; here only xterm's own styles and colors are attached.
 */
export const XTerminal = forwardRef<XTerminalHandle, XTerminalProps>(
  function XTerminal({ sessionId, token, onClosed }, ref) {
    // Ref of the container div: xterm.open mount target, also the ResizeObserver target.
    const containerRef = useRef<HTMLDivElement>(null);
    // Store the latest onClosed in a ref to avoid reconnecting WS when the callback identity changes.
    const onClosedRef = useRef(onClosed);
    onClosedRef.current = onClosed;
    // Active handle ref: written by the init effect, read by onopen / RO callbacks. WS lifecycle spans the entire mount.
    const liveRef = useRef<LiveHandle | null>(null);

    // Imperative handle: the outer input box sends command bytes into PTY stdin through it. Reads liveRef.current.ws,
    // automatically following the WS for the sessionId (rebuilt only when sessionId changes).
    useImperativeHandle(
      ref,
      () => ({
        sendInput: (data: string) => {
          const ws = liveRef.current?.ws;
          if (ws) sendBinary(ws, data);
        },
      }),
      [],
    );

    useEffect(() => {
      const container = containerRef.current;
      if (!container) return;

      // --- 1. Create xterm instance and fit addon ---
      const term = new Terminal({
        // Terminal color scheme aligned with Figma dark draft: background #1a1a1a, foreground #e2e8f0.
        // Font is JetBrains Mono Variable (@imported in index.css).
        fontFamily:
          "'JetBrains Mono Variable', 'JetBrains Mono', ui-monospace, monospace",
        fontSize: 13,
        lineHeight: 1,
        cursorBlink: true,
        cursorStyle: "bar",
        allowProposedApi: true,
        theme: {
          background: "#1a1a1a",
          foreground: "#e2e8f0",
          cursor: "#10b981",
          selectionBackground: "#3a3a3a",
          selectionForeground: "#e2e8f0",
          // ANSI colors follow a common dark scheme to keep colored output like ls readable.
          black: "#1a1a1a",
          red: "#f87171",
          green: "#10b981",
          yellow: "#f59e0b",
          blue: "#38bdf8",
          magenta: "#c084fc",
          cyan: "#22d3ee",
          white: "#e2e8f0",
          brightBlack: "#64748b",
          brightRed: "#fca5a5",
          brightGreen: "#34d399",
          brightYellow: "#fbbf24",
          brightBlue: "#60a5fa",
          brightMagenta: "#d8b4fe",
          brightCyan: "#67e8f9",
          brightWhite: "#f8fafc",
        },
      });
      const fit = new FitAddon();
      term.loadAddon(fit);
      term.open(container);
      // Initial fit to compute size; the container may not have finished layout, so tolerate by fitting once more.
      safeFit(fit);
      setTimeout(() => safeFit(fit), 100);

      // --- 2. Establish WebSocket ---
      const ws = new WebSocket(buildWsUrl(sessionId, token));
      ws.binaryType = "arraybuffer"; // receive raw bytes, feed to term.write

      // Expose the active handle for reuse by WS onopen and ResizeObserver callbacks.
      liveRef.current = { term, fit, ws };

      // Mark closed frame / normal unmount, to avoid triggering onClosed repeatedly.
      let closedNotified = false;
      const notifyClosed = () => {
        if (closedNotified) return;
        closedNotified = true;
        onClosedRef.current(sessionId);
      };

      // PTY output → xterm. BinaryMessage is raw terminal bytes; TextMessage may be a
      // closed control frame (currently the only TextMessage sent by the backend).
      ws.onmessage = (ev) => {
        if (typeof ev.data === "string") {
          // Text frame: only possible as closed control (aligned with backend closedFrame).
          if (isClosedFrame(ev.data)) {
            notifyClosed();
          } else {
            // Theoretically the backend sends no other text frames; write defensively.
            term.write(ev.data);
          }
        } else if (ev.data instanceof ArrayBuffer) {
          term.write(new Uint8Array(ev.data));
        }
      };

      // Send a resize as soon as the WS connects: the backend PTY previously only had the default size from creation (80×24);
      // here the real web size is pushed over so the terminal uses correct rows/cols on first entry. Tab switching and remounting
      // also go through here, syncing the current container size to the backend (already-alive) PTY, completing "reconnect".
      ws.onopen = () => {
        console.info("Websocket open:", sessionId);
        fitAndResizeWithCols(liveRef.current, 100, 40);
        // Send a window reset frame count once when active to ensure console data is refreshed.
        setTimeout(() => {
          fitAndResize(liveRef.current, containerRef.current);
        }, 20);
      };

      // Keyboard input → WS, sent as raw terminal bytes via BinaryMessage.
      const disposeData = term.onData((data) => {
        sendBinary(ws, data);
      });

      // --- 3. Size adaptation: container change → fit → send resize control frame ---
      // Under the mount/unmount approach the card is always visible and correctly sized; tab switching goes through unmount/remount via the onopen path,
      // so no extra active-triggered resend is needed.
      const ro = new ResizeObserver(() => {
        fitAndResize(liveRef.current, containerRef.current);
      });
      ro.observe(container);

      // 监听 window 尺寸变化(如浏览器窗口缩放、布局抖动):容器尺寸若随之变化,
      // ResizeObserver 通常已能捕获,但某些场景(父级 flex/绝对定位固定)下容器尺寸不变而视口变化,
      // 仍需主动触发一次 safeFit 以重新计算 xterm 的 cols/rows,避免终端尺寸与可视区脱节。
      const onWindowResize = () => {
        fitAndResize(liveRef.current, containerRef.current);
      };
      window.addEventListener("resize", onWindowResize);

      // --- 4. Unmount cleanup: close WS, stop observer, dispose xterm ---
      // Do not send a close frame — keep the backend PTY alive (disconnect ≠ Kill). Reconnecting with the same sessionId next time is sufficient.
      return () => {
        window.removeEventListener("resize", onWindowResize);
        ro.disconnect();
        disposeData.dispose();
        // Remove listeners before closing, to avoid extra side effects from onclose.
        ws.onmessage = null;
        ws.onopen = null;
        ws.onclose = null;
        ws.onerror = null;
        if (
          ws.readyState === WebSocket.OPEN ||
          ws.readyState === WebSocket.CONNECTING
        ) {
          ws.close();
        }
        term.dispose();
        liveRef.current = null;
      };
      // Depends only on sessionId: a token change (logout) comes with component unmount, no need to depend on it separately.
      // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [sessionId]);

    return <div ref={containerRef} className="XTerminal h-full w-full" />;
  },
);

/**
 * Unified "fit + send resize frame" action. Reused in multiple places:
 *   - WS onopen (first entry / tab-switch reconnect);
 *   - ResizeObserver (container size change).
 * Safely skipped when the container size is 0 or the WS is not ready — no invalid/lost frames sent.
 */
function fitAndResize(
  live: LiveHandle | null,
  container: HTMLElement | null,
): void {
  if (!live) return;
  if (!hasSize(container)) return;
  if (!safeFit(live.fit)) return;
  const { cols, rows } = live.term;
  sendText(live.ws, buildResizeFrame(cols, rows));
}

function fitAndResizeWithCols(
  live: LiveHandle | null,
  cols: number,
  rows: number,
): void {
  if (!live) return;
  if (!safeFit(live.fit)) return;
  sendText(live.ws, buildResizeFrame(cols, rows));
}

/** Safe fit: FitAddon throws when the container size is 0; swallow and return false. */
function safeFit(fit: FitAddon): boolean {
  try {
    fit.fit();
    return true;
  } catch {
    return false;
  }
}

/** Send raw terminal bytes (BinaryMessage). The frame is dropped when WS is not OPEN (queueing not implemented for now). */
function sendBinary(ws: WebSocket, data: string): void {
  if (ws.readyState !== WebSocket.OPEN) return;
  // String → UTF-8 bytes. Terminal interaction is mostly ASCII, but pasting Chinese also needs correct encoding.
  ws.send(new TextEncoder().encode(data));
}

/** Send a control frame (TextMessage). */
function sendText(ws: WebSocket, data: string): void {
  if (ws.readyState !== WebSocket.OPEN) return;
  ws.send(data);
}
