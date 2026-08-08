import { useState, type FormEvent } from "react"
import { Terminal, Loader2 } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { useAuth } from "@/context/AuthContext"
import { ApiError } from "@/lib/api"
import { useI18n } from "@/hooks/useI18n"

export function Login() {
  const { t } = useI18n()
  const { login } = useAuth()
  const [username, setUsername] = useState("")
  const [password, setPassword] = useState("")
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)

  async function handleSubmit(e: FormEvent) {
    e.preventDefault()
    setError(null)
    setLoading(true)
    try {
      await login({ username, password })
      // Once the token is ready, the Root guard renders the home page.
    } catch (e) {
      setError(e instanceof ApiError ? e.message : t({ id: "login.error.failed", dm: "Login failed" }))
    } finally {
      setLoading(false)
    }
  }

  return (
    <main className="bg-background text-foreground flex min-h-svh items-center justify-center p-6">
      <Card className="w-full max-w-sm">
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-2xl">
            <Terminal className="size-6" /> {t({ id: "login.title", dm: "WebShell Login" })}
          </CardTitle>
          <CardDescription>{t({ id: "login.description", dm: "Enter your username and password to log in." })}</CardDescription>
        </CardHeader>
        <form onSubmit={handleSubmit}>
          <CardContent className="flex flex-col gap-4">
            <div className="flex flex-col gap-2">
              <Label htmlFor="username">{t({ id: "login.field.username", dm: "Username" })}</Label>
              <Input id="username" autoComplete="username"
                value={username} onChange={(e) => setUsername(e.target.value)} required />
            </div>
            <div className="flex flex-col gap-2">
              <Label htmlFor="password">{t({ id: "login.field.password", dm: "Password" })}</Label>
              <Input id="password" type="password" autoComplete="current-password"
                value={password} onChange={(e) => setPassword(e.target.value)} required />
            </div>
            {error && <p className="text-destructive text-sm">{error}</p>}
          </CardContent>
          <CardFooter className="mt-4">
            <Button type="submit" className="w-full" disabled={loading}>
              {loading && <Loader2 className="size-4 animate-spin" />}
              {loading ? t({ id: "login.button.loading", dm: "Logging in…" }) : t({ id: "login.button.submit", dm: "Login" })}
            </Button>
          </CardFooter>
        </form>
      </Card>
    </main>
  )
}