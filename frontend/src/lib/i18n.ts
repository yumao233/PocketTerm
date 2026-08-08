// i18n —— lightweight multi-language core (pure TS singleton, no React dependency).
//
// Design:
//   - Messages are statically imported as JSON and bundled; no async loading.
//   - `$t({ id, dm, vars? })`: looks up the dictionary by current locale, falling
//     back to `dm` (English source language) on a miss, then to `id`; finally
//     interpolates `{key}` placeholders using `vars`. Any missing key is a no-throw.
//   - `subscribe` is consumed by `useSyncExternalStore` to track locale changes.
//   - Non-React code (e.g. lib/api.ts) can directly `import {$t}` to read the
//     current locale snapshot.
//
// Persistence and detection are centralized in `lib/storage.ts` to keep
// localStorage reads/writes in one place.
import zhCN from "@/locales/zh_CN.json";
import enUS from "@/locales/en_US.json";
import { loadLocale, saveLocale, type Locale } from "@/lib/storage";

export type { Locale };

/** Module-level mutable state for the current locale; mutated only inside `setLocale`. */
let currentLocale: Locale = loadLocale() ?? detectLocale();

/** locale → dictionary. Statically imported; Vite supports JSON natively. */
const dictionaries: Record<Locale, Record<string, string>> = {
  zh_CN: zhCN as Record<string, string>,
  en_US: enUS as Record<string, string>,
};

/** List of selectable languages rendered by the switcher. `labelKey` itself goes through i18n to show each locale's own localized name (e.g. Chinese / English). */
export const SUPPORTED_LOCALES: ReadonlyArray<{ value: Locale; labelKey: string }> = [
  { value: "zh_CN", labelKey: "locale.zh_CN" },
  { value: "en_US", labelKey: "locale.en_US" },
];

/** Set of subscribers; each is invoked when `setLocale` fires. */
const listeners = new Set<() => void>();

export interface TranslateOptions {
  /** Dictionary key, flat dotted (e.g. "header.logout"). */
  id: string;
  /** Default message (English source language); fallback when key is missing from the dictionary. */
  dm?: string;
  /** Optional interpolation variables that replace `{name}` placeholders in the message. */
  vars?: Record<string, string | number>;
}

/**
 * Translate function. Fallback chain: current locale dictionary → dm → id; then `{key}` interpolation.
 * Inside components, call `useI18n()` to subscribe first; otherwise locale changes won't trigger a re-render.
 */
export function $t({ id, dm, vars }: TranslateOptions): string {
  const dict = dictionaries[currentLocale];
  const text = dict?.[id] ?? dm ?? id;
  if (!vars) return text;
  return interpolate(text, vars);
}

/** Simple `{key}` interpolation; placeholders not provided are left verbatim (no error). */
function interpolate(
  text: string,
  vars: Record<string, string | number>,
): string {
  return text.replace(/\{(\w+)\}/g, (match, key: string) =>
    key in vars ? String(vars[key]) : match,
  );
}

export function getLocale(): Locale {
  return currentLocale;
}

/** Switch language: update module state + persist + notify all subscribers. */
export function setLocale(locale: Locale): void {
  if (locale === currentLocale) return;
  currentLocale = locale;
  saveLocale(locale);
  listeners.forEach((cb) => cb());
}

/** Subscribe to locale changes; returns an unsubscribe function (for useSyncExternalStore). */
export function subscribe(cb: () => void): () => void {
  listeners.add(cb);
  return () => {
    listeners.delete(cb);
  };
}

/** Guess the locale from navigator.language; "zh" prefix → Chinese, otherwise English. */
function detectLocale(): Locale {
  const lang =
    (typeof navigator !== "undefined" && navigator.language) || "en";
  return lang.toLowerCase().startsWith("zh") ? "zh_CN" : "en_US";
}