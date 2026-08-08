// useI18n — bridges the lib/i18n locale store into React's reactive system.
//
// Subscribes to locale changes via useSyncExternalStore; components using this hook
// automatically re-render on locale switch, and $t called during render naturally
// picks up the new locale. No useCallback (complies with §13/§14).
import { useSyncExternalStore } from "react";
import {
  $t,
  getLocale,
  setLocale,
  subscribe,
  type Locale,
  type TranslateOptions,
} from "@/lib/i18n";

export function useI18n(): {
  t: (opts: TranslateOptions) => string;
  locale: Locale;
  setLocale: (l: Locale) => void;
} {
  const locale = useSyncExternalStore(subscribe, getLocale, getLocale);
  return { t: $t, locale, setLocale };
}