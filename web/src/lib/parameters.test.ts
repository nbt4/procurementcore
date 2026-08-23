import { describe, expect, it } from 'vitest'
import { buildParameterSchema } from './parameters'

describe('buildParameterSchema', () => {
  it('creates API schema without exposing JSON editing', () => {
    expect(buildParameterSchema([
      { label:'Leistung in Watt', type:'number', unit:'W', options:'' },
      { label:'Farbe', type:'select', unit:'', options:'Schwarz, Weiß' },
    ])).toEqual([
      { key:'leistung_in_watt', label:'Leistung in Watt', type:'number', unit:'W', options:undefined },
      { key:'farbe', label:'Farbe', type:'select', unit:undefined, options:['Schwarz','Weiß'] },
    ])
  })

  it('rejects duplicate generated keys', () => {
    expect(() => buildParameterSchema([
      { label:'Größe', type:'text', unit:'', options:'' },
      { label:'Große', type:'text', unit:'', options:'' },
    ])).toThrow('eindeutig')
  })
})
