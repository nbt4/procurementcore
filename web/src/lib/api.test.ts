import { afterEach, describe, expect, it, vi } from 'vitest'
import { api, date, euro } from './api'

afterEach(() => vi.restoreAllMocks())

describe('formatters', () => {
  it('formats integer cents without floating point leakage', () => expect(euro(123456)).toMatch(/1\.234,56/))
  it('uses an en dash for missing dates', () => expect(date()).toBe('–'))
})

describe('api', () => {
  it('lets the browser add the multipart boundary for FormData', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response('{}', {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    }))
    const form = new FormData()
    form.append('file', new Blob(['%PDF-test'], { type: 'application/pdf' }), 'order.pdf')

    await api('/orders/import-preview', { method: 'POST', body: form })

    const headers = fetchMock.mock.calls[0][1]?.headers as Headers
    expect(headers.has('Content-Type')).toBe(false)
  })
})
