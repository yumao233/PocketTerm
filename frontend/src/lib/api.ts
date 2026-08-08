// HTTP envelope client. All non-WS endpoints go through this wrapper:
// the backend uniformly returns HTTP 200 + {code, message, data}, where code===0 means success.
import type { ApiResponse } from "@api/api"
import { $t } from "@/lib/i18n"

// Frontend runtime constant copy (backend/web/types/api.d.ts const enum ErrorCode
// has no runtime object, so numeric literals are used here; semantics map verbatim to the backend ErrXxx).
export const Err = {
  Unauthenticated: 1004,
  TemplateNameDuplicate: 2001,
  SessionNotFound: 3001,
  SessionClosed: 3002,
} as const

/** Business error. code is non-zero; HTTP/network-layer errors use code=0 as a placeholder to distinguish them. */
export class ApiError extends Error {
  code: number
  constructor(code: number, message: string) {
    super(message)
    this.name = "ApiError"
    this.code = code
  }
}

/**
 * Issue an envelope HTTP request and return data (of type T).
 * - code !== 0  -> throw ApiError(code, message)
 * - non-200 / network failure -> throw ApiError(0, ...)
 */
export async function apiFetch<T>(
  path: string,
  opts: { method?: string; body?: unknown; token?: string } = {},
): Promise<T> {
  const { method = "GET", body, token } = opts
  const headers: Record<string, string> = {}
  if (body !== undefined) headers["Content-Type"] = "application/json"
  if (token) headers["Authorization"] = `Bearer ${token}`

  let resp: Response
  try {
    resp = await fetch(path, {
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
    })
  } catch (e) {
    throw new ApiError(0, $t({ id: "api.networkError", dm: "Network request failed: {message}", vars: { message: e instanceof Error ? e.message : String(e) } }))
  }

  if (!resp.ok) {
    throw new ApiError(0, $t({ id: "api.httpError", dm: "HTTP {status} {statusText}", vars: { status: resp.status, statusText: resp.statusText } }))
  }

  const env = (await resp.json()) as ApiResponse<T>
  if (env.code !== 0) {
    throw new ApiError(env.code, env.message)
  }
  // data is non-null on success; the caller guarantees the non-null semantics of T.
  return env.data as T
}