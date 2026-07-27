import assert from 'node:assert/strict'
import test from 'node:test'
import { resolveDesktopV3StartupAgent } from '../services/desktop-startup-agent'
import { resolveDesktopWorktreeSessionDefaults } from '../services/desktop-worktree-session-defaults'
import type { AgentProfileRecord, AgentStateRecord, ModelProfileState, ResolvedSessionPreference } from '../types/chat'

function profile(name: string, mode = 'primary'): AgentProfileRecord {
  return {
    name,
    mode,
    description: '',
    provider: 'codex',
    model: 'gpt-5.4',
    thinking: 'high',
    modelMode: 'single',
    planProvider: '', planModel: '', planThinking: '', planServiceTier: '',
    autoProvider: '', autoModel: '', autoThinking: '', autoServiceTier: '',
    prompt: '', runtimeMode: 'plan_auto', defaultSessionMode: 'auto', executionSetting: '',
    exitPlanModeEnabled: true, toolScope: null, toolContract: null,
    enabled: true, protected: false, updatedAt: 0,
  }
}

function state(profiles: AgentProfileRecord[], activePrimary: string): AgentStateRecord {
  return { profiles, activePrimary, activeSubagent: {}, version: 1, providerDefaultsPreview: null, toolInventory: null }
}

const draftPreference: ResolvedSessionPreference = {
  preference: {
    provider: 'draft-provider', model: 'draft-model', thinking: 'medium', serviceTier: '', contextMode: '', updatedAt: 1,
  },
  contextWindow: 0,
  maxOutputTokens: 0,
}

const noModelProfiles: ModelProfileState = { profiles: [], defaultProfileId: '' }

test('new Desktop sessions select a valid active primary before built-in Swarm', () => {
  assert.equal(resolveDesktopV3StartupAgent(state([profile('legacy'), profile('swarm')], 'legacy')), 'legacy')
})

test('new Desktop sessions fall back to built-in Swarm when active primary is invalid', () => {
  assert.equal(resolveDesktopV3StartupAgent(state([profile('swarm')], 'missing')), 'swarm')
})

test('new Desktop sessions keep compiled Swarm selectable when stale stored state marks it disabled', () => {
  assert.equal(resolveDesktopV3StartupAgent(state([{ ...profile('swarm'), enabled: false, protected: true }], 'swarm')), 'swarm')
})

test('an explicit requested agent remains authoritative', () => {
  assert.equal(resolveDesktopV3StartupAgent(state([profile('swarm')], 'swarm'), 'other'), 'other')
})

test('worktree defaults validate a stale active primary and use the resolved profile mode', () => {
  const resolved = resolveDesktopWorktreeSessionDefaults({
    agentState: state([{ ...profile('swarm'), defaultSessionMode: 'plan' }], 'missing'),
    modelProfiles: noModelProfiles,
    modelOptions: [],
    draftPreference,
    globalDefaultMode: 'auto',
  })

  assert.equal(resolved.agentName, 'swarm')
  assert.equal(resolved.mode, 'plan')
  assert.equal(resolved.preference.model, 'draft-model')
  assert.equal(resolved.modelProfileChoice, undefined)
})

test('worktree defaults use the Swarm account-default profile selection for the resolved mode', () => {
  const resolved = resolveDesktopWorktreeSessionDefaults({
    agentState: state([{ ...profile('swarm'), defaultSessionMode: 'plan' }], 'swarm'),
    modelProfiles: {
      defaultProfileId: 'account-profile',
      profiles: [{
        profileId: 'account-profile', name: 'Account', modelMode: 'split', single: null,
        plan: { provider: 'codex', model: 'plan-model', thinking: 'high', serviceTier: 'priority', contextMode: '' },
        auto: { provider: 'codex', model: 'auto-model', thinking: 'medium', serviceTier: '', contextMode: '' },
        createdAt: 1, updatedAt: 2, isDefault: true,
      }],
    },
    modelOptions: [],
    draftPreference,
    globalDefaultMode: 'auto',
  })

  assert.equal(resolved.mode, 'plan')
  assert.equal(resolved.preference.model, 'plan-model')
  assert.deepEqual(resolved.modelProfileChoice, { kind: 'account-default' })
})

test('worktree defaults preserve a configured non-Swarm split model lock', () => {
  const resolved = resolveDesktopWorktreeSessionDefaults({
    agentState: state([{
      ...profile('custom'), defaultSessionMode: 'auto', modelMode: 'split',
      planProvider: 'codex', planModel: 'custom-plan', planThinking: 'high', planServiceTier: '',
      autoProvider: 'codex', autoModel: 'custom-auto', autoThinking: 'medium', autoServiceTier: 'priority',
    }, profile('swarm')], 'custom'),
    modelProfiles: {
      defaultProfileId: 'swarm-default',
      profiles: [{
        profileId: 'swarm-default', name: 'Swarm default', modelMode: 'single',
        single: { provider: 'other', model: 'wrong-model', thinking: 'low', serviceTier: '', contextMode: '' },
        plan: null, auto: null, createdAt: 1, updatedAt: 2, isDefault: true,
      }],
    },
    modelOptions: [],
    draftPreference,
    globalDefaultMode: 'plan',
  })

  assert.equal(resolved.agentName, 'custom')
  assert.equal(resolved.mode, 'auto')
  assert.equal(resolved.preference.model, 'custom-auto')
  assert.deepEqual(resolved.modelProfileChoice, { kind: 'agent-default' })
})

test('an explicit workspace mode takes precedence for worktree profile resolution', () => {
  const resolved = resolveDesktopWorktreeSessionDefaults({
    agentState: state([{ ...profile('swarm'), defaultSessionMode: 'auto' }], 'swarm'),
    modelProfiles: {
      defaultProfileId: 'account-profile',
      profiles: [{
        profileId: 'account-profile', name: 'Account', modelMode: 'split', single: null,
        plan: { provider: 'codex', model: 'explicit-plan', thinking: 'high', serviceTier: '', contextMode: '' },
        auto: { provider: 'codex', model: 'default-auto', thinking: 'medium', serviceTier: '', contextMode: '' },
        createdAt: 1, updatedAt: 2, isDefault: true,
      }],
    },
    modelOptions: [],
    draftPreference,
    explicitMode: 'plan',
    globalDefaultMode: 'auto',
  })

  assert.equal(resolved.mode, 'plan')
  assert.equal(resolved.preference.model, 'explicit-plan')
})
