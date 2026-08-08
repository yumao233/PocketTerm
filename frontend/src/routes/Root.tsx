import type { ReactNode } from "react"
import { Navigate, Route, Routes } from "react-router-dom"
import { Loader2 } from "lucide-react"
import { useAuth } from "@/context/AuthContext"
import { Setup } from "@/routes/Setup"
import { Login } from "@/routes/Login"
import { Terminal } from "@/routes/Terminal"

function FullScreenLoading() {
  return (
    <div className="flex min-h-svh items-center justify-center">
      <Loader2 className="text-muted-foreground size-6 animate-spin" />
    </div>
  )
}

/** Authenticated guard: redirect logged-in users visiting setup/login to the home page. */
function AnonymousRoute({ children }: { children: ReactNode }) {
  const { token } = useAuth()
  if (token) return <Navigate to="/" replace />
  return <>{children}</>
}

/** Protected route: without a token, redirect to login (or setup). */
function ProtectedRoute({ children }: { children: ReactNode }) {
  const { token, initialized } = useAuth()
  if (!token) {
    return <Navigate to={initialized ? "/login" : "/setup"} replace />
  }
  return <>{children}</>
}

/** Guards applied to a route element; `undefined` means public (no guard). */
type GuardKind = "anonymous" | "protected"

interface RouteConfig {
  path: string
  element: ReactNode
  guard?: GuardKind
}

/**
 * Central route table — add a new route by appending one entry here. Guards are
 * identified by `GuardKind` (not component refs) so they render as real JSX via
 * `withGuard`, keeping them compliant with the Rules of Hooks.
 */
const ROUTES: RouteConfig[] = [
  { path: "/setup", element: <Setup />, guard: "anonymous" },
  { path: "/login", element: <Login />, guard: "anonymous" },
  { path: "/", element: <Terminal />, guard: "protected" },
  { path: "*", element: <Navigate to="/" replace /> },
]

/** Wrap a route element with its guard component, if any. */
function withGuard(kind: GuardKind | undefined, element: ReactNode): ReactNode {
  switch (kind) {
    case "anonymous":
      return <AnonymousRoute>{element}</AnonymousRoute>
    case "protected":
      return <ProtectedRoute>{element}</ProtectedRoute>
    default:
      return element
  }
}

export function Root() {
  const { ready } = useAuth()
  if (!ready) return <FullScreenLoading />

  return (
    <Routes>
      {ROUTES.map(({ path, element, guard }) => (
        <Route key={path} path={path} element={withGuard(guard, element)} />
      ))}
    </Routes>
  )
}
