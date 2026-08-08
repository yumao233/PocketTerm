import { createContext, useContext, useEffect, useState, type ReactNode } from "react"
import type { User, CheckSetupResponse, AuthUserResponse, SetupRequest, LoginRequest } from "@api/api"
import { apiFetch } from "@/lib/api"
import { loadAuth, saveAuth, clearAuth } from "@/lib/storage"

interface AuthContextType {
  user: User | null
  token: string | null
  ready: boolean
  initialized: boolean
  setup: (req: SetupRequest) => Promise<void>
  login: (req: LoginRequest) => Promise<void>
  logout: () => void
}

const AuthContext = createContext<AuthContextType | null>(null)

export function AuthProvider({ children }: { children: ReactNode }) {
  const [user, setUser] = useState<User | null>(null)
  const [token, setToken] = useState<string | null>(null)
  const [ready, setReady] = useState(false)
  const [initialized, setInitialized] = useState(true)

  // On startup: probe whether already initialized + restore local auth state.
  useEffect(() => {
    let cancelled = false
    ;(async () => {
      try {
        const r = await apiFetch<CheckSetupResponse>("/api/v1/check-setup")
        if (cancelled) return
        setInitialized(r.initialized)
      } catch {
        // On probe failure default to "initialized" to avoid entering setup by
        // mistake.
        if (!cancelled) setInitialized(true)
      }
      const stored = loadAuth()
      if (stored && !cancelled) {
        setToken(stored.token)
        setUser(stored.user)
      }
      if (!cancelled) setReady(true)
    })()
    return () => {
      cancelled = true
    }
  }, [])

  async function setup(req: SetupRequest): Promise<void> {
    const r = await apiFetch<AuthUserResponse>("/api/v1/setup", {
      method: "POST",
      body: req,
    })
    saveAuth(r.token, r.user)
    setToken(r.token)
    setUser(r.user)
    setInitialized(true)
  }

  async function login(req: LoginRequest): Promise<void> {
    const r = await apiFetch<AuthUserResponse>("/api/v1/login", {
      method: "POST",
      body: req,
    })
    saveAuth(r.token, r.user)
    setToken(r.token)
    setUser(r.user)
  }

  function logout(): void {
    clearAuth()
    setToken(null)
    setUser(null)
  }

  return (
    <AuthContext.Provider
      value={{ user, token, ready, initialized, setup, login, logout }}
    >
      {children}
    </AuthContext.Provider>
  )
}

export function useAuth(): AuthContextType {
  const ctx = useContext(AuthContext)
  if (!ctx) throw new Error("useAuth must be used within AuthProvider")
  return ctx
}