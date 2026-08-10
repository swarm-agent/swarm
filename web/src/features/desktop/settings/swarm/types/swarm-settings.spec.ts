import assert from 'node:assert/strict'
import test from 'node:test'

import {
  DEFAULT_SIDEBAR_HIDE_INACTIVE_HOURS,
  normalizeFollowupCheckpointPolicyDefault,
  normalizeGlobalThemeSettings,
  normalizePlanContextGuardSettings,
  normalizePlanContextGuardUsedPercent,
  normalizePlanContextGuardMaxCompactions,
  normalizeSessionMode,
  normalizeShowTipsEnabled,
  normalizeSidebarHideInactiveHours,
  normalizeSwarmSettings,
  withDefaultNewSessionMode,
  withPlanContextGuardSettings,
  withSidebarHideInactiveHours,
  type UISettingsWire,
} from './swarm-settings'

test('normalizeSessionMode only accepts Desktop session modes', () => {
  assert.equal(normalizeSessionMode('plan'), 'plan')
  assert.equal(normalizeSessionMode('auto'), 'auto')
  assert.equal(normalizeSessionMode('readwrite'), 'auto')
  assert.equal(normalizeSessionMode(undefined), 'auto')
})

test('global theme settings use the canonical daemon default when active theme is unset', () => {
  assert.equal(normalizeGlobalThemeSettings({ theme: { default_theme_id: 'tide' } }).activeId, 'tide')
  assert.equal(normalizeGlobalThemeSettings({ theme: { active_id: '  ', default_theme_id: 'castor' } }).activeId, 'castor')
  assert.equal(normalizeGlobalThemeSettings(null).activeId, '')
})

test('home tips default on and preserve an explicit disabled setting', () => {
  assert.equal(normalizeShowTipsEnabled(undefined), true)
  assert.equal(normalizeShowTipsEnabled({}), true)
  assert.equal(normalizeShowTipsEnabled({ chat: { show_tips: true } }), true)
  assert.equal(normalizeShowTipsEnabled({ chat: { show_tips: false } }), false)
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

test('plan context guard normalization defaults and clamps persisted wire values', () => {
  assert.deepEqual(normalizePlanContextGuardSettings(undefined), {
    enabled: true,
    usedPercent: 80,
    maxCompactions: 1,
  })
  assert.deepEqual(normalizePlanContextGuardSettings({
    chat: {
      plan_context_guard_enabled: false,
      plan_context_guard_used_percent: 99,
      plan_context_guard_max_compactions: -4,
    },
  }), {
    enabled: false,
    usedPercent: 95,
    maxCompactions: 0,
  })
  assert.equal(normalizePlanContextGuardUsedPercent(49.6), 50)
  assert.equal(normalizePlanContextGuardUsedPercent(Number.NaN), 80)
  assert.equal(normalizePlanContextGuardMaxCompactions(9), 3)
})

test('plan context guard patch preserves unrelated settings and writes normalized fields', () => {
  const current: UISettingsWire = {
    theme: { active_id: 'tide' },
    chat: { thinking_tags: false, default_new_session_mode: 'plan' },
    swarm: { name: 'Local' },
  }
  assert.deepEqual(withPlanContextGuardSettings(current, {
    enabled: false,
    usedPercent: 97,
    maxCompactions: 8,
  }), {
    ...current,
    chat: {
      thinking_tags: false,
      default_new_session_mode: 'plan',
      plan_context_guard_enabled: false,
      plan_context_guard_used_percent: 95,
      plan_context_guard_max_compactions: 3,
    },
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

test('UI settings contract remains presentation, device, and tool scoped', () => {
  const settings: UISettingsWire = {
    theme: { active_id: 'tide' },
    chat: { thinking_tags: true },
    swarm: { name: 'Local' },
    tools: { image: { default_model: 'image-model' } },
  }
  assert.deepEqual(Object.keys(settings).sort(), ['chat', 'swarm', 'theme', 'tools'])
})

test('follow-up checkpoint policy default preserves ask-first aliases', () => {
  assert.equal(normalizeFollowupCheckpointPolicyDefault('require_approval'), 'require_approval')
  assert.equal(normalizeFollowupCheckpointPolicyDefault('ask'), 'require_approval')
  assert.equal(normalizeFollowupCheckpointPolicyDefault('manual'), 'require_approval')
  assert.equal(normalizeSwarmSettings({ chat: { followup_checkpoint_policy_default: 'require_approval' } }).followupCheckpointPolicyDefault, 'require_approval')
})
