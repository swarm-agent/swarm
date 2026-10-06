import assert from 'node:assert/strict'
import test from 'node:test'
import { applyFavoriteScope, favoriteDefaultPatch, favoriteDefaultSlot } from './favorite-scopes'
import type { AgentModelSettings } from '../../settings/swarm/types/agent-model-settings'

// Purpose: Favorites must target the selected chat rather than its surrounding
// Orchestrator and preserve the other canonical model slot. favoriteDefaultPatch
// owns the payload; a pure unit test is the narrowest proof of target selection.
test('favorite defaults use selected agent identity and preserve the other slot', () => {
  const assignment = { provider: 'codex', model: 'favorite', thinking: 'high', serviceTier: 'priority', contextMode: 'long' }
  const action = { ...assignment, model: 'action' }
  const plan = { ...assignment, model: 'plan' }
  const settings: AgentModelSettings = {
    swarm: { action, plan },
    systemAgents: { compact: action, finder: action, coder: action, designer: action, router: action },
    updatedAt: 1,
  }
  for (const alias of ['system-orchestrator', 'swarm-orchestrator', ' Orchestrator ']) {
    assert.equal(favoriteDefaultSlot(alias), 'plan')
    assert.deepEqual(favoriteDefaultPatch(settings, assignment, alias), { action, plan: assignment })
  }
  assert.equal(favoriteDefaultSlot('swarm', 'system-orchestrator'), 'action')
  assert.deepEqual(favoriteDefaultPatch(settings, assignment, 'swarm', 'system-orchestrator'), { action: assignment, plan })
  assert.equal(favoriteDefaultSlot('', 'system-orchestrator'), 'plan')
  assert.deepEqual(settings.swarm, { action, plan })
})

// Purpose: applyFavoriteScope must isolate chat-only/default-only effects,
// sequence combined effects, reject missing chat context before writes, and
// disclose partial saves instead of reporting success. This service unit test
// exercises the exact orchestration used by AgentModelControl, not source text.
test('favorite scopes isolate effects and report failed/partial writes', async () => {
  for (const scope of ['chat', 'default', 'default-and-chat'] as const) {
    const effects: string[] = []
    await applyFavoriteScope(scope, async () => { effects.push('default') }, async () => { effects.push('chat') })
    assert.deepEqual(effects, scope === 'chat' ? ['chat'] : scope === 'default' ? ['default'] : ['default', 'chat'])
  }
  const effects: string[] = []
  await assert.rejects(applyFavoriteScope('default-and-chat', async () => { effects.push('default'); throw new Error('rejected') }, async () => { effects.push('chat') }), /rejected/)
  assert.deepEqual(effects, ['default'])
  effects.length = 0
  await assert.rejects(applyFavoriteScope('default-and-chat', async () => { effects.push('default') }, async () => { throw new Error('busy') }), /Default saved, but this chat was not changed: busy/)
  assert.deepEqual(effects, ['default'])
  effects.length = 0
  await assert.rejects(applyFavoriteScope('default-and-chat', async () => { effects.push('default') }), /This chat is unavailable/)
  assert.deepEqual(effects, [])
  await assert.rejects(applyFavoriteScope('chat', async () => { effects.push('default') }, async () => { throw new Error('busy') }), /^Error: busy$/)
  assert.deepEqual(effects, [])
})
