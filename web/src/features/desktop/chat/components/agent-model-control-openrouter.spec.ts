import assert from 'node:assert/strict'
import test from 'node:test'

import { agentModelProviderChoices } from './agent-model-control'
import type { ModelOptionRecord } from '../types/chat'

function option(provider: string, model: string): ModelOptionRecord {
  return {
    key: `${provider}:${model}:`,
    provider,
    model,
    upstreamFamily: provider === 'openrouter' ? model.split('/')[0] : '',
    contextMode: '',
    label: `${provider}/${model}`,
    thinking: '',
    thinkingOptions: [],
    defaultThinking: '',
    thinkingProviderParameter: '',
    thinkingMappings: [],
    favorite: false,
    contextWindow: 0,
    pricing: null,
    serviceTiers: [],
    defaultServiceTier: '',
    serviceTierMappings: [],
    contextModes: [],
    media: null,
  }
}

test('agent setup exposes direct Google and OpenRouter Google as separate provider routes', () => {
  const choices = agentModelProviderChoices([
    option('google', 'gemini-3.1-pro'),
    option('openrouter', 'google/gemini-3.1-pro'),
    option('openrouter', 'anthropic/claude-opus-4.8'),
  ])

  assert.deepEqual(choices, [
    { key: 'google::direct', label: 'google', provider: 'google', upstreamFamily: '' },
    { key: 'openrouter::upstream::anthropic', label: 'OpenRouter → Anthropic', provider: 'openrouter', upstreamFamily: 'anthropic' },
    { key: 'openrouter::upstream::google', label: 'OpenRouter → Google', provider: 'openrouter', upstreamFamily: 'google' },
  ])
  assert.equal(choices.filter((choice) => choice.provider === 'google').length, 1)
  assert.equal(choices.filter((choice) => choice.label === 'OpenRouter → Google').every((choice) => choice.provider === 'openrouter'), true)
})
