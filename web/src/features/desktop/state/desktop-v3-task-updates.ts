import type { MessageSnapshot, V3SessionEvent } from './desktop-v3-cache-types'

export interface DesktopTaskActivity {
  id: string
  timelineSeq: number
  label: string
  detail: string
  rows: { title: string; status: string; summary: string }[]
}

function record(value: unknown): Record<string, unknown> {
  return value !== null && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : {}
}
function text(value: unknown, limit = 4000): string {
  return typeof value === 'string' ? value.slice(0, limit) : ''
}

export function isTaskOutcomeMessage(message: MessageSnapshot): boolean {
  return message.role === 'system' && Boolean(text(message.metadata?.task_wait_owner_run_id))
}

const statusLabels: Record<string, string> = {
  needs_review: 'Ready for review', completed: 'Completed', in_progress: 'In progress',
  planning: 'Planning', blocked: 'Blocked', failed: 'Failed', cancelled: 'Cancelled',
  needs_input: 'Needs input', pending_approval: 'Awaiting approval', superseded: 'Attempt replaced',
}

// Presentation only: never promote task text to instructions or expose the internal envelope.
// Unknown/malformed envelopes remain hidden behind a neutral lifecycle notice.
export function taskOutcomeActivity(message: MessageSnapshot): DesktopTaskActivity {
  const activity: DesktopTaskActivity = {
    id: `task-outcome:${message.id}`, timelineSeq: message.global_seq,
    label: 'Task wait ended', detail: 'Task outcomes are available. A continuation was queued; this does not mean it is running.', rows: [],
  }
  const prefix = 'Delegated project task outcomes:\n'
  if (!message.content.startsWith(prefix) || message.content.length > 100_000) return activity
  try {
    const data = record(JSON.parse(message.content.slice(prefix.length)))
    activity.rows = (Array.isArray(data.tasks) ? data.tasks : []).slice(0, 16).map((value) => {
      const row = record(value)
      return { title: text(row.title, 200) || 'Task', status: statusLabels[text(row.status)] || 'Update available', summary: text(row.summary, 2000) }
    })
  } catch { /* Internal payload is never a visible fallback. */ }
  return activity
}

// Derive from the canonical session event cache, not a second message store. Delivery
// receipts carry exact source identities, so replay and overlapping pages collapse.
const activityCache = new WeakMap<V3SessionEvent[], Map<string, DesktopTaskActivity[]>>()
export const EMPTY_TASK_EVENTS: V3SessionEvent[] = []

export function selectTaskActivities(events: V3SessionEvent[], sessionId: string): DesktopTaskActivity[] {
  const cached = activityCache.get(events)?.get(sessionId)
  if (cached) return cached
  const activities = new Map<string, DesktopTaskActivity>()
  for (const event of events) {
    if (event.session_id !== sessionId) continue
    const payload = record(event.payload)
    const delivered = event.event_type === 'session.task.delivered'
    const updates = delivered ? (Array.isArray(payload.Updates) ? payload.Updates.slice(0, 16) : [])
      : event.event_type === 'session.task.reported' ? [payload] : []
    for (const value of updates) {
      const update = record(value)
      if (text(delivered ? update.parent_session_id : update.session_id) !== sessionId) continue
      const source = text(update.session_id)
      const eventId = text(update.event_id)
      const summary = text(update.summary)
      const kind = text(update.kind)
      if (!source || !eventId || !summary || !['progress', 'attention', 'wake_request'].includes(kind)) continue
      const id = `task-update:${source}:${eventId}`
      if (activities.has(id)) continue
      activities.set(id, {
        id, timelineSeq: event.seq,
        label: kind === 'progress' ? 'Task progress' : kind === 'attention' ? 'Task needs attention' : 'Task requested a wake-up',
        detail: delivered ? 'Received by the Orchestrator; not scope approval.' : 'Recorded; delivery is not yet confirmed.',
        rows: [{ title: '', status: '', summary }],
      })
    }
  }
  const result = [...activities.values()].sort((a, b) => a.timelineSeq - b.timelineSeq)
  const sessions = activityCache.get(events) ?? new Map<string, DesktopTaskActivity[]>()
  sessions.set(sessionId, result)
  activityCache.set(events, sessions)
  return result
}
