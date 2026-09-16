import type { AutomationDefinition, AutomationProgress } from './desktop-automation-api'

// Presentation only: never evaluate cron, reconstruct occurrence counts, or infer admission.
export function automationFrequency(schedule: AutomationDefinition['schedule']): string {
  if (schedule.kind === 'manual') return 'Manual'
  if (schedule.kind === 'event') return `Event · ${schedule.trigger_source || 'unavailable'}`
  if (schedule.kind === 'interval') {
    const seconds = schedule.interval_seconds
    return seconds ? `Every ${seconds % 3600 === 0 ? `${seconds / 3600} hours` : seconds % 60 === 0 ? `${seconds / 60} minutes` : `${seconds} seconds`} elapsed` : 'Interval unavailable'
  }
  const match = /^(\d+) (\d+) \* \* \*$/.exec(schedule.expression ?? '')
  if (match) return `Daily · ${match[2].padStart(2, '0')}:${match[1].padStart(2, '0')} ${schedule.timezone}`
  return `Cron · ${schedule.expression || 'unavailable'} · ${schedule.timezone || 'timezone unavailable'}`
}
export function automationTime(value: number | undefined, timezone: string): string {
  return value ? new Intl.DateTimeFormat(undefined, { timeZone: timezone, dateStyle: 'medium', timeStyle: 'short' }).format(value) : 'Unavailable'
}
export function automationProgressSummary(progress: AutomationProgress | undefined, stale: boolean, now: number): string {
  if (!progress) return 'Progress unavailable'
  if (stale || now < progress.day_start || now >= progress.day_end) return 'Progress stale — refresh required'
  const completed = progress.counts.completed ?? 0
  const exceptional = ['failed', 'blocked', 'cancelled', 'running', 'pending', 'cancelling', 'skipped'].filter(state => progress.counts[state] > 0).map(state => `${progress.counts[state]} ${state}`)
  // The backend exposes forecasts, not a meaningful complete daily quota. No denominator.
  return [automationFrequency(progress.schedule), `${progress.history_complete ? '' : 'at least '}${completed} completed`, ...exceptional,
    progress.next_eligible && !progress.no_next_reason ? `next ${automationTime(progress.next_eligible.scheduled_at, progress.display_timezone)} (conditional)` : (progress.no_next_reason ?? 'Next run unavailable').replace(/_/g, ' ')].join(' · ')
}
export function editedAutomationDefinition(draft: AutomationDefinition): AutomationDefinition {
  const { approval_reference: _grant, ...authorization } = draft.authorization
  const { timezone: _timezone, ...schedule } = draft.schedule
  if (schedule.kind === 'interval' && (!Number.isInteger(schedule.interval_seconds) || schedule.interval_seconds! < 60 || schedule.interval_seconds! > 366 * 86400)) throw new Error('Interval must be 60–31622400 whole seconds')
  if (schedule.kind === 'cron') {
    if (!draft.schedule.timezone || draft.schedule.timezone === 'Local') throw new Error('Explicit IANA timezone required')
    new Intl.DateTimeFormat('en', { timeZone: draft.schedule.timezone })
  }
  return { ...draft, enabled: false, schedule: schedule.kind === 'cron' ? { ...schedule, timezone: draft.schedule.timezone } : schedule, authorization: { ...authorization, mode: 'approval_required' } }
}
