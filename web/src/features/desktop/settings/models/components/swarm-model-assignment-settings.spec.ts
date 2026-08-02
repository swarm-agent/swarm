import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'
import { buildSwarmModelAssignmentSaveInput } from './swarm-model-assignment-settings'

const source = readFileSync(new URL('./swarm-model-assignment-settings.tsx', import.meta.url), 'utf8')
const action = { provider: 'codex', model: 'gpt-action', thinking: 'high', serviceTier: 'fast', contextMode: '' }
const plan = { provider: 'codex', model: 'gpt-plan', thinking: 'xhigh', serviceTier: '', contextMode: 'large' }

test('Action and Plan each require a direct model selection', () => {
  assert.deepEqual(buildSwarmModelAssignmentSaveInput({ action: { ...action, provider: '' }, plan }), {
    value: null, error: 'Choose provider, model, and thinking for Action.',
  })
  assert.deepEqual(buildSwarmModelAssignmentSaveInput({ action, plan: { ...plan, thinking: '' } }), {
    value: null, error: 'Choose provider, model, and thinking for Plan.',
  })
})

test('direct selections are normalized independently', () => {
  assert.deepEqual(buildSwarmModelAssignmentSaveInput({
    action: { ...action, provider: ' codex ', model: ' gpt-action ' },
    plan: { ...plan, contextMode: ' LARGE ' },
  }), {
    value: { action, plan },
    error: null,
  })
})

test('component exposes direct Action and Plan editors without favorite assignment controls', () => {
  assert.match(source, /onSave: \(input: SwarmModelAssignmentSaveInput\) => void/)
  assert.match(source, /label="Action model"/)
  assert.match(source, /label="Plan model"/)
  assert.match(source, /do not create or assign model favorites/)
  assert.doesNotMatch(source, /actionFavoriteId|planFavoriteId|planEnabled|Choose an Action favorite|Enable Plan assignment/)
  assert.doesNotMatch(source, /requestJson|useQuery|useMutation/)
})
