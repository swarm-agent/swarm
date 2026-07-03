import assert from 'node:assert/strict'
import test from 'node:test'

import { displayModelName, modelServiceTierOptions, supportsModelServiceTier } from './model-options'

test('displayModelName strips Fireworks account model prefix', () => {
  assert.equal(displayModelName('fireworks', 'accounts/fireworks/models/kimi-k2p6', ''), 'kimi-k2p6')
})

test('displayModelName preserves hyphens after Fireworks prefix stripping', () => {
  assert.notEqual(displayModelName('fireworks', 'accounts/fireworks/models/kimi-k2p6', ''), 'kimik2p6')
})

test('displayModelName leaves non-Fireworks model ids unchanged', () => {
  assert.equal(displayModelName('codex', 'gpt-5.4', ''), 'gpt-5.4')
})

test('Fireworks service tier options preserve standard, priority, and fast from catalog tiers', () => {
  assert.deepEqual(modelServiceTierOptions('fireworks', 'accounts/fireworks/models/kimi-k2p6', ['priority', 'fast']), [
    { label: 'Off / standard', value: '' },
    { label: 'Priority', value: 'priority' },
    { label: 'Fast', value: 'fast' },
  ])
  assert.equal(supportsModelServiceTier('fireworks', 'accounts/fireworks/models/kimi-k2p6', ['priority', 'fast'], 'priority'), true)
  assert.equal(supportsModelServiceTier('fireworks', 'accounts/fireworks/models/kimi-k2p6', ['priority'], 'fast'), false)
})

test('Codex service tier options come from catalog tiers for fast and flex', () => {
  assert.deepEqual(modelServiceTierOptions('codex', 'gpt-5.5', ['fast', 'flex']), [
    { label: 'Off / standard', value: '' },
    { label: 'Fast', value: 'fast' },
    { label: 'Flex', value: 'flex' },
  ])
  assert.equal(supportsModelServiceTier('codex', 'gpt-5.5', ['fast'], 'fast'), true)
  assert.equal(supportsModelServiceTier('codex', 'gpt-5.5', ['flex'], 'flex'), true)
  assert.equal(supportsModelServiceTier('codex', 'gpt-5.5', [], 'fast'), false)
})
