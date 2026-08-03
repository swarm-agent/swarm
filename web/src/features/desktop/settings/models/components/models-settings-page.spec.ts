import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'
import { toFlatModelFavorite, toFlatModelOptions, toModelProfileInput } from './models-settings-page'
import type { ModelOptionRecord, ModelProfileRecord } from '../../../chat/types/chat'

const source = readFileSync(new URL('./models-settings-page.tsx', import.meta.url), 'utf8')
const settingsPageSource = readFileSync(new URL('../../components/desktop-settings-page.tsx', import.meta.url), 'utf8')

const profile: ModelProfileRecord = {
  profileId: 'mp_daily', name: 'Daily', provider: 'codex', model: 'gpt-5.5', thinking: 'high',
  serviceTier: 'fast', contextMode: 'large', createdAt: 1, updatedAt: 2, sortOrder: 0, isDefault: true,
}

function option(contextMode: string): ModelOptionRecord {
  return {
    key: `codex:gpt-5.5:${contextMode}`, provider: 'codex', model: 'gpt-5.5', contextMode,
    label: 'GPT 5.5', thinking: 'high', thinkingOptions: ['high', 'xhigh'], defaultThinking: 'high',
    thinkingProviderParameter: '', thinkingMappings: [], favorite: true, contextWindow: 100_000,
    pricing: null, serviceTiers: ['fast'], defaultServiceTier: '', serviceTierMappings: [],
    contextModes: [{ mode: 'large' }, { mode: 'compact' }], media: null,
  }
}

test('composition maps canonical model profile fields without another persistent type', () => {
  assert.deepEqual(toFlatModelFavorite(profile), {
    id: 'mp_daily', name: 'Daily', provider: 'codex', model: 'gpt-5.5', thinking: 'high',
    serviceTier: 'fast', contextMode: 'large', isDefault: true,
  })
  assert.deepEqual(toModelProfileInput({
    name: 'Daily', provider: 'codex', model: 'gpt-5.5', thinking: 'high', serviceTier: 'fast', contextMode: 'large',
  }), {
    name: 'Daily', provider: 'codex', model: 'gpt-5.5', thinking: 'high', serviceTier: 'fast', contextMode: 'large',
  })
})

test('model options collapse context variants into one flat favorite editor choice', () => {
  assert.deepEqual(toFlatModelOptions([option(''), option('large')]), [{
    provider: 'codex', model: 'gpt-5.5', label: 'GPT 5.5', thinkingOptions: ['high', 'xhigh'],
    serviceTierOptions: ['fast'], contextModeOptions: ['large', 'compact'],
  }])
})

test('Models page uses canonical queries, mutations, invalidation, and explicit errors', () => {
  assert.match(source, /configure Swarm’s Action and Plan models directly/)
  assert.match(source, /modelOptionsQueryOptions\(\)/)
  assert.match(source, /modelProfilesQueryOptions\(\)/)
  assert.match(source, /createModelProfile/)
  assert.match(source, /updateModelProfile/)
  assert.match(source, /deleteModelProfile/)
  assert.match(source, /reorderModelProfiles/)
  assert.match(source, /setDefaultModelProfile/)
  assert.match(source, /invalidateModelProfiles\(queryClient\)/)
  assert.match(source, /agentModelSettingsQueryOptions/)
  assert.match(source, /saveSwarmAgentModelSettings/)
  assert.match(source, /action=\{settings\.swarm\.action\}/)
  assert.match(source, /plan=\{settings\.swarm\.plan\}/)
  assert.doesNotMatch(source, /actionFavoriteId|planFavoriteId|planEnabled/)
  assert.match(source, /role="alert"/)
})

test('Desktop settings exposes exactly one Models tab and one composed page', () => {
  assert.equal((settingsPageSource.match(/id: 'models'/g) ?? []).length, 1)
  assert.equal((settingsPageSource.match(/<ModelsSettingsPage \/>/g) ?? []).length, 1)
  assert.doesNotMatch(settingsPageSource, /ModelFavoritesSettings|SwarmModelAssignmentSettings/)
})
