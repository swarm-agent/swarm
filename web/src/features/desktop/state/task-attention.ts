import type { TaskSessionCandidate } from '../runtime/desktop-projects-membership'
import { extractTaskSessionIds } from '../runtime/desktop-projects-membership'
import { parseAskUserPermission, permissionKind } from '../permissions/services/permission-payload'
import type { DesktopPermissionRecord } from '../types/realtime'
import type { DesktopSessionPlanRecord } from '../chat/types/chat'
import type { DesktopV3CacheState } from './desktop-v3-cache-types'
import { selectDesktopPendingPermissions } from './desktop-v3-cache-selectors'

// Membership is durable lineage, never titles or currently selected conversations.
export function taskAttentionSessionIds(state: DesktopV3CacheState, task: TaskSessionCandidate): string[] {
  const roots = extractTaskSessionIds(task)
  const owner = state.sessionsById[task.sessionId || roots[0]]
  if (owner?.kind !== 'full') return task.sessionId ? [task.sessionId] : roots.slice(0, 1)
  const allowed = (id: string) => {
    const record = state.sessionsById[id]
    return record?.kind === 'full' && !state.tombstonesBySession[id]
      && record.session.account_scope_id === owner.session.account_scope_id
      && record.session.user_id === owner.session.user_id
      && (!task.id || !record.session.metadata?.task_id || record.session.metadata.task_id === task.id)
      && (!task.id || !record.session.metadata?.project_task_id || record.session.metadata.project_task_id === task.id)
  }
  const excluded = new Set<string>()
  // Superseded program generations and their descendants cannot poison a new attempt.
  for (const source of [task.taskProgramStatus, task.task_program_status, task.taskProgram, task.task_program]) {
    for (const job of source?.jobs || []) {
      const current = job.current_session_id || job.child_session_id
      for (const generation of job.generation_history || []) {
        if (generation.session_id && generation.session_id !== current) excluded.add(generation.session_id)
      }
    }
  }
  for (const root of [...roots]) {
    const plan = state.plansBySession[root] as DesktopSessionPlanRecord | undefined
    const current = plan?.document?.executionState?.currentSessionId
    if (current && !roots.includes(current) && allowed(current)) roots.push(current)
    for (const checkpoint of plan?.document?.checkpoints || []) {
      for (const attempt of checkpoint.attempts || []) {
        if (attempt.sessionId && attempt.sessionId !== root && attempt.sessionId !== current) excluded.add(attempt.sessionId)
      }
    }
  }
  // Explicit task bindings can demand an authorized hydration before their
  // principal shell is present; they cannot expose permissions until hydrated.
  const ids = new Set(roots.filter(id => !excluded.has(id) && !state.tombstonesBySession[id]
    && (!state.sessionsById[id] || allowed(id))))
  let changed = true
  while (changed) {
    changed = false
    for (const [id, record] of Object.entries(state.sessionsById)) {
      if (record.kind !== 'full') continue
      const parent = record.session.metadata?.parent_session_id
      if (typeof parent !== 'string') continue
      if (excluded.has(parent) && !excluded.has(id)) { excluded.add(id); changed = true }
      if (ids.has(parent) && !ids.has(id) && !excluded.has(id) && allowed(id)) {
        ids.add(id); changed = true
      }
    }
  }
  return [...ids].filter(id => !excluded.has(id)).sort()
}

export function taskAttentionPermissions(state: DesktopV3CacheState, ids: readonly string[]): DesktopPermissionRecord[] {
  return ids.filter(id => state.sessionsById[id]?.kind === 'full' && !state.tombstonesBySession[id]).flatMap(id => selectDesktopPendingPermissions(state, id).filter(permission =>
    permission.sessionId === id))
    .sort((a, b) => a.createdAt - b.createdAt || a.id.localeCompare(b.id))
}

export function taskAttentionLabel(permission: DesktopPermissionRecord): string {
  return permissionKind(permission) === 'ask-user' ? 'Needs your input' : 'Approval required'
}

export function taskAttentionContext(permission: DesktopPermissionRecord): string {
  if (permissionKind(permission) === 'ask-user') {
    const payload = parseAskUserPermission(permission)
    return payload?.questions.map(question => question.question).join(' · ') || permission.reason || 'The AI has a question for you'
  }
  return `${permission.toolName}: ${[permission.reason, permission.toolArguments || permission.requirement].filter(Boolean).join(' · ')}`
}

export type TaskAttentionDecision = 'approve' | 'deny' | 'approve_always' | 'always_allow' | 'always_deny'
// A shared single-flight guard prevents double decisions across multiple mounted cards.
const decisions = new Set<string>()
export async function submitTaskAttentionDecision(
  permission: DesktopPermissionRecord,
  action: TaskAttentionDecision,
  reason: string,
  approvedArguments: Record<string, unknown> | undefined,
  deps: {
    getState: () => DesktopV3CacheState
    resolve: (sessionId: string, requestId: string, action: TaskAttentionDecision, reason: string, args?: Record<string, unknown>) => Promise<DesktopPermissionRecord | null>
    commit: (permission: DesktopPermissionRecord | null) => void
  },
): Promise<void> {
  const key = JSON.stringify([permission.sessionId, permission.id])
  const current = taskAttentionPermissions(deps.getState(), [permission.sessionId]).find(item => item.id === permission.id)
  if (!current || current.runId !== permission.runId || current.callId !== permission.callId || current.updatedAt !== permission.updatedAt || current.toolArguments !== permission.toolArguments) throw new Error('This request changed or was resolved. Review the current request.')
  if (decisions.has(key)) throw new Error('A decision is already being submitted for this request.')
  decisions.add(key)
  try {
    const resolved = await deps.resolve(permission.sessionId, permission.id, action, reason, approvedArguments)
    if (!resolved || resolved.sessionId !== permission.sessionId || resolved.id !== permission.id || resolved.runId !== permission.runId || resolved.callId !== permission.callId || resolved.status === 'pending') {
      throw new Error('The decision did not return a resolved request. Refresh and retry.')
    }
    deps.commit(resolved)
  } finally {
    decisions.delete(key)
  }
}
