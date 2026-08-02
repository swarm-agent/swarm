import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'
import { profileTriggerDisplay } from './profile-agent-picker'
import type { ActiveModelProfileState, ModelProfileRecord } from '../types/chat'

const source = readFileSync(new URL('./profile-agent-picker.tsx', import.meta.url), 'utf8')
const composerSource = readFileSync(new URL('./desktop-v3-agentic-composer.tsx', import.meta.url), 'utf8')
const agentSetupSource = readFileSync(new URL('./agent-model-control.tsx', import.meta.url), 'utf8')
const newSessionSource = readFileSync(new URL('./desktop-v3-new-session-pane.tsx', import.meta.url), 'utf8')
const existingConversationSource = readFileSync(new URL('./desktop-v3-existing-conversation-pane.tsx', import.meta.url), 'utf8')
const modelsSettingsSource = readFileSync(new URL('../../settings/models/components/models-settings-page.tsx', import.meta.url), 'utf8')

function favorite(overrides: Partial<ModelProfileRecord> = {}): ModelProfileRecord {
  return {
    profileId: 'favorite-1', name: 'Fast coding', provider: 'openai', model: 'gpt-5.6-codex', thinking: 'high', serviceTier: 'fast', contextMode: '', createdAt: 1, updatedAt: 2, sortOrder: 0, isDefault: true, ...overrides,
  }
}

function activeSaved(overrides: Partial<ActiveModelProfileState> = {}): ActiveModelProfileState {
  return { source: 'saved', profileId: 'favorite-1', name: 'Fast coding', ...overrides }
}

test('Profiles trigger shows the active favorite and resolved model', () => {
  const display = profileTriggerDisplay({ activeProfile: activeSaved(), profiles: [favorite()], modelDetail: 'GPT-5.6 Codex · thinking high · tier fast' })
  assert.equal(display.profileLabel, 'Fast coding')
  assert.equal(display.modelLabel, 'GPT-5.6 Codex · thinking high · tier fast')
})

test('composer Profiles flow stages favorites until Save', () => {
  assert.match(source, />Profiles</)
  assert.match(source, /aria-label="Action favorites"/)
  assert.match(source, /setStagedProfileId\(profile\.profileId\)/)
  assert.match(source, /await onProfileSelect\(stagedProfileId\)/)
  assert.match(source, />Save</)
  assert.doesNotMatch(source, /onClick=\{\(\) => \{ void onProfileSelect/)
})

test('Profiles flow supports inline model changes and compact add favorite assignment', () => {
  assert.match(source, />Provider<select/)
  assert.match(source, />Model<select/)
  assert.match(source, />Thinking<select/)
  assert.match(source, />Service tier<select/)
  assert.match(source, /Add favorite/)
  assert.match(source, /const profileId = await onFavoriteCreate/)
  assert.match(source, /await onProfileSelect\(profileId\)/)
  assert.match(source, /setOpen\(false\)/)
  assert.match(source, /setLocalError\(cause instanceof Error/)
})

test('new and existing composers preserve Plan settings while assigning only Action', () => {
  for (const paneSource of [newSessionSource, existingConversationSource]) {
    assert.match(paneSource, /actionFavoriteId: normalized/)
    assert.match(paneSource, /planEnabled: current\.planEnabled/)
    assert.match(paneSource, /current\.planFavoriteId \? \{ planFavoriteId: current\.planFavoriteId \}/)
    assert.match(paneSource, /onModelFavoriteCreate=\{createActionFavorite\}/)
  }
  assert.equal((composerSource.match(/showFavoriteSelector \?/g) ?? []).length, 2)
})

test('Agent Setup has no duplicate Saved Profiles CRUD and Models settings remains canonical CRUD', () => {
  assert.doesNotMatch(agentSetupSource, /Saved model profiles|Saved profiles|makeModelProfileDefault|removeModelProfile|persistModelProfileOrder/)
  assert.match(modelsSettingsSource, /onCreate=/)
  assert.match(modelsSettingsSource, /onUpdate=/)
  assert.match(modelsSettingsSource, /onDelete=/)
  assert.match(modelsSettingsSource, /onReorder=/)
  assert.match(modelsSettingsSource, /onSetDefault=/)
})
