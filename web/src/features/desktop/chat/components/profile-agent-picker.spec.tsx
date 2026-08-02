import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'
import { profileTriggerDisplay } from './profile-agent-picker'
import type { ActiveModelProfileState, ModelProfileRecord } from '../types/chat'

const source = readFileSync(new URL('./profile-agent-picker.tsx', import.meta.url), 'utf8')
const composerSource = readFileSync(new URL('./desktop-v3-agentic-composer.tsx', import.meta.url), 'utf8')
const existingConversationSource = readFileSync(new URL('./desktop-v3-existing-conversation-pane.tsx', import.meta.url), 'utf8')

function favorite(overrides: Partial<ModelProfileRecord> = {}): ModelProfileRecord {
  return {
    profileId: 'favorite-1',
    name: 'Fast coding',
    provider: 'openai',
    model: 'gpt-5.6-codex',
    thinking: 'high',
    serviceTier: 'fast',
    contextMode: '',
    createdAt: 1,
    updatedAt: 2,
    sortOrder: 0,
    isDefault: true,
    ...overrides,
  }
}

function activeSaved(overrides: Partial<ActiveModelProfileState> = {}): ActiveModelProfileState {
  return { source: 'saved', profileId: 'favorite-1', name: 'Fast coding', ...overrides }
}

test('resolved session favorite trigger shows the active favorite and resolved model', () => {
  const display = profileTriggerDisplay({
    activeProfile: activeSaved(),
    profiles: [favorite()],
    modelDetail: 'GPT-5.6 Codex · thinking high · tier fast',
  })
  assert.equal(display.profileLabel, 'Fast coding')
  assert.equal(display.modelLabel, 'GPT-5.6 Codex · thinking high · tier fast')
  assert.equal(display.combinedLabel, 'Fast coding · GPT-5.6 Codex · thinking high · tier fast')
})

test('resolved session favorite trigger falls back to canonical flat favorite fields', () => {
  const display = profileTriggerDisplay({
    activeProfile: activeSaved(),
    profiles: [favorite()],
    modelDetail: '',
  })
  assert.equal(display.modelLabel, 'openai/gpt-5.6-codex · thinking high · fast')
})

test('resolved selector exposes one flat saved-favorite list plus the restored agent setup entry', () => {
  assert.match(source, /Session favorite/)
  assert.match(source, /aria-label="Saved favorites"/)
  assert.match(source, /onProfileSelect\?:/)
  assert.match(source, /await onProfileSelect\(profileId\)/)
  assert.match(source, /activeProfile\?\.source === 'saved'.*activeProfile\.profileId === profileId/)
  assert.match(source, /role="menuitemradio"/)
  assert.match(source, /No permitted saved favorites/)
  assert.match(source, /Open agent setup/)
  assert.match(source, /onOpenAgentSetup/)
  assert.doesNotMatch(source, /onAgentSelect|Add profile|Edit profile|Delete profile|onSetDefault|onReorderProfiles/)
  assert.doesNotMatch(source, /modelMode|\.single|\.plan|\.auto|Single|Split|Plan \$|Action \$/)
})

test('resolved selector preserves explicit loading, busy, and error states', () => {
  assert.match(source, /Loading favorites…/)
  assert.match(source, /Pending model change…/)
  assert.match(source, />Pending</)
  assert.match(source, /role="alert"/)
  assert.match(source, /aria-busy=\{busy \|\| loading\}/)
  assert.match(source, /setLocalError\(cause instanceof Error/)
})

test('existing and pending composers expose the flat favorite selector and share full agent setup', () => {
  assert.match(existingConversationSource, /resolvedSessionControls/)
  assert.equal((composerSource.match(/showFavoriteSelector \?/g) ?? []).length, 2)
  assert.match(composerSource, /onOpenAgentSetup=\{openAgentSetup\}/)
  assert.match(composerSource, /<AgentModelControl/)
  assert.match(readFileSync(new URL('./desktop-v3-new-session-pane.tsx', import.meta.url), 'utf8'), /onModelProfileSelect=\{setSwarmActionFavorite\}/)
})
