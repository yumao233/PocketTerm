import type { User } from "@api/api"

// Locale type required by i18n; kept here alongside its persistence, re-exported by lib/i18n.ts.
export type Locale = "zh_CN" | "en_US"

// localStorage reads/writes are isolated here to avoid scattered string literals.
const KEY = "webshell.auth"
const LOCALE_KEY = "webshell.locale"

interface StoredAuth {
  token: string
  user: User
}

export function loadAuth(): StoredAuth | null {
  try {
    const raw = localStorage.getItem(KEY)
    if (!raw) return null
    const parsed = JSON.parse(raw) as StoredAuth
    if (!parsed.token || !parsed.user) return null
    return parsed
  } catch {
    return null
  }
}

export function saveAuth(token: string, user: User): void {
  localStorage.setItem(KEY, JSON.stringify({ token, user }))
}

export function clearAuth(): void {
  localStorage.removeItem(KEY)
}

/** Read the persisted locale; returns null if invalid or missing (caller falls back to detect). */
export function loadLocale(): Locale | null {
  try {
    const v = localStorage.getItem(LOCALE_KEY)
    if (v === "zh_CN" || v === "en_US") return v
    return null
  } catch {
    return null
  }
}

/** Persist the locale; failures (e.g. private mode) are silently ignored. */
export function saveLocale(locale: Locale): void {
  try {
    localStorage.setItem(LOCALE_KEY, locale)
  } catch {
    // ignore
  }
}