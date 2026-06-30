import assert from 'node:assert/strict'
import test from 'node:test'

import {
  localContainerUpdateWarningDismissed,
  normalizeFollowupCheckpointPolicyDefault,
  normalizeSessionMode,
  normalizeSwarmSettings,
  withDefaultNewSessionMode,
  withLocalContainerUpdateWarningDismissed,
  type UISettingsWire,
} from './swarm-settings'

test('normalizeSessionMode only accepts Desktop session modes', () => {
  assert.equal(normalizeSessionMode('plan'), 'plan')
  assert.equal(normalizeSessionMode('auto'), 'auto')
  assert.equal(normalizeSessionMode('readwrite'), 'auto')
  assert.equal(normalizeSessionMode(undefined), 'auto')
})

test('local container update warning dismissal reads backend UI update setting only', () => {
  assert.equal(localContainerUpdateWarningDismissed(null), false)
  assert.equal(localContainerUpdateWarningDismissed({ updates: {} }), false)
  assert.equal(localContainerUpdateWarningDismissed({ updates: { local_container_warning_dismissed: true } }), true)
})

test('withDefaultNewSessionMode preserves existing chat fields while updating default mode', () => {
  const current: UISettingsWire = {
    theme: { active_id: 'crimson' },
    chat: { thinking_tags: false, default_workspace_routes: { '/repo': 'self' } },
    swarm: { name: 'Primary' },
  }

  assert.deepEqual(withDefaultNewSessionMode(current, 'plan'), {
    ...current,
    chat: { thinking_tags: false, default_workspace_routes: { '/repo': 'self' }, default_new_session_mode: 'plan' },
  })
})

test('withLocalContainerUpdateWarningDismissed preserves existing settings while updating local warning flag', () => {
  const current: UISettingsWire = {
    theme: { active_id: 'crimson' },
    chat: { thinking_tags: false, default_new_session_mode: 'plan' },
    swarm: { name: 'Primary' },
    updates: { local_container_warning_dismissed: false },
  }

  assert.deepEqual(withLocalContainerUpdateWarningDismissed(current, true), {
    ...current,
    updates: { local_container_warning_dismissed: true },
  })
})

test('follow-up checkpoint policy default normalizes missing and unknown values to auto-start', () => {
  assert.equal(normalizeFollowupCheckpointPolicyDefault(undefined), 'auto_start')
  assert.equal(normalizeFollowupCheckpointPolicyDefault(null), 'auto_start')
  assert.equal(normalizeFollowupCheckpointPolicyDefault(''), 'auto_start')
  assert.equal(normalizeFollowupCheckpointPolicyDefault('unexpected'), 'auto_start')
  assert.equal(normalizeSwarmSettings({}).followupCheckpointPolicyDefault, 'auto_start')
})

test('follow-up checkpoint policy default preserves ask-first aliases', () => {
  assert.equal(normalizeFollowupCheckpointPolicyDefault('require_approval'), 'require_approval')
  assert.equal(normalizeFollowupCheckpointPolicyDefault('ask'), 'require_approval')
  assert.equal(normalizeFollowupCheckpointPolicyDefault('manual'), 'require_approval')
  assert.equal(normalizeSwarmSettings({ chat: { followup_checkpoint_policy_default: 'require_approval' } }).followupCheckpointPolicyDefault, 'require_approval')
})
