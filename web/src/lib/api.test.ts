import { describe, expect, it } from 'vitest'
import { date, euro } from './api'

describe('formatters', () => {
  it('formats integer cents without floating point leakage', () => expect(euro(123456)).toMatch(/1\.234,56/))
  it('uses an en dash for missing dates', () => expect(date()).toBe('–'))
})
