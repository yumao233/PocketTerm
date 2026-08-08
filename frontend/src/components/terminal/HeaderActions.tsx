// HeaderActions —— left action button group of the Header: new terminal + template management.
//
// Visually aligned with Figma node `actions-group` (dark draft): two #282828 background buttons with #3a3a3a border, 14px icon +
// 13px medium #e2e8f0 text. To reuse the project's existing Button component variant (outline), the button body
// uses Button, icons use lucide (Plus / Settings2).
//
// "New Terminal": clicking does not create directly — backend session requires a template_id, so a
// template dropdown (base-ui Menu) is shown; selecting a template launches it. "Template Manager" opens TemplateManagerDialog
// (reuses TemplateList for create/delete/edit + launch).
import { useCallback, useEffect, useState } from "react"
import { Plus, Settings2, Loader2 } from "lucide-react"
import { Menu } from "@base-ui/react/menu"
import { Button } from "@/components/ui/button"
import { useAuth } from "@/context/AuthContext"
import { useI18n } from "@/hooks/useI18n"
import { ApiError, Err } from "@/lib/api"
import { listTemplates } from "@/lib/templates"
import type { Template } from "@api/api"
import { TemplateManagerDialog } from "./TemplateManagerDialog"

interface HeaderActionsProps {
  /** Launches a template to create a terminal; comes from useSessions.create. */
  onLaunch: (template: Template) => void
  /** Whether a launch is in progress (busy); used for button loading state. */
  busy: boolean
}

export function HeaderActions({ onLaunch, busy }: HeaderActionsProps) {
  const { token, logout } = useAuth()
  const { t } = useI18n()
  const [templates, setTemplates] = useState<Template[]>([])
  const [managerOpen, setManagerOpen] = useState(false)

  /** Fetch template list; templates are a prerequisite for session launch, only fetched when needed (before Menu opens). */
  const refreshTemplates = useCallback(async () => {
    if (!token) return
    try {
      const list = await listTemplates(token)
      setTemplates(list)
    } catch (e) {
      // 401 logout; other errors are silent (Menu shows empty when opened, guiding user to create via manager).
      if (e instanceof ApiError && e.code === Err.Unauthenticated) logout()
    }
  }, [token, logout])

  // Fetch once on first entry so the "New Terminal" dropdown has data; subsequent refreshes happen after the manager dialog saves.
  useEffect(() => {
    void refreshTemplates()
  }, [refreshTemplates])

  /** Launch a template: unified entry, used by both the manager dialog and the dropdown. */
  const handleLaunch = (t: Template) => {
    void onLaunch(t)
  }

  return (
    <div className="flex shrink-0 items-center gap-2">
      {/* New Terminal: template dropdown selector */}
      <Menu.Root
        onOpenChange={(open) => {
          // Refresh template list before each open so newly added templates are immediately selectable.
          if (open) void refreshTemplates()
        }}
      >
        <Menu.Trigger
          render={
            <Button variant="outline" size="sm" disabled={busy}>
              {busy ? (
                <Loader2 className="size-3.5 animate-spin" />
              ) : (
                <Plus className="size-3.5" />
              )}
              {t({ id: "header.newTerminal", dm: "New Terminal" })}
            </Button>
          }
        />
        <Menu.Portal>
          <Menu.Positioner align="start" sideOffset={6}>
            <Menu.Popup className="z-50 min-w-44 overflow-hidden rounded-md border border-[#3a3a3a] bg-[#282828] py-1 shadow-lg">
              {templates.length === 0 ? (
                <div className="text-muted-foreground px-3 py-3 text-center text-xs">
                  {t({ id: "header.emptyTemplates", dm: "No templates yet — create one in Templates" })}
                </div>
              ) : (
                templates.map((t) => (
                  <Menu.Item
                    key={t.id}
                    onClick={() => handleLaunch(t)}
                    className="hover:bg-[#3a3a3a] flex w-full cursor-pointer flex-col items-start px-3 py-1.5 text-left"
                  >
                    <span className="text-[13px] font-medium text-[#e2e8f0]">
                      {t.name}
                    </span>
                    <span className="text-muted-foreground text-[11px]">
                      {t.command} {t.args.join(" ")}
                    </span>
                  </Menu.Item>
                ))
              )}
            </Menu.Popup>
          </Menu.Positioner>
        </Menu.Portal>
      </Menu.Root>

      {/* Template Manager: open dialog */}
      <Button
        variant="outline"
        size="sm"
        onClick={() => setManagerOpen(true)}
      >
        <Settings2 className="size-3.5" />
        {t({ id: "header.templateManager", dm: "Templates" })}
      </Button>

      <TemplateManagerDialog
        open={managerOpen}
        onLaunch={handleLaunch}
        onClose={() => {
          setManagerOpen(false)
          // Refresh dropdown data after closing the dialog (manager may have done create/delete/edit).
          void refreshTemplates()
        }}
      />
    </div>
  )
}