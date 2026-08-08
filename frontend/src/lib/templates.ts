import type { Template, TemplateCreateRequest, TemplateListResponse } from "@api/api"
import { apiFetch } from "@/lib/api"

export function listTemplates(token: string): Promise<Template[]> {
  // Backend wraps the payload as {templates: [...]}.
  return apiFetch<TemplateListResponse>("/api/v1/templates", { token })
    .then((r) => r.templates)
}

export function createTemplate(token: string, req: TemplateCreateRequest): Promise<Template> {
  return apiFetch<Template>("/api/v1/templates", { method: "POST", body: req, token })
}

export function updateTemplate(
  token: string,
  id: string,
  req: TemplateCreateRequest,
): Promise<Template> {
  return apiFetch<Template>(`/api/v1/templates/${id}`, { method: "PUT", body: req, token })
}

export function deleteTemplate(token: string, id: string): Promise<void> {
  return apiFetch<null>(`/api/v1/templates/${id}`, { method: "DELETE", token }).then(() => undefined)
}