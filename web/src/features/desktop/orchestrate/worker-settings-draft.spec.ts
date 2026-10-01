import assert from 'node:assert/strict'
import test from 'node:test'
import { createWorkerSettingsDraft, reconcileWorkerSettingsDraft, workerSettingsChanges } from './worker-settings-draft'
import { resetWorkerModelSlot, selectWorkerModelSlot, workerModelOptionSelection, workerSlotInherited } from './worker-model-picker'
import type { WorkerRecord } from '../state/desktop-workers-api'
import type { ModelOptionRecord } from '../chat/types/chat'

const worker: WorkerRecord = { id: 'fixture', account_scope_id: 'account', name: 'Fixture', instructions: 'Work', lifecycle_state: 'active', revision: 2, created_at: 1, updated_at: 2 }

// Requirement: unchanged/default-equivalent policies cannot submit; mode and slot
// edits are independent and discard restores the candidate. Threat: misleading
// dirty state or policy loss. These production draft transformations are the
// narrowest deterministic layer, without a browser or mutation/provider fixture.
test('draft detects only effective policy changes and discard restores the baseline', () => {
  const initial = createWorkerSettingsDraft(worker)
  assert.deepEqual(workerSettingsChanges(initial.baseline, initial.value), [])
  const changed = { ...initial, value: { ...initial.value, mode: 'plan' as const } }
  assert.deepEqual(workerSettingsChanges(changed.baseline, changed.value), ['Execution'])
  const selection = { provider: 'fixture', model: 'long-model', thinking: 'high', service_tier: 'fast', context_mode: 'extended' }
  const pinned = selectWorkerModelSlot(undefined, 'plan', selection, selection)
  assert.equal(workerSlotInherited(pinned, 'action'), true)
  assert.equal(workerSlotInherited(pinned, 'plan'), false)
  assert.deepEqual(workerSettingsChanges(initial.value, { ...initial.value, profile: pinned }), ['Plan model'])
  const reset = resetWorkerModelSlot(pinned, 'plan')
  assert.deepEqual(workerSettingsChanges(initial.value, { ...initial.value, profile: reset }), [])
  assert.deepEqual(createWorkerSettingsDraft(worker), initial)
})

// Requirement: revisions refresh idle state, preserve active drafts with their
// original CAS, and never leak a draft across worker/account identity. Authority:
// reconcileWorkerSettingsDraft used by WorkerSettingsEditor. Pure state tests
// prove this boundary without claiming browser wiring or backend CAS execution.
test('revision and pending-review changes preserve edits until explicitly discarded', () => {
  const initial = createWorkerSettingsDraft(worker)
  const remote = { ...worker, revision: 3, pending_review: { ...worker, execution_mode: 'plan' as const } }
  const idle = reconcileWorkerSettingsDraft(initial, remote)
  assert.equal(idle.revision, 3)
  assert.equal(idle.value.mode, 'plan')
  const dirty = { ...initial, value: { ...initial.value, mode: 'plan' as const } }
  assert.equal(reconcileWorkerSettingsDraft(dirty, remote), dirty)
  assert.equal(dirty.revision, 2)
  assert.notEqual(dirty.key, createWorkerSettingsDraft(remote).key)
  assert.equal(createWorkerSettingsDraft(remote).value.mode, 'plan')
  assert.equal(reconcileWorkerSettingsDraft(dirty, { ...worker, id: 'second' }).value.mode, 'auto')
  assert.equal(reconcileWorkerSettingsDraft(dirty, { ...worker, account_scope_id: 'second-account' }).value.mode, 'auto')
  const accepted = { ...remote, revision: 4, execution_mode: 'plan' as const, pending_review: undefined }
  assert.equal(reconcileWorkerSettingsDraft(idle, accepted).revision, 4)
  const saved = { ...idle, saved: true }
  assert.equal(reconcileWorkerSettingsDraft(saved, worker), saved, 'late old props do not erase saved pending state')
})

// Requirement: source/reset actions preserve the other slot and all selected
// thinking/tier/context settings. Threat: opening or reselecting silently resets
// custom policy. selectWorkerModelSlot/workerModelOptionSelection own these edits;
// their pure outputs are the narrowest observable assertion layer.
test('pinning, selecting and resetting Plan preserves Action policy without mutation', () => {
  const action = { provider: 'fixture', model: 'action', thinking: 'high', service_tier: 'priority', context_mode: 'extended' }
  const plan = { ...action, model: 'plan' }
  const profile = { source: 'temporary', action, plan }
  const pinned = selectWorkerModelSlot(profile, 'plan', { ...plan, thinking: 'low' }, action)
  assert.deepEqual(pinned.action, action)
  assert.equal(pinned.plan?.thinking, 'low')
  const reset = resetWorkerModelSlot(pinned, 'plan')
  assert.equal(workerSlotInherited(reset, 'action'), false)
  assert.equal(workerSlotInherited(reset, 'plan'), true)
  assert.deepEqual(reset.action, action)
  const option = { provider: 'fixture', model: 'action', contextMode: 'extended', defaultThinking: 'low', defaultServiceTier: 'standard' } as ModelOptionRecord
  assert.deepEqual(workerModelOptionSelection(option, action), action)
  assert.equal(workerModelOptionSelection({ ...option, model: 'new' }, action).thinking, 'low')
  assert.equal(profile.plan.thinking, 'high')
  assert.equal(profile.action.service_tier, 'priority')
})
