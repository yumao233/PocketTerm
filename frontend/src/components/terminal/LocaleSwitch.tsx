// LocaleSwitch —— locale switcher on the right side of the top bar.
//
// Reuses the project's existing base-ui Menu (same as HeaderActions' template dropdown) and outline Button;
// dark style aligns with existing popup (bg-[#282828] / border-[#3a3a3a]). The Trigger shows the current
// locale's localized short label (e.g. Chinese / EN); clicking an item calls setLocale and persists it (see lib/i18n).
import { Languages } from "lucide-react";
import { Menu } from "@base-ui/react/menu";
import { Button } from "@/components/ui/button";
import { useI18n } from "@/hooks/useI18n";
import { SUPPORTED_LOCALES } from "@/lib/i18n";

export function LocaleSwitch() {
  const { t, locale, setLocale } = useI18n();

  return (
    <Menu.Root>
      <Menu.Trigger
        render={
          <Button variant="outline" size="sm" aria-label={t({ id: "locale.switch.label", dm: "Language" })}>
            <Languages className="size-3.5" />
            {locale === "zh_CN" ? "中文" : "EN"}
          </Button>
        }
      />
      <Menu.Portal>
        <Menu.Positioner align="end" sideOffset={6}>
          <Menu.Popup className="z-50 min-w-36 overflow-hidden rounded-md border border-[#3a3a3a] bg-[#282828] py-1 shadow-lg">
            {SUPPORTED_LOCALES.map((l) => (
              <Menu.Item
                key={l.value}
                onClick={() => setLocale(l.value)}
                className={
                  "flex w-full cursor-pointer items-center justify-between px-3 py-1.5 text-[13px] hover:bg-[#3a3a3a] " +
                  (l.value === locale ? "text-[#93c5fd] font-semibold" : "text-[#e2e8f0]")
                }
              >
                <span>{t({ id: l.labelKey, dm: l.value })}</span>
                {l.value === locale && <span className="text-[#3b82f6]">✓</span>}
              </Menu.Item>
            ))}
          </Menu.Popup>
        </Menu.Positioner>
      </Menu.Portal>
    </Menu.Root>
  );
}