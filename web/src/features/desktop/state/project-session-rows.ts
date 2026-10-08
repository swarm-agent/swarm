import type { DesktopV3CacheState, SessionSnapshot } from './desktop-v3-cache-types'
import { buildDesktopSidebarPlanState, selectSessionRunIntents, summarizeDesktopV3TaskToolActivity } from './desktop-v3-cache-selectors'
import { buildDesktopV3RunStatusModel } from '../chat/components/desktop-v3-run-status'
import type { DesktopV3RunStatusModel } from '../chat/components/desktop-v3-run-status'

export interface ProjectSessionRow {
  session: SessionSnapshot
  active: boolean
  attention: boolean
  label: string
  timer: DesktopV3RunStatusModel | null
  activityAt: number
  archivedVersion?: number
}

// These are view models over the canonical cache, not a second session authority.
export function projectSessionRow(state: DesktopV3CacheState, session: SessionSnapshot, archivedVersion?: number): ProjectSessionRow {
  const intent = state.currentRunIntentBySession[session.id] || selectSessionRunIntents(state, session.id).slice(-1)[0]
  const active = !archivedVersion && ['running', 'pending_executor', 'dispatch_blocked'].includes(intent?.status || '')
  const permissions = state.permissionSummaryBySessionId[session.id]?.pendingApprovalCount || 0
  const plan = buildDesktopSidebarPlanState(state, session.id).planExecution
  const timer = archivedVersion ? null : buildDesktopV3RunStatusModel({ currentRunIntent: intent, latestRunIntent: intent })
  const live = intent?.run_id ? state.liveRunsBySession[session.id]?.[intent.run_id] : undefined
  const tool = active && live ? summarizeDesktopV3TaskToolActivity(Object.values(live.toolCallsByCallId)) : ''
  const attention = !archivedVersion && Boolean(permissions || plan?.blocked || plan?.failed || plan?.reviewRequired || ['dispatch_blocked', 'failed', 'interrupted', 'expired'].includes(intent?.status || ''))
  const label = archivedVersion ? 'Archived' : permissions ? 'Needs approval' : plan?.blocked ? 'Blocked' : plan?.failed ? 'Failed'
    : plan?.reviewRequired ? 'Needs review' : intent?.status === 'dispatch_blocked' ? 'Blocked'
    : active ? tool || timer?.label || 'Running' : timer?.label || 'Idle'
  // Never sort on projection/usage timestamps or the ticking display clock.
  const activityAt = Math.max(session.last_message_at || 0, intent?.completed_at || 0, intent?.started_at || 0, session.created_at || 0)
  return { session, active, attention, label, timer, activityAt, archivedVersion }
}

export function compareProjectSessionRows(a: ProjectSessionRow, b: ProjectSessionRow): number {
  return Number(b.active) - Number(a.active) || b.activityAt - a.activityAt || a.session.id.localeCompare(b.session.id)
}
