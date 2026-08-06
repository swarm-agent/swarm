import assert from 'node:assert/strict'
import test from 'node:test'
import {
  activeModelProfileFromMetadata,
  activeModelProfileFromPolicy,
  modelOptionForSelection,
  modelProfileChoiceForTemporary,
  modelProfileFromMetadata,
  modelProfileInputFromDraft,
  preferenceFromModelProfile,
  preferenceFromModelProfileMetadata,
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

test('flat favorite helpers preserve context mode and normalize input', () => {
  const full = option('full')
  const selection = selectionFromModelOption(full, { serviceTier: 'standard' })
  assert.equal(selection.contextMode, 'full')
  assert.equal(selection.serviceTier, '')
  assert.equal(modelOptionForSelection(selection, [option(''), full]), full)
  assert.deepEqual(modelProfileInputFromDraft({ name: ' Favorite ', ...selection }), { name: 'Favorite', ...selection })
})

test('a flat favorite resolves identically in Action and Plan modes', () => {
  const profile: ModelProfileInput = {
    name: 'Fast', provider: 'openai', model: 'action', thinking: 'high', serviceTier: 'fast', contextMode: 'full',
  }
  assert.equal(preferenceFromModelProfile(profile, 'plan').model, 'action')
  assert.equal(preferenceFromModelProfile(profile, 'auto').serviceTier, 'fast')
  assert.deepEqual(modelProfileChoiceForTemporary(profile), { kind: 'temporary', profile })
})

test('durable session metadata reads canonical Action and optional Plan snapshots', () => {
  const metadata = {
    model_profile: {
      source: 'saved', action_favorite_id: 'mp_action', action_favorite_name: 'Action',
      action: { provider: 'openai', model: 'profile-action', thinking: 'high', service_tier: 'fast', context_mode: '' },
      plan_favorite_id: 'mp_plan', plan_favorite_name: 'Plan',
      plan: { provider: 'anthropic', model: 'profile-plan', thinking: 'medium', service_tier: '', context_mode: 'full' },
      applied_at: 123,
    },
  }
  assert.deepEqual(modelProfileFromMetadata(metadata, 'auto'), {
    name: 'Action', provider: 'openai', model: 'profile-action', thinking: 'high', serviceTier: 'fast', contextMode: '',
  })
  assert.deepEqual(preferenceFromModelProfileMetadata(metadata, 'plan'), {
    provider: 'anthropic', model: 'profile-plan', thinking: 'medium', serviceTier: '', contextMode: 'full', updatedAt: 123,
  })
  assert.deepEqual(activeModelProfileFromMetadata(metadata), { source: 'saved', profileId: 'mp_action', name: 'Action' })
})

test('invalid session snapshot does not masquerade as an authoritative preference', () => {
  assert.equal(preferenceFromModelProfileMetadata({ model_profile: { source: 'saved', action: { provider: 'openai' } } }, 'auto'), null)
  assert.deepEqual(activeModelProfileFromMetadata({}), { source: '', profileId: '', name: '' })
})

test('active profile state maps hydrated policy identity without bundle mode fields', () => {
  assert.deepEqual(activeModelProfileFromPolicy({ profile_source: 'saved', profile_id: 'mp_1', profile_name: 'Recommended' }), {
    source: 'saved', profileId: 'mp_1', name: 'Recommended',
  })
})
