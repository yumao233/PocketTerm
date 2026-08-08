// Header —— terminal page top bar: brand + action buttons + tabs + locale switch + logout.
//
// Layout (aligned with Figma node `header`, dark draft): 56px tall, #2a2a2a background + bottom border #3a3a3a, 24px horizontal padding.
// Three sections: flex-none left brand+actions, flex-1 center tabs (centered), flex-none right locale switch+logout.
//
// Pure presentational orchestration component; holds no business state: action callbacks are passed through from TerminalPage, brand is static.
// Static copy always goes through useI18n().t; brand "WebShell" is not translated.
import { LogOut } from "lucide-react";
import type { Session, Template } from "@api/api";
import { Button } from "@/components/ui/button";
import { useAuth } from "@/context/AuthContext";
import { useI18n } from "@/hooks/useI18n";
import { HeaderActions } from "./HeaderActions";
import { SessionTabs } from "./SessionTabs";
import { LocaleSwitch } from "./LocaleSwitch";

interface HeaderProps {
  sessions: Session[];
  activeId: string | null;
  busy: boolean;
  onSelectTab: (id: string) => void;
  onCloseSession: (id: string) => void;
  onLaunch: (template: Template) => void;
}

export function Header({
  sessions,
  activeId,
  busy,
  onSelectTab,
  onCloseSession,
  onLaunch,
}: HeaderProps) {
  const { logout } = useAuth();
  const { t } = useI18n();

  return (
    <header className="flex h-14 shrink-0 items-center justify-between border-b border-[#3a3a3a] bg-[#2a2a2a] px-6">
      {/* Left: brand + action buttons */}
      <div className="flex shrink-0 items-center gap-4 flex-1 justify-start">
        <span className="text-[18px] font-bold whitespace-nowrap text-[#f8fafc]">
          WebShell
        </span>
        <HeaderActions onLaunch={onLaunch} busy={busy} />
        {/* Center: terminal tabs (empty when no session; whitespace preserves centered layout) */}
        <SessionTabs
          sessions={sessions}
          activeId={activeId}
          onSelect={onSelectTab}
          onClose={onCloseSession}
        />
      </div>

      {/* Right: locale switch + logout */}
      <div className="flex shrink-0 items-center gap-2 pl-3">
        <LocaleSwitch />
        <Button variant="outline" size="sm" onClick={logout}>
          <LogOut className="size-4" />
          {t({ id: "header.logout", dm: "Logout" })}
        </Button>
      </div>
    </header>
  );
}
