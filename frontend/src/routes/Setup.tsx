import { useState, type FormEvent } from "react"
import { Navigate, useNavigate } from "react-router-dom"
import { Terminal, Loader2 } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { useAuth } from "@/context/AuthContext"
import { ApiError } from "@/lib/api"
import { useI18n } from "@/hooks/useI18n"

export function Setup() {
  const { t } = useI18n()
  const { setup, initialized } = useAuth()
  const navigate = useNavigate()
  const [username, setUsername] = useState("admin")
  const [password, setPassword] = useState("")
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)

  // If the backend is already initialized (race / direct visit), go to login.
  // Returning <Navigate/> here delegates to the router layer, avoiding
  // side-effect navigate during render.
  if (initialized) {
    return <Navigate to="/login" replace />
  }

  async function handleSubmit(e: FormEvent) {
    e.preventDefault()
    setError(null)
    setLoading(true)
    try {
      await setup({ username, password })
      // After setup succeeds the token is ready; the Root guard will render the home page.
      navigate("/", { replace: true })
    } catch (e) {
      setError(e instanceof ApiError ? e.message : t({ id: "setup.error.failed", dm: "Setup failed" }))
    } finally {
      setLoading(false)
    }
  }

  return (
    <main className="bg-background text-foreground flex min-h-svh items-center justify-center p-6">
      <Card className="w-full max-w-sm">
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-2xl">
            <Terminal className="size-6" /> {t({ id: "setup.title", dm: "Initialize Admin" })}
          </CardTitle>
          <CardDescription>{t({ id: "setup.description", dm: "The first registered account becomes the system administrator." })}</CardDescription>
        </CardHeader>
        <form onSubmit={handleSubmit}>
          <CardContent className="flex flex-col gap-4">
            <div className="flex flex-col gap-2">
              <Label htmlFor="username">{t({ id: "setup.field.username", dm: "Username" })}</Label>
              <Input id="username" value={username} autoComplete="username"
                onChange={(e) => setUsername(e.target.value)} required />
            </div>
            <div className="flex flex-col gap-2">
              <Label htmlFor="password">{t({ id: "setup.field.password", dm: "Password" })}</Label>
              <Input id="password" type="password" autoComplete="new-password"
                value={password} onChange={(e) => setPassword(e.target.value)} required />
            </div>
            {error && <p className="text-destructive text-sm">{error}</p>}
          </CardContent>
          <CardFooter className="mt-4">
            <Button type="submit" className="w-full" disabled={loading}>
              {loading && <Loader2 className="size-4 animate-spin" />}
              {loading ? t({ id: "setup.button.loading", dm: "Creating…" }) : t({ id: "setup.button.submit", dm: "Create Admin" })}
            </Button>
          </CardFooter>
        </form>
      </Card>
    </main>
  )
}