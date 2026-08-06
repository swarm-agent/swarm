import assert from 'node:assert/strict'
import test from 'node:test'

import { defaultModelThinking, displayModelName, modelAllowedByProviderPreset, modelOptionGroupKey, modelOptionRouteLabel, modelServiceTierOptions, modelThinkingOptions, modelUpstreamFamily, normalizeModelID, normalizeModelServiceTier, normalizeProviderID, supportsModelServiceTier } from './model-options'

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

test('Google service tier options preserve catalog-backed priority selection', () => {
  assert.deepEqual(modelServiceTierOptions('google', 'gemini-3.1-pro-preview', {
    serviceTiers: ['standard', 'priority', 'fast', 'batch', 'flex'],
    serviceTierMappings: [
      { tier: 'standard', swarm_setting: 'off', provider_parameter: 'service_tier', provider_value: '' },
      { tier: 'priority', swarm_setting: 'fast', provider_parameter: 'service_tier', provider_value: 'priority' },
    ],
  }), [
    { label: 'Off / standard', value: '' },
    { label: 'Priority', value: 'priority' },
    { label: 'Fast', value: 'fast' },
  ])
  assert.equal(normalizeModelServiceTier('google', 'priority'), 'priority')
  assert.equal(normalizeModelServiceTier('google', 'fast'), 'fast')
  assert.equal(normalizeModelServiceTier('google', 'batch'), '')
  assert.equal(normalizeModelServiceTier('google', 'flex'), '')
  assert.equal(supportsModelServiceTier('google', 'gemini-3.1-pro-preview', ['standard', 'priority'], 'priority'), true)
  assert.equal(supportsModelServiceTier('google', 'gemini-3.1-pro-preview', ['standard', 'fast'], 'fast'), true)
  assert.equal(supportsModelServiceTier('google', 'gemini-3.1-pro-preview', ['standard', 'priority', 'fast', 'batch', 'flex'], 'batch'), false)
  assert.equal(supportsModelServiceTier('google', 'gemini-3.1-pro-preview', ['standard', 'priority', 'fast', 'batch', 'flex'], 'flex'), false)
})

test('OpenAI API provider stays distinct from Codex and exposes catalog models', () => {
  assert.equal(normalizeProviderID('openai'), 'openai')
  assert.equal(normalizeModelID('openai', 'gpt-5.5'), 'gpt-5.5')
  assert.equal(modelAllowedByProviderPreset('openai', 'babbage-002'), true)
  assert.deepEqual(modelServiceTierOptions('openai', 'gpt-5.5', ['standard', 'priority', 'flex', 'batch']), [
    { label: 'Off / standard', value: '' },
    { label: 'Priority', value: 'priority' },
    { label: 'Flex', value: 'flex' },
  ])
  assert.equal(normalizeModelServiceTier('openai', 'priority'), 'priority')
  assert.equal(normalizeModelServiceTier('openai', 'flex'), 'flex')
  assert.equal(normalizeModelServiceTier('openai', 'batch'), '')
  assert.equal(supportsModelServiceTier('openai', 'gpt-5.5', ['standard', 'priority', 'flex', 'batch'], 'priority'), true)
  assert.equal(supportsModelServiceTier('openai', 'gpt-5.5', ['standard', 'priority', 'flex', 'batch'], 'batch'), false)
})

test('OpenRouter upstream families remain routed and distinct from direct providers', () => {
  const routedGoogle = { provider: 'openrouter', model: 'google/gemini-3.1-pro', upstreamFamily: 'google' }
  const directGoogle = { provider: 'google', model: 'gemini-3.1-pro', upstreamFamily: '' }
  assert.equal(modelUpstreamFamily(routedGoogle.provider, routedGoogle.model), 'google')
  assert.equal(modelUpstreamFamily(directGoogle.provider, directGoogle.model), '')
  assert.equal(modelOptionRouteLabel(routedGoogle), 'OpenRouter → Google')
  assert.equal(modelOptionRouteLabel(directGoogle), 'google')
  assert.equal(modelOptionGroupKey(routedGoogle), 'openrouter::upstream::google')
  assert.equal(modelOptionGroupKey(directGoogle), 'google::direct')
  assert.notEqual(modelOptionGroupKey(routedGoogle), modelOptionGroupKey(directGoogle))
})

test('Codex catalog models are not filtered by the local sorting presets', () => {
  assert.equal(modelAllowedByProviderPreset('codex', 'gpt-5.6-luna'), true)
  assert.equal(modelAllowedByProviderPreset('codex', 'gpt-5.6-sol'), true)
  assert.equal(modelAllowedByProviderPreset('codex', 'gpt-5.6-terra'), true)
})

test('thinking options preserve snapshot max and ultra levels', () => {
  assert.deepEqual(modelThinkingOptions({ thinkingOptions: ['off', 'high', 'max', 'ultra'] }), ['off', 'high', 'max', 'ultra'])
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


test('Anthropic service tier options expose only explicit snapshot tiers', () => {
  const priorityOnly = {
    serviceTiers: ['standard', 'priority', 'batch'],
    serviceTierMappings: [
      { tier: 'standard', swarm_setting: 'off', provider_parameter: 'service_tier', provider_value: 'standard_only' },
      { tier: 'priority', swarm_setting: 'fast', provider_parameter: 'service_tier', provider_value: 'auto' },
    ],
  }
  assert.deepEqual(modelServiceTierOptions('anthropic', 'claude-sonnet-4-6', priorityOnly), [
    { label: 'Off / standard', value: '' },
    { label: 'Priority', value: 'priority' },
  ])
  assert.equal(supportsModelServiceTier('anthropic', 'claude-sonnet-4-6', priorityOnly, 'fast'), false)

  const opus48 = {
    ...priorityOnly,
    serviceTierMappings: [
      ...priorityOnly.serviceTierMappings,
      { tier: 'fast', swarm_setting: '', provider_parameter: 'speed', provider_value: 'fast' },
    ],
  }
  assert.deepEqual(modelServiceTierOptions('anthropic', 'claude-opus-4-8', opus48), [
    { label: 'Off / standard', value: '' },
    { label: 'Priority', value: 'priority' },
    { label: 'Fast', value: 'fast' },
  ])
  assert.equal(supportsModelServiceTier('anthropic', 'claude-opus-4-8', opus48, 'fast'), true)
  assert.equal(normalizeModelServiceTier('anthropic', 'priority'), 'priority')
  assert.equal(normalizeModelServiceTier('anthropic', 'batch'), '')
})
