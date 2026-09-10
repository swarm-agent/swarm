import type { AutomationDefinition, AutomationRecord } from '../../state/desktop-automation-api'

export function dayKey(time: number, timezone: string): string {
  const parts = new Intl.DateTimeFormat('en-US', { timeZone: timezone, year: 'numeric', month: '2-digit', day: '2-digit' }).formatToParts(time)
  return ['year', 'month', 'day'].map(type => parts.find(p => p.type === type)?.value).join('-')
}
// One local presentation timer at the next zone date boundary, including DST.
export function nextDayDelay(now: number, timezone: string): number {
  const day = dayKey(now, timezone)
  let low = now, high = now + 27 * 60 * 60 * 1000
  while (high - low > 1000) {
    const middle = Math.floor((low + high) / 2)
    if (dayKey(middle, timezone) === day) low = middle
    else high = middle
  }
  return high - now + 10
}
export function groupUpdates(records: AutomationRecord[], timezone: string): [string, AutomationRecord[]][] {
  const groups = new Map<string, AutomationRecord[]>()
  for (const record of [...records].sort((a, b) => b.written_at - a.written_at)) {
    const key = dayKey(record.written_at, timezone)
    groups.set(key, [...(groups.get(key) ?? []), record])
  }
  return [...groups]
}
export function needsAttention(record: AutomationRecord): boolean {
  return ['blocked', 'failed', 'needs_review', 'approval_required'].includes(record.occurrence?.state ?? record.outcome?.kind ?? '')
}
export function validatePlans(plans: AutomationDefinition['plans']): void {
  if (!plans.length || plans.length > 16) throw new Error('Associate 1–16 ordered plans.')
  const seen = new Set<string>()
  for (const binding of plans) {
    if (!binding.id.trim() || seen.has(binding.id) || !binding.plan.session_id.trim() || !binding.plan.plan_id.trim() || !Number.isSafeInteger(binding.plan.revision) || binding.plan.revision < 1) throw new Error('Each binding needs a unique ID and exact session, plan and revision.')
    const dependencies = binding.depends_on ?? []
    if (new Set(dependencies).size !== dependencies.length || dependencies.some(id => !seen.has(id))) throw new Error('Dependencies must refer only to earlier bindings.')
    seen.add(binding.id)
  }
}
export function parseInstructions(text: string): Record<string, string> {
  const value: unknown = JSON.parse(text)
  if (!value || typeof value !== 'object' || Array.isArray(value) || Object.values(value).some(item => typeof item !== 'string')) throw new Error('Instructions must be a JSON object of text values.')
  return value as Record<string, string>
}
