import type { WorkerModelProfile, WorkerRecord } from '../state/desktop-workers-api'
import { workerSlotInherited } from './worker-model-picker'

export interface WorkerSettingsValue { mode: 'auto' | 'plan'; profile?: WorkerModelProfile | null }
export interface WorkerSettingsDraft {
  key: string
  revision: number
  baseline: WorkerSettingsValue
  value: WorkerSettingsValue
  saved: boolean
}
export function workerSettingsValue(worker: WorkerRecord): WorkerSettingsValue {
  const candidate = worker.pending_review || worker
  return { mode: candidate.execution_mode || 'auto', profile: candidate.model_profile }
}
function slotPolicy(profile: WorkerModelProfile | null | undefined, slot: 'action' | 'plan') {
  if (workerSlotInherited(profile, slot)) return 'default'
  const value = profile?.[slot]
  return [value?.provider || '', value?.model || '', value?.thinking || '', value?.service_tier || '', value?.context_mode || '']
}
export function workerSettingsChanges(before: WorkerSettingsValue, after: WorkerSettingsValue): string[] {
  const fields: string[] = []
  if (before.mode !== after.mode) fields.push('Execution')
  for (const slot of ['action', 'plan'] as const) {
    if (JSON.stringify(slotPolicy(before.profile, slot)) !== JSON.stringify(slotPolicy(after.profile, slot))) fields.push(slot === 'action' ? 'Action model' : 'Plan model')
  }
  return fields
}
export function createWorkerSettingsDraft(worker: WorkerRecord): WorkerSettingsDraft {
  const value = workerSettingsValue(worker)
  return { key: JSON.stringify([worker.account_scope_id, worker.id, worker.revision, value, worker.lifecycle_state, !!worker.pending_review]), revision: worker.revision, baseline: value, value, saved: false }
}
/** A remote revision never silently overwrites unsaved edits or rebases their CAS. */
export function reconcileWorkerSettingsDraft(draft: WorkerSettingsDraft, worker: WorkerRecord): WorkerSettingsDraft {
  const next = createWorkerSettingsDraft(worker)
  if (draft.key === next.key) return draft
  const oldIdentity = JSON.parse(draft.key) as unknown[]
  if (oldIdentity[0] !== worker.account_scope_id || oldIdentity[1] !== worker.id) return next
  if (draft.saved && worker.revision < draft.revision) return draft
  return workerSettingsChanges(draft.baseline, draft.value).length ? draft : next
}
