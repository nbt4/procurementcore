import type { ParameterDefinition } from './types'

export type ParameterDraft = {
  label: string
  type: ParameterDefinition['type']
  unit: string
  options: string
}

export const emptyParameter = (): ParameterDraft => ({ label:'', type:'text', unit:'', options:'' })

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
    const key = keyFromLabel(label)
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
