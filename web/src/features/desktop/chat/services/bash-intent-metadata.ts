export type BashCategory = 'read' | 'write' | 'update' | 'delete'

export interface BashIntentMetadata {
  command: string
  explanation: string[]
  category: BashCategory
  critical: boolean
}

export function parseBashIntentMetadata(raw: string): BashIntentMetadata | null {
  try {
    const parsed = JSON.parse(raw) as Record<string, unknown>
    const command = typeof parsed.command === 'string' ? parsed.command.trim() : ''
    const category = typeof parsed.category === 'string' ? parsed.category.trim().toLowerCase() : ''
    const explanation = Array.isArray(parsed.explanation)
      ? parsed.explanation.filter((item): item is string => typeof item === 'string').map((item) => item.trim()).filter(Boolean)
      : []
    if (!command || explanation.length === 0 || !['read', 'write', 'update', 'delete'].includes(category) || typeof parsed.critical !== 'boolean') {
      return null
    }
    if (category === 'delete' && !parsed.critical) {
      return null
    }
    return { command, explanation, category: category as BashCategory, critical: parsed.critical }
  } catch {
    return null
  }
}
