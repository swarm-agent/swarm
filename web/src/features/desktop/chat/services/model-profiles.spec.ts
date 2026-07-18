import assert from 'node:assert/strict'
import test from 'node:test'
import {
  activeModelProfileFromMetadata,
  activeModelProfileFromPolicy,
  modelOptionForSelection,
  modelProfileChoiceForTemporary,
  preferenceFromModelProfile,
  selectionFromModelOption,
} from './model-profiles'
import type { ModelOptionRecord, ModelProfileInput } from '../types/chat'

function option(contextMode: string): ModelOptionRecord {
  return {
    key: `openai:gpt-test:${contextMode}`,
    provider: 'openai', model: 'gpt-test', contextMode, label: 'GPT test', thinking: 'high',
    thinkingOptions: ['off', 'high'], defaultThinking: 'high', thinkingProviderParameter: '', thinkingMappings: [],
    favorite: true, contextWindow: contextMode === 'full' ? 200000 : 100000, pricing: null,
    serviceTiers: ['fast'], defaultServiceTier: '', serviceTierMappings: [], contextModes: [],
  }
}

test('model profile helpers preserve context mode and standard tier semantics', () => {
  const full = option('full')
  const selection = selectionFromModelOption(full, { serviceTier: 'standard' })
  assert.equal(selection.contextMode, 'full')
  assert.equal(selection.serviceTier, '')
  assert.equal(modelOptionForSelection(selection, [option(''), full]), full)
})

test('split model profile resolves the branch for the current mode', () => {
  const profile: ModelProfileInput = {
    name: 'Split', modelMode: 'split', single: null,
    plan: { provider: 'openai', model: 'plan', thinking: 'high', serviceTier: '', contextMode: 'full' },
    auto: { provider: 'openai', model: 'action', thinking: 'off', serviceTier: 'fast', contextMode: '' },
  }
  assert.equal(preferenceFromModelProfile(profile, 'plan')?.model, 'plan')
  assert.equal(preferenceFromModelProfile(profile, 'auto')?.serviceTier, 'fast')
  assert.deepEqual(modelProfileChoiceForTemporary({ ...profile, single: { provider: '', model: '', thinking: '', serviceTier: '', contextMode: '' } }), { kind: 'temporary', profile })
})

test('active profile state maps the durable session metadata snapshot', () => {
  assert.deepEqual(activeModelProfileFromMetadata({ model_profile: { source: 'saved', saved_profile_id: 'mp_1', name: 'Recommended', model_mode: 'split' } }), {
    source: 'saved', profileId: 'mp_1', name: 'Recommended', modelMode: 'split',
  })
  assert.deepEqual(activeModelProfileFromMetadata({}), {
    source: '', profileId: '', name: '', modelMode: '',
  })
})

test('active profile state maps hydrated snake-case policy fields', () => {
  assert.deepEqual(activeModelProfileFromPolicy({ profile_source: 'saved', profile_id: 'mp_1', profile_name: 'Recommended', profile_mode: 'split' }), {
    source: 'saved', profileId: 'mp_1', name: 'Recommended', modelMode: 'split',
  })
})
