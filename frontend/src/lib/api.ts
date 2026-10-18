// API client: relative URLs (served through the frontend's nginx / Vite proxy),
// bearer token from localStorage, server error text surfaced as Error.message.

const TOKEN_KEY = "token"

export class ApiError extends Error {
  status: number

  constructor(status: number, message: string) {
    super(message)
    this.status = status
  }
}

export const getToken = () => localStorage.getItem(TOKEN_KEY)
export const setToken = (token: string) => localStorage.setItem(TOKEN_KEY, token)

export function logout() {
  localStorage.removeItem(TOKEN_KEY)
  window.location.href = "/login"
}

interface TokenClaims {
  username?: string
  role?: string
  exp?: number
}

export function tokenClaims(): TokenClaims | null {
  const token = getToken()
  if (!token) return null
  try {
    const payload = token.split(".")[1].replace(/-/g, "+").replace(/_/g, "/")
    return JSON.parse(atob(payload))
  } catch {
    return null
  }
}

export function isLoggedIn(): boolean {
  const claims = tokenClaims()
  return !!claims && (!claims.exp || claims.exp * 1000 > Date.now())
}

export const currentRole = () => tokenClaims()?.role ?? "Viewer"
export const canEdit = () => ["Admin", "Operator"].includes(currentRole())
export const isAdmin = () => currentRole() === "Admin"

type ApiInit = Omit<RequestInit, "body"> & { json?: unknown }

export async function api<T = void>(path: string, init: ApiInit = {}): Promise<T> {
  const { json, ...rest } = init
  const headers = new Headers(rest.headers)
  const token = getToken()
  if (token) headers.set("Authorization", `Bearer ${token}`)
  let body: string | undefined
  if (json !== undefined) {
    headers.set("Content-Type", "application/json")
    body = JSON.stringify(json)
  }

  const res = await fetch(`/api/v1${path}`, { ...rest, headers, body })
  if (res.status === 401 && path !== "/auth/login") {
    logout()
    throw new ApiError(401, "Session expired")
  }
  if (!res.ok) {
    const text = (await res.text()).trim()
    throw new ApiError(res.status, text || res.statusText)
  }
  if (!(res.headers.get("Content-Type") || "").includes("application/json")) {
    return undefined as T
  }
  return res.json() as Promise<T>
}

export const errorMessage = (err: unknown) => (err instanceof Error ? err.message : String(err))
