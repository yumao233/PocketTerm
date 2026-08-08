import { useState, type FormEvent } from "react"
import { Loader2, X } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { useAuth } from "@/context/AuthContext"
import { useI18n } from "@/hooks/useI18n"
import { ApiError, Err } from "@/lib/api"
import { createTemplate, updateTemplate } from "@/lib/templates"
import type { Template, KV, TemplateCreateRequest } from "@api/api"

interface Props {
  mode: "create" | "edit"
  template?: Template
  onClose: () => void
  onSaved: () => void
}

function argsToText(args: string[]): string {
  return args.join("\n")
}
function textToArgs(text: string): string[] {
  return text.split("\n").map((s) => s.trim()).filter(Boolean)
}
function envToText(env: KV[]): string {
  return env.map((kv) => `${kv.key}=${kv.value}`).join("\n")
}
function textToEnv(text: string): KV[] {
  return text.split("\n")
    .map((s) => s.trim())
    .filter(Boolean)
    .map((line) => {
      const i = line.indexOf("=")
      if (i === -1) return { key: line, value: "" }
      return { key: line.slice(0, i), value: line.slice(i + 1) }
    })
}

export function TemplateDialog({ mode, template, onClose, onSaved }: Props) {
  const { token, logout } = useAuth()
  const { t } = useI18n()
  const [name, setName] = useState(template?.name ?? "")
  const [command, setCommand] = useState(template?.command ?? "bash")
  const [cwd, setCwd] = useState(template?.cwd ?? "")
  const [argsText, setArgsText] = useState(template ? argsToText(template.args) : "")
  const [envText, setEnvText] = useState(template ? envToText(template.env) : "")
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)

  async function handleSubmit(e: FormEvent) {
    e.preventDefault()
    if (!token || !name.trim()) return
    setError(null)
    setLoading(true)
    const req: TemplateCreateRequest = {
      name: name.trim(),
      command: command.trim() || "bash",
      cwd: cwd.trim() || undefined,
      args: textToArgs(argsText),
      env: textToEnv(envText),
    }
    try {
      if (mode === "create") {
        await createTemplate(token, req)
      } else if (template) {
        await updateTemplate(token, template.id, req)
      }
      onSaved()
      onClose()
    } catch (e) {
      if (e instanceof ApiError && e.code === Err.Unauthenticated) {
        logout()
        onClose()
        return
      }
      let msg = t({ id: "template.saveFailed", dm: "Failed to save" })
      if (e instanceof ApiError) {
        msg = e.code === Err.TemplateNameDuplicate ? t({ id: "template.nameDuplicate", dm: "Template name already exists" }) : e.message
      }
      setError(msg)
    } finally {
      setLoading(false)
    }
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4">
      <div className="bg-background w-full max-w-md rounded-xl border p-6 shadow-lg">
        <div className="mb-4 flex items-center justify-between">
          <h2 className="text-lg font-semibold">
            {mode === "create" ? t({ id: "template.createTitle", dm: "New Template" }) : t({ id: "template.editTitle", dm: "Edit Template" })}
          </h2>
          <button onClick={onClose} className="text-muted-foreground hover:text-foreground">
            <X className="size-5" />
          </button>
        </div>
        <form onSubmit={handleSubmit} className="flex flex-col gap-4">
          <div className="flex flex-col gap-2">
            <Label htmlFor="tpl-name">{t({ id: "template.field.name", dm: "Name *" })}</Label>
            <Input id="tpl-name" value={name}
              onChange={(e) => setName(e.target.value)} required />
          </div>
          <div className="flex flex-col gap-2">
            <Label htmlFor="tpl-command">{t({ id: "template.field.command", dm: "Command" })}</Label>
            <Input id="tpl-command" value={command}
              onChange={(e) => setCommand(e.target.value)} />
          </div>
          <div className="flex flex-col gap-2">
            <Label htmlFor="tpl-cwd">{t({ id: "template.field.cwd", dm: "Working Directory" })}</Label>
            <Input id="tpl-cwd" value={cwd} placeholder={t({ id: "template.field.cwdPlaceholder", dm: "Leave empty to use default $HOME" })}
              onChange={(e) => setCwd(e.target.value)} />
          </div>
          <div className="flex flex-col gap-2">
            <Label htmlFor="tpl-args">{t({ id: "template.field.args", dm: "Arguments (one per line)" })}</Label>
            <textarea id="tpl-args" value={argsText} rows={3}
              className="border-input bg-transparent rounded-lg border px-2.5 py-1 text-sm outline-none focus-visible:border-ring"
              onChange={(e) => setArgsText(e.target.value)} />
          </div>
          <div className="flex flex-col gap-2">
            <Label htmlFor="tpl-env">{t({ id: "template.field.env", dm: "Environment Variables (KEY=VALUE per line)" })}</Label>
            <textarea id="tpl-env" value={envText} rows={3}
              className="border-input bg-transparent rounded-lg border px-2.5 py-1 text-sm outline-none focus-visible:border-ring"
              onChange={(e) => setEnvText(e.target.value)} />
          </div>
          {error && <p className="text-destructive text-sm">{error}</p>}
          <div className="flex justify-end gap-2">
            <Button type="button" variant="outline" onClick={onClose}>{t({ id: "template.cancel", dm: "Cancel" })}</Button>
            <Button type="submit" disabled={loading}>
              {loading && <Loader2 className="size-4 animate-spin" />}
              {t({ id: "template.save", dm: "Save" })}
            </Button>
          </div>
        </form>
      </div>
    </div>
  )
}
