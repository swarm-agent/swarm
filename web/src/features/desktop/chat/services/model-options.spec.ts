import assert from 'node:assert/strict'
import test from 'node:test'

import { defaultModelThinking, displayModelName, modelServiceTierOptions, modelThinkingOptions, normalizeModelID, normalizeModelServiceTier, supportsModelServiceTier } from './model-options'

test('displayModelName strips Fireworks account model prefix', () => {
  assert.equal(displayModelName('fireworks', 'accounts/fireworks/models/kimi-k2p6', ''), 'kimi-k2p6')
})

test('displayModelName preserves hyphens after Fireworks prefix stripping', () => {
  assert.notEqual(displayModelName('fireworks', 'accounts/fireworks/models/kimi-k2p6', ''), 'kimik2p6')
})

test('normalizeModelID matches Fireworks short and provider-qualified model ids', () => {
  assert.equal(normalizeModelID('fireworks', 'accounts/fireworks/models/deepseek-v4-flash'), 'deepseek-v4-flash')
  assert.equal(normalizeModelID('fireworks', 'accounts/fireworks/routers/deepseek-v4-flash'), 'deepseek-v4-flash')
  assert.equal(normalizeModelID('fireworks', 'fireworks/deepseek-v4-flash'), 'deepseek-v4-flash')
  assert.equal(normalizeModelID('codex', 'accounts/fireworks/models/deepseek-v4-flash'), 'accounts/fireworks/models/deepseek-v4-flash')
})

test('displayModelName leaves non-Fireworks model ids unchanged', () => {
  assert.equal(displayModelName('codex', 'gpt-5.4', ''), 'gpt-5.4')
})

test('displayModelName keeps Fireworks fast model suffix', () => {
  assert.equal(displayModelName('fireworks', 'kimi-k2p6-fast', ''), 'kimi-k2p6-fast')
  assert.equal(displayModelName('fireworks', 'accounts/fireworks/routers/kimi-k2p6-fast', ''), 'kimi-k2p6-fast')
})

test('Fireworks service tier options come directly from catalog tiers', () => {
  assert.deepEqual(modelServiceTierOptions('fireworks', 'accounts/fireworks/models/kimi-k2p6', ['standard', 'priority']), [
    { label: 'Off / standard', value: '' },
    { label: 'Priority', value: 'priority' },
  ])
  assert.deepEqual(modelServiceTierOptions('fireworks', 'kimi-k2p6-fast', ['standard']), [
    { label: 'Off / standard', value: '' },
  ])
  assert.equal(supportsModelServiceTier('fireworks', 'accounts/fireworks/models/kimi-k2p6', ['standard', 'priority'], 'priority'), true)
  assert.equal(supportsModelServiceTier('fireworks', 'kimi-k2p6-fast', ['standard'], 'priority'), false)
  assert.equal(normalizeModelServiceTier('fireworks', 'fast'), 'fast')
})

test('Codex service tier options come from catalog tiers and keep priority distinct from fast', () => {
  assert.deepEqual(modelServiceTierOptions('codex', 'gpt-5.5', ['priority', 'fast', 'flex']), [
    { label: 'Off / standard', value: '' },
    { label: 'Priority', value: 'priority' },
    { label: 'Fast', value: 'fast' },
    { label: 'Flex', value: 'flex' },
  ])
  assert.equal(normalizeModelServiceTier('codex', 'priority'), 'priority')
  assert.equal(normalizeModelServiceTier('codex', 'fast'), 'fast')
  assert.equal(supportsModelServiceTier('codex', 'gpt-5.5', ['priority'], 'priority'), true)
  assert.equal(supportsModelServiceTier('codex', 'gpt-5.5', ['priority'], 'fast'), false)
  assert.equal(supportsModelServiceTier('codex', 'gpt-5.5', ['fast'], 'fast'), true)
  assert.equal(supportsModelServiceTier('codex', 'gpt-5.5', ['flex'], 'flex'), true)
  assert.equal(supportsModelServiceTier('codex', 'gpt-5.5', [], 'fast'), false)
})

test('GLM 5.2 thinking options come directly from catalog metadata', () => {
  const glm52 = {
    thinkingOptions: ['off', 'high', 'xhigh'],
    defaultThinking: 'xhigh',
    thinking: '',
  }
  assert.deepEqual(modelThinkingOptions(glm52), ['off', 'high', 'xhigh'])
  assert.equal(defaultModelThinking(glm52), 'xhigh')
  assert.equal(modelThinkingOptions(glm52).includes('low'), false)
  assert.equal(modelThinkingOptions(glm52).includes('medium'), false)
})


test('Anthropic service tier options expose priority but hide asynchronous batch', () => {
  assert.deepEqual(modelServiceTierOptions('anthropic', 'claude-sonnet-5', ['standard', 'priority', 'batch']), [
    { label: 'Off / standard', value: '' },
    { label: 'Priority', value: 'priority' },
  ])
  assert.equal(normalizeModelServiceTier('anthropic', 'priority'), 'priority')
  assert.equal(normalizeModelServiceTier('anthropic', 'batch'), '')
  assert.equal(supportsModelServiceTier('anthropic', 'claude-sonnet-5', ['standard', 'priority', 'batch'], 'priority'), true)
  assert.equal(supportsModelServiceTier('anthropic', 'claude-sonnet-5', ['standard', 'priority', 'batch'], 'batch'), false)
})
