import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, it } from 'vitest'
import type { RequisitionLine } from '../lib/types'
import { ProductLinks } from './RequisitionsPage'

const line = (overrides: Partial<RequisitionLine> = {}): RequisitionLine => ({
  description: 'Mikrofon',
  quantity: 1,
  unit: 'Stk.',
  estimatedPriceCents: 10900,
  purchaseUrl: '',
  ...overrides,
})

const renderLinks = (value: RequisitionLine) => renderToStaticMarkup(
  <MemoryRouter><ProductLinks line={value}/></MemoryRouter>,
)

describe('ProductLinks', () => {
  it('verlinkt Katalogartikel und hinterlegte Produktseite', () => {
    const html = renderLinks(line({ productId: 42, purchaseUrl: 'https://shop.example/mikrofon' }))

    expect(html).toContain('href="/catalog/42"')
    expect(html).toContain('href="https://shop.example/mikrofon"')
    expect(html).toContain('target="_blank"')
  })

  it('kennzeichnet eine Freitextposition ohne erfundene Links', () => {
    const html = renderLinks(line())

    expect(html).toContain('Freitextposition')
    expect(html).not.toContain('href=')
  })
})
