import { useCallback, useEffect, useState } from "react"
import { Plus, Pencil, Trash2, Play, Loader2, RefreshCw } from "lucide-react"
import { Button } from "@/components/ui/button"
import { useAuth } from "@/context/AuthContext"
import { useI18n } from "@/hooks/useI18n"
import { ApiError, Err } from "@/lib/api"
import { deleteTemplate, listTemplates } from "@/lib/templates"
import { TemplateDialog } from "@/components/templates/TemplateDialog"
import type { Template } from "@api/api"

interface Props {
  onLaunch: (t: Template) => void
  onTemplatesChange?: (t: Template[]) => void
}

export function TemplateList({ onLaunch, onTemplatesChange }: Props) {
  const { token, logout } = useAuth()
  const { t } = useI18n()
  const [templates, setTemplates] = useState<Template[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [dialog, setDialog] = useState<
    | { mode: "create" }
    | { mode: "edit"; template: Template }
    | null
  >(null)

  const load = useCallback(async () => {
    if (!token) return
    setLoading(true)
    setError(null)
    try {
      const list = await listTemplates(token)
      setTemplates(list)
      onTemplatesChange?.(list)
    } catch (e) {
      if (e instanceof ApiError && e.code === Err.Unauthenticated) {
        logout()
        return
      }
      setError(e instanceof ApiError ? e.message : t({ id: "template.loadFailed", dm: "Failed to load" }))
    } finally {
      setLoading(false)
    }
  }, [token, logout, onTemplatesChange, t])

  useEffect(() => {
    load()
  }, [load])

  async function handleDelete(tpl: Template) {
    if (!token) return
    if (!confirm(t({ id: "template.deleteConfirm", dm: "Delete template \"{name}\"?", vars: { name: tpl.name } }))) return
    try {
      await deleteTemplate(token, tpl.id)
      await load()
    } catch (e) {
      if (e instanceof ApiError && e.code === Err.Unauthenticated) {
        logout()
        return
      }
      setError(e instanceof ApiError ? e.message : t({ id: "template.deleteFailed", dm: "Failed to delete" }))
    }
  }

  return (
    <div className="flex h-full flex-col gap-3">
      <div className="flex items-center justify-between">
        <h2 className="text-sm font-semibold">{t({ id: "template.title", dm: "Templates" })}</h2>
        <div className="flex gap-1">
          <Button size="icon-sm" variant="ghost" onClick={load} aria-label={t({ id: "template.refresh", dm: "Refresh" })}>
            <RefreshCw className="size-4" />
          </Button>
          <Button size="icon-sm" variant="outline" onClick={() => setDialog({ mode: "create" })} aria-label={t({ id: "template.new", dm: "New" })}>
            <Plus className="size-4" />
          </Button>
        </div>
      </div>

      {loading && <Loader2 className="text-muted-foreground size-4 animate-spin" />}
      {error && <p className="text-destructive text-xs">{error}</p>}

      <div className="flex flex-col gap-1 overflow-y-auto">
        {templates.map((tpl) => (
          <div key={tpl.id} className="hover:bg-muted group flex items-center gap-2 rounded-md px-2 py-1.5">
            <button
              className="flex flex-1 flex-col items-start text-left"
              onClick={() => onLaunch(tpl)}
              title={t({ id: "template.launch", dm: "Launch {name}", vars: { name: tpl.name } })}
            >
              <span className="text-sm font-medium">{tpl.name}</span>
              <span className="text-muted-foreground text-xs">
                {tpl.command} {tpl.args.join(" ")}
              </span>
            </button>
            <div className="flex opacity-0 group-hover:opacity-100">
              <Button size="icon-xs" variant="ghost" aria-label={t({ id: "template.launch", dm: "Launch {name}", vars: { name: tpl.name } })}
                onClick={() => onLaunch(tpl)}>
                <Play className="size-3" />
              </Button>
              <Button size="icon-xs" variant="ghost" aria-label={t({ id: "template.edit", dm: "Edit" })}
                onClick={() => setDialog({ mode: "edit", template: tpl })}>
                <Pencil className="size-3" />
              </Button>
              <Button size="icon-xs" variant="ghost" aria-label={t({ id: "template.delete", dm: "Delete" })}
                onClick={() => handleDelete(tpl)}>
                <Trash2 className="size-3" />
              </Button>
            </div>
          </div>
        ))}
        {!loading && templates.length === 0 && (
          <p className="text-muted-foreground px-2 py-4 text-center text-xs">
            {t({ id: "template.emptyHint", dm: "No templates yet. Click + to create one." })}
          </p>
        )}
      </div>

      {dialog && (
        <TemplateDialog
          mode={dialog.mode}
          template={dialog.mode === "edit" ? dialog.template : undefined}
          onClose={() => setDialog(null)}
          onSaved={load}
        />
      )}
    </div>
  )
}
