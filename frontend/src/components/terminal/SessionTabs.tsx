// SessionTabs —— terminal tabs track in the middle of the Header, with an overflow "More" dropdown.
//
// Interaction design (aligned with Figma node `header-tabs-container`, dark draft):
//   - Active tab: background #282828 + bottom 2px #3b82f6 + text #93c5fd semibold;
//   - Inactive: text #94a3b8 medium, darkens on hover;
//   - When tabs exceed the container width, right-overflowing tabs are collapsed into a "More ▾" dropdown (base-ui Menu);
//     dropdown items are equivalent to track items (focus can be switched).
//
// Overflow detection approach: an invisible "measurement layer" renders all tabs, compares its scrollWidth against
// the container's available width, and trims one-by-one until they fit (reserving width for the "More" button). The display layer
// renders only visibleCount tabs + the overflow dropdown. The measurement layer and display layer use identical tab styles to guarantee width consistency.
import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
} from "react";
import { ChevronDown } from "lucide-react";
import { Menu } from "@base-ui/react/menu";
import type { Session } from "@api/api";
import { useI18n } from "@/hooks/useI18n";

interface SessionTabsProps {
  sessions: Session[];
  activeId: string | null;
  onSelect: (id: string) => void;
  onClose: (id: string) => void;
}

/** Reserved width for the "More" dropdown button (including padding and icon). */
const MORE_BUTTON_RESERVE = 72;

/** Tab text style: active and inactive states. */
function tabTextClass(active: boolean): string {
  return active ? "text-[#93c5fd] font-semibold" : "text-[#94a3b8] font-medium";
}

/** Shared tab internal structure: title + close button appearing on hover. */
function TabContent({
  session,
  active,
}: {
  session: Session;
  active: boolean;
}) {
  return (
    <>
      <span className={"text-[13px] " + tabTextClass(active)}>
        {session.title}
      </span>
    </>
  );
}

export function SessionTabs({
  sessions,
  activeId,
  onSelect,
}: SessionTabsProps) {
  const { t } = useI18n();
  const containerRef = useRef<HTMLDivElement>(null);
  const measureRef = useRef<HTMLDivElement>(null);
  // Number of visible tabs; overflow goes into "More".
  const [visibleCount, setVisibleCount] = useState(sessions.length);

  /**
   * Measure the total width of the invisible layer, compare with container width, and trim down to visibleCount.
   * Uses useLayoutEffect to complete before paint, avoiding visible-state flicker.
   */
  const measure = useCallback(() => {
    const container = containerRef.current;
    const measureEl = measureRef.current;
    if (!container || !measureEl) return;
    const available = container.clientWidth;
    const fullWidth = measureEl.scrollWidth;
    if (fullWidth <= available) {
      setVisibleCount(sessions.length);
      return;
    }
    // Trim item-by-item: each tab in the measurement layer is a direct child; accumulate widths to decide.
    const tabs = Array.from(
      measureEl.querySelectorAll<HTMLDivElement>("[data-tab]"),
    );
    let count = sessions.length;
    for (let n = sessions.length - 1; n >= 1; n -= 1) {
      const width = tabs
        .slice(0, n)
        .reduce((sum, el) => sum + el.offsetWidth, 0);
      if (width + MORE_BUTTON_RESERVE <= available) {
        count = n;
        break;
      }
      count = n;
    }
    setVisibleCount(count);
  }, [sessions.length]);

  useLayoutEffect(() => {
    measure();
  }, [measure, sessions]);

  useEffect(() => {
    const container = containerRef.current;
    if (!container) return;
    const ro = new ResizeObserver(() => measure());
    ro.observe(container);
    return () => ro.disconnect();
  }, [measure]);

  if (sessions.length === 0) return null;

  const visible = sessions.slice(0, visibleCount);
  const overflowed = sessions.slice(visibleCount);

  return (
    <div
      ref={containerRef}
      className="relative flex min-w-0 flex-1 items-center justify-start px-8"
    >
      {/* Display layer: render only the visible number of tabs */}
      <div className="flex h-full items-center">
        {visible.map((s) => (
          <div
            key={s.id}
            className={"group relative flex  cursor-pointer items-center px-2 "}
            onClick={() => onSelect(s.id)}
          >
            <TabContent session={s} active={s.id === activeId} />
            {/* Active-state bottom blue bar */}
            {/* {s.id === activeId && (
              <span className="absolute inset-x-0 bottom-0 h-0.5 bg-[#3b82f6]" />
            )} */}
          </div>
        ))}
      </div>

      {/* "More" dropdown: only shown when there are overflowing tabs */}
      {overflowed.length > 0 && (
        <Menu.Root>
          <Menu.Trigger
            render={
              <button
                type="button"
                className="flex shrink-0 cursor-pointer items-center gap-1 rounded px-2.5 py-1.5 text-[12px] font-medium text-[#94a3b8] hover:bg-white/5 hover:text-[#e2e8f0]"
              >
                {t({ id: "session.tabs.more", dm: "More" })}
                <ChevronDown className="size-2.5" />
              </button>
            }
          />
          <Menu.Portal>
            <Menu.Positioner align="end" sideOffset={4}>
              <Menu.Popup className="z-50 min-w-40 overflow-hidden rounded-md border border-[#3a3a3a] bg-[#282828] py-1 shadow-lg">
                {overflowed.map((s) => (
                  <Menu.Item
                    key={s.id}
                    onClick={() => onSelect(s.id)}
                    className={
                      "flex w-full cursor-pointer items-center px-3 py-1.5 text-[13px] hover:bg-[#3a3a3a] " +
                      tabTextClass(s.id === activeId)
                    }
                  >
                    <span className="truncate">{s.title}</span>
                  </Menu.Item>
                ))}
              </Menu.Popup>
            </Menu.Positioner>
          </Menu.Portal>
        </Menu.Root>
      )}

      {/* Measurement layer: invisible, renders all tabs to compute width */}
      <div
        ref={measureRef}
        aria-hidden="true"
        className="pointer-events-none invisible absolute left-0 top-0 flex h-full items-center"
      >
        {sessions.map((s) => (
          <div
            key={s.id}
            data-tab
            className="flex h-full shrink-0 items-center px-4"
          >
            <TabContent session={s} active={s.id === activeId} />
          </div>
        ))}
      </div>
    </div>
  );
}
