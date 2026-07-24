import assert from 'node:assert/strict'
import test from 'node:test'

import {
  DEFAULT_GLOBAL_THEME_ID,
  DEFAULT_SIDEBAR_HIDE_INACTIVE_HOURS,
  normalizeFollowupCheckpointPolicyDefault,
  normalizeGlobalThemeSettings,
  normalizeDesignerAgentSettings,
  normalizeExplorerAgentSettings,
  normalizeSessionMode,
  normalizeSidebarHideInactiveHours,
  normalizeSwarmSettings,
  withDefaultNewSessionMode,
  withDesignerAgentSettings,
  withExplorerAgentSettings,
  withSidebarHideInactiveHours,
  type UISettingsWire,
} from './swarm-settings'

test('normalizeSessionMode only accepts Desktop session modes', () => {
  assert.equal(normalizeSessionMode('plan'), 'plan')
  assert.equal(normalizeSessionMode('auto'), 'auto')
  assert.equal(normalizeSessionMode('readwrite'), 'auto')
  assert.equal(normalizeSessionMode(undefined), 'auto')
})

test('global theme settings default to Crimson when unset', () => {
  assert.equal(normalizeGlobalThemeSettings({}).activeId, DEFAULT_GLOBAL_THEME_ID)
  assert.equal(normalizeGlobalThemeSettings(null).activeId, DEFAULT_GLOBAL_THEME_ID)
  assert.equal(normalizeGlobalThemeSettings({ theme: { active_id: '  ' } }).activeId, DEFAULT_GLOBAL_THEME_ID)
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

test('sidebar inactivity threshold defaults to 12 hours and preserves explicit Never', () => {
  assert.equal(normalizeSidebarHideInactiveHours(undefined), DEFAULT_SIDEBAR_HIDE_INACTIVE_HOURS)
  assert.equal(normalizeSidebarHideInactiveHours(24), 24)
  assert.equal(normalizeSidebarHideInactiveHours(null), null)
  assert.deepEqual(withSidebarHideInactiveHours({ chat: { thinking_tags: true } }, null), {
    chat: { thinking_tags: true, sidebar_hide_inactive_hours: 0 },
  })
})

test('follow-up checkpoint policy default normalizes missing and unknown values to auto-start', () => {
  assert.equal(normalizeFollowupCheckpointPolicyDefault(undefined), 'auto_start')
  assert.equal(normalizeFollowupCheckpointPolicyDefault(null), 'auto_start')
  assert.equal(normalizeFollowupCheckpointPolicyDefault(''), 'auto_start')
  assert.equal(normalizeFollowupCheckpointPolicyDefault('unexpected'), 'auto_start')
  assert.equal(normalizeSwarmSettings({}).followupCheckpointPolicyDefault, 'auto_start')
})

test('Explorer priority normalizes and persists through its canonical settings path', () => {
  const current: UISettingsWire = { agents: { explorer: { provider: 'codex', model: 'gpt-5.4', thinking: 'high' } } }
  const saved = withExplorerAgentSettings(current, { provider: 'CODEX', model: 'gpt-5.4', thinking: 'high', service_tier: 'PRIORITY' })
  assert.equal(normalizeExplorerAgentSettings(saved).service_tier, 'priority')
})

test('Designer settings normalize and persist through the immutable system-agent path', () => {
  const current: UISettingsWire = { agents: { explorer: { provider: 'anthropic', model: 'claude' } } }
  const saved = withDesignerAgentSettings(current, { provider: 'CODEX', model: 'gpt-5.4-mini', thinking: 'medium', service_tier: 'PRIORITY' })
  assert.deepEqual(normalizeDesignerAgentSettings(saved), {
    provider: 'codex',
    model: 'gpt-5.4-mini',
    thinking: 'medium',
    service_tier: 'priority',
  })
  assert.equal(saved.agents?.explorer?.model, 'claude')
})

test('follow-up checkpoint policy default preserves ask-first aliases', () => {
  assert.equal(normalizeFollowupCheckpointPolicyDefault('require_approval'), 'require_approval')
  assert.equal(normalizeFollowupCheckpointPolicyDefault('ask'), 'require_approval')
  assert.equal(normalizeFollowupCheckpointPolicyDefault('manual'), 'require_approval')
  assert.equal(normalizeSwarmSettings({ chat: { followup_checkpoint_policy_default: 'require_approval' } }).followupCheckpointPolicyDefault, 'require_approval')
})
