import type { ParameterDefinition } from './types'

export type ParameterDraft = {
  key?: string
  label: string
  type: ParameterDefinition['type']
  unit: string
  options: string
}

export const emptyParameter = (): ParameterDraft => ({ label:'', type:'text', unit:'', options:'' })

export const editParameters = (schema: ParameterDefinition[] = []): ParameterDraft[] => schema.map(parameter => ({
  key:parameter.key,
  label:parameter.label,
  type:parameter.type,
  unit:parameter.unit || '',
  options:parameter.options?.join(', ') || '',
}))

const keyFromLabel = (label: string) => label
  .trim()
  .toLowerCase()
  .replace(/ß/g, 'ss')
  .normalize('NFD')
  .replace(/[\u0300-\u036f]/g, '')
  .replace(/[^a-z0-9]+/g, '_')
  .replace(/^_+|_+$/g, '')

export function buildParameterSchema(drafts: ParameterDraft[]): ParameterDefinition[] {
  const schema = drafts.map((draft, index) => {
    const label = draft.label.trim()
    const key = draft.key || keyFromLabel(label)
    if (!label || !key) throw new Error(`Parameter ${index + 1} benötigt einen Namen.`)
    const options = draft.type === 'select'
      ? draft.options.split(',').map(option => option.trim()).filter(Boolean)
      : undefined
    if (draft.type === 'select' && !options?.length) {
      throw new Error(`Für „${label}“ muss mindestens eine Auswahlmöglichkeit angegeben werden.`)
    }
    return { key, label, type:draft.type, unit:draft.unit.trim() || undefined, options }
  })
  if (new Set(schema.map(parameter => parameter.key)).size !== schema.length) {
    throw new Error('Parameternamen müssen eindeutig sein.')
  }
  return schema
}

export function mapImportedParameters(schema: ParameterDefinition[] = [], attributes: Record<string,string> = {}): Record<string,string|number|boolean> {
  const normalized = new Map(Object.entries(attributes).map(([name,value]) => [keyFromLabel(name),value]))
  const result:Record<string,string|number|boolean> = {}
  for (const parameter of schema) {
    const raw = normalized.get(keyFromLabel(parameter.label)) ?? normalized.get(keyFromLabel(parameter.key))
    if (raw === undefined) continue
    if (parameter.type === 'number') {
      const match = raw.replace(',', '.').match(/-?\d+(?:\.\d+)?/)
      if (match) result[parameter.key] = Number(match[0])
    } else if (parameter.type === 'boolean') {
      const value = raw.trim().toLowerCase()
      if (['ja','true','yes','1'].includes(value)) result[parameter.key] = true
      if (['nein','false','no','0'].includes(value)) result[parameter.key] = false
    } else if (parameter.type === 'select') {
      const option = parameter.options?.find(value => value.toLowerCase() === raw.trim().toLowerCase())
      if (option) result[parameter.key] = option
    } else {
      result[parameter.key] = raw
    }
  }
  return result
}
