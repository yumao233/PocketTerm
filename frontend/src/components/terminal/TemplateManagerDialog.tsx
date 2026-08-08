// TemplateManagerDialog —— template manager dialog: wraps TemplateList (list + create/delete/edit)
// in a centered modal, invoked by the Header "Template Manager" button.
//
// Reuses the existing TemplateList component (with refresh/create/edit/delete/launch); this only provides the modal shell and
// launch callback: selecting "Launch" on a template also closes the dialog. Single responsibility — does not re-implement list logic.
import { useEffect } from "react"
import { X } from "lucide-react"
import { useI18n } from "@/hooks/useI18n"
import { TemplateList } from "@/components/templates/TemplateList"
import type { Template } from "@api/api"

interface TemplateManagerDialogProps {
  open: boolean
  /** Callback when a template is launched (clicking a list item); also closes the dialog. */
  onLaunch: (template: Template) => void
  onClose: () => void
}

export function TemplateManagerDialog({
  open,
  onLaunch,
  onClose,
}: TemplateManagerDialogProps) {
  const { t } = useI18n()
  // ESC to close: lightweight keyboard accessibility.
  useEffect(() => {
    if (!open) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose()
    }
    window.addEventListener("keydown", onKey)
    return () => window.removeEventListener("keydown", onKey)
  }, [open, onClose])

  if (!open) return null

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4"
      onClick={onClose}
    >
      <div
        className="bg-background flex w-full max-w-md flex-col rounded-xl border p-4 shadow-lg"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="mb-3 flex items-center justify-between">
          <h2 className="text-base font-semibold">{t({ id: "header.templateManager", dm: "Templates" })}</h2>
          <button
            type="button"
            aria-label={t({ id: "tmplmgr.close", dm: "Close" })}
            onClick={onClose}
            className="text-muted-foreground hover:text-foreground"
          >
            <X className="size-5" />
          </button>
        </div>
        {/* Max height constraint; list scrolls internally */}
        <div className="max-h-[60vh] min-h-[12rem]">
          <TemplateList
            onLaunch={(t) => {
              onLaunch(t)
              onClose()
            }}
          />
        </div>
      </div>
    </div>
  )
}