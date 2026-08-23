const pathname = typeof window === 'undefined' ? '/' : window.location.pathname
const proxied = pathname === '/procurement' || pathname.startsWith('/procurement/')
export const apiBase = proxied ? '/api/v1/procurement' : '/api/v1'

export async function api<T>(path: string, options: RequestInit = {}): Promise<T> {
  const response = await fetch(`${apiBase}${path}`, {
    credentials: 'include',
    ...options,
    headers: { 'Content-Type': 'application/json', ...(options.headers || {}) },
  })
  if (response.status === 401) {
    const redirect = encodeURIComponent(window.location.pathname + window.location.search)
    window.location.href = `${window.__DASHBOARD_URL__ || ''}/login?redirect=${redirect}`
    throw new Error('Nicht angemeldet')
  }
  if (!response.ok) {
    const body = await response.json().catch(() => ({ error: 'Unbekannter Fehler' }))
    throw new Error(body.error || `HTTP ${response.status}`)
  }
  if (response.status === 204) return undefined as T
  return response.json() as Promise<T>
}

export const euro = (cents = 0) => new Intl.NumberFormat('de-DE', { style: 'currency', currency: 'EUR' }).format(cents / 100)
export const date = (value?: string) => value ? new Intl.DateTimeFormat('de-DE').format(new Date(value)) : '–'
export const dateTime = (value?: string) => value ? new Intl.DateTimeFormat('de-DE', { dateStyle: 'short', timeStyle: 'short' }).format(new Date(value)) : '–'
