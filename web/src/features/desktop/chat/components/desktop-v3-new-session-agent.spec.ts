import assert from 'node:assert/strict'
import test from 'node:test'
import { resolveDesktopV3StartupAgent } from '../services/desktop-startup-agent'
import type { AgentProfileRecord, AgentStateRecord } from '../types/chat'

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

test('new Desktop sessions select canonical Swarm over a stale active primary', () => {
  assert.equal(resolveDesktopV3StartupAgent(state([profile('legacy'), profile('swarm')], 'legacy')), 'swarm')
})

test('an explicit requested agent remains authoritative', () => {
  assert.equal(resolveDesktopV3StartupAgent(state([profile('swarm')], 'swarm'), 'other'), 'other')
})
