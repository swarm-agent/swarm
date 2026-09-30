// Purpose: workerModelOptionSelection/selectWorkerModelSlot own staged worker
// policy, workerCatalogPrice owns catalog-only wording. Prevent same-model
// reselect erasing saved options, cross-slot mutation and unknown-as-free pricing.
// Pure transformations are the narrowest policy layer; browser tests own focus.
import test from 'node:test'
import assert from 'node:assert/strict'
import { workerModelOptionSelection, selectWorkerModelSlot, workerCatalogPrice } from './worker-model-picker'
import type { ModelOptionRecord } from '../chat/types/chat'
test('same model preserves options, different model uses catalog defaults without changing plan', () => {
  const option = { provider: 'fixture', model: 'one', contextMode: '', defaultThinking: 'low', defaultServiceTier: 'standard', pricing: null } as ModelOptionRecord
  const current = { provider: 'fixture', model: 'one', thinking: 'high', service_tier: 'priority', context_mode: '' }
  assert.deepEqual(workerModelOptionSelection(option, current), current)
  const plan = { provider: 'fixture', model: 'planner' }
  const profile = { source: 'temporary', action: current, plan }
  const next = selectWorkerModelSlot(profile, 'action', workerModelOptionSelection({ ...option, model: 'two' }, current), current)
  assert.deepEqual(next.plan, plan)
  assert.equal(next.action.thinking, 'low'); assert.equal(profile.action.thinking, 'high')
  assert.match(workerCatalogPrice(option), /unknown/)
  assert.equal(workerCatalogPrice({ ...option, pricing: { is_free: true } }), 'Catalog: free')
  assert.match(workerCatalogPrice({ ...option, pricing: { input_price_per_million_tokens: 1, output_price_per_million_tokens: 2 } }), /Catalog estimate/)
})
