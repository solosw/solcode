/** ACP v1 select options. Values/IDs are opaque; category is only a UI hint. */
export interface ConfigValue {
  value: string
  name: string
  description?: string
}
export interface ConfigGroup {
  group: string
  name: string
  options: ConfigValue[]
}
export interface ConfigOption {
  id: string
  name: string
  category?: string
  description?: string
  type: 'select'
  currentValue: string
  options: (ConfigValue | ConfigGroup)[]
}

function record(value: unknown): Record<string, unknown> | undefined {
  return typeof value === 'object' && value !== null ? value as Record<string, unknown> : undefined
}
function values(input: unknown): ConfigValue[] {
  if (!Array.isArray(input)) return []
  return input.flatMap((item: unknown) => {
    const r = record(item)
    if (r === undefined || typeof r.value !== 'string' || typeof r.name !== 'string') return []
    const value: ConfigValue = { value: r.value, name: r.name }
    if (typeof r.description === 'string') value.description = r.description
    return [value]
  })
}
export function hasConfigValue(option: ConfigOption, value: string): boolean {
  return option.options.some(item => 'group' in item
    ? item.options.some(candidate => candidate.value === value)
    : item.value === value)
}

/** Unknown types/categories must not break a conversation. A full snapshot
 * replaces the previous one, including when every selector has disappeared. */
export function readConfigOptions(input: unknown): ConfigOption[] {
  if (!Array.isArray(input)) return []
  const seen = new Set<string>()
  return input.flatMap((item: unknown) => {
    const r = record(item)
    if (r === undefined || r.type !== 'select' || typeof r.id !== 'string' || r.id === '' || seen.has(r.id) || typeof r.name !== 'string' || typeof r.currentValue !== 'string' || !Array.isArray(r.options)) return []
    const options: (ConfigValue | ConfigGroup)[] = r.options.flatMap((entry: unknown): (ConfigValue | ConfigGroup)[] => {
      const group = record(entry)
      if (group !== undefined && typeof group.group === 'string' && typeof group.name === 'string') {
        return [{ group: group.group, name: group.name, options: values(group.options) }]
      }
      return values([entry])
    })
    const option: ConfigOption = { id: r.id, name: r.name, type: 'select', currentValue: r.currentValue, options }
    if (typeof r.category === 'string') option.category = r.category
    if (typeof r.description === 'string') option.description = r.description
    if (!hasConfigValue(option, option.currentValue)) return []
    seen.add(option.id)
    return [option]
  })
}
