import { appPath, centralLoginURL } from './app-paths'
import { suiteLocale } from './cores-design'

export const apiBase = appPath('/api/v1')

export async function api<T>(path: string, options: RequestInit = {}): Promise<T> {
  const headers = new Headers(options.headers)
  if (!(options.body instanceof FormData) && !headers.has('Content-Type')) headers.set('Content-Type', 'application/json')
  const response = await fetch(`${apiBase}${path}`, {
    credentials: 'include',
    ...options,
    headers,
  })
  if (response.status === 401) {
    window.location.href = centralLoginURL()
    throw new Error('Nicht angemeldet')
  }
  if (!response.ok) {
    const body = await response.json().catch(() => ({ error: 'Unbekannter Fehler' }))
    throw new Error(body.error || `HTTP ${response.status}`)
  }
  if (response.status === 204) return undefined as T
  return response.json() as Promise<T>
}

export const euro = (cents = 0) => new Intl.NumberFormat(suiteLocale(), { style: 'currency', currency: 'EUR' }).format(cents / 100)
export const date = (value?: string) => value ? new Intl.DateTimeFormat(suiteLocale()).format(new Date(value)) : '–'
export const dateTime = (value?: string) => value ? new Intl.DateTimeFormat(suiteLocale(), { dateStyle: 'short', timeStyle: 'short' }).format(new Date(value)) : '–'
