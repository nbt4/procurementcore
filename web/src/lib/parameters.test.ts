import { describe, expect, it } from 'vitest'
import { buildParameterSchema, editParameters, mapImportedParameters } from './parameters'

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

  it('preserves stored keys while editing labels', () => {
    const drafts = editParameters([{ key:'leistung', label:'Leistung', type:'number', unit:'W' }])
    drafts[0].label = 'Nennleistung'
    expect(buildParameterSchema(drafts)[0]).toMatchObject({ key:'leistung', label:'Nennleistung' })
  })

  it('maps scraped attributes to category fields', () => {
    expect(mapImportedParameters([
      { key:'leistung', label:'Leistung', type:'number', unit:'W' },
      { key:'dimmbar', label:'Dimmbar', type:'boolean' },
    ], { Leistung:'80 W', Dimmbar:'Ja', Ignoriert:'Wert' })).toEqual({ leistung:80, dimmbar:true })
  })
})
