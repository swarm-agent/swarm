import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'
import { profilePickerAgentSections, profileTriggerDisplay } from './profile-agent-picker'
import type { ActiveModelProfileState, AgentProfileRecord, ModelProfileRecord } from '../types/chat'

const source = readFileSync(new URL('./profile-agent-picker.tsx', import.meta.url), 'utf8')
const setupSource = readFileSync(new URL('./agent-model-control.tsx', import.meta.url), 'utf8')
const composerSource = readFileSync(new URL('./desktop-v3-agentic-composer.tsx', import.meta.url), 'utf8')

function savedProfile(modelMode: 'single' | 'split'): ModelProfileRecord {
  const selection = (provider: string, model: string) => ({ provider, model, thinking: 'high', serviceTier: '', contextMode: '' })
  return {
    profileId: 'profile-1',
    name: 'My models',
    modelMode,
    single: modelMode === 'single' ? selection('openai', 'gpt-5.4') : null,
    plan: modelMode === 'split' ? selection('anthropic', 'claude-opus-4-6') : null,
    auto: modelMode === 'split' ? selection('openai', 'gpt-5.4') : null,
    createdAt: 1,
    updatedAt: 1,
    isDefault: true,
  }
}

function activeSaved(modelMode: 'single' | 'split'): ActiveModelProfileState {
  return { source: 'saved', profileId: 'profile-1', name: 'My models', modelMode }
}

function agent(name: string, mode = 'primary'): AgentProfileRecord {
  return { name, mode, enabled: true } as AgentProfileRecord
}

test('profile picker keeps minimal stacked profile cards before separated stacked agent sections', () => {
  assert.match(source, /aria-label="Profiles"/)
  assert.match(source, /No profiles yet/)
  assert.match(source, /Add profile/)
  assert.equal((source.match(/className="grid gap-1\.5"/g) ?? []).length, 2)
  assert.doesNotMatch(source, /sm:grid-cols-2/)
  assert.match(source, /Agent setup/)
  assert.doesNotMatch(source, /Primary agents/)
  assert.match(source, /Subagents/)
  assert.ok(source.indexOf('<section aria-label="Profiles"') < source.indexOf('{agentSections.map'))
})

test('profile-first selection hides Swarm when profiles exist and retains it only as the no-profile fallback', () => {
  const agents = [agent('swarm'), agent('writer'), agent('system-explorer', 'subagent')]
  const withProfiles = profilePickerAgentSections(agents, true)
  const withoutProfiles = profilePickerAgentSections(agents, false)

  assert.deepEqual(withProfiles.map((section) => section.label), ['Agents', 'Subagents'])
  assert.deepEqual(withProfiles.flatMap((section) => section.agents.map((entry) => entry.name)), ['writer', 'system-explorer'])
  assert.deepEqual(withoutProfiles.find((section) => section.label === 'Default agent')?.agents.map((entry) => entry.name), ['swarm'])
  assert.equal(withProfiles.some((section) => section.agents.some((entry) => entry.name === 'swarm')), false)
})

test('agent setup applies the same profile-first Swarm suppression without primary-agent copy', () => {
  assert.match(setupSource, /label: 'Default agent', profiles: modelProfiles\.length === 0/)
  assert.match(setupSource, /agent\.name !== SWARM_AGENT_NAME/)
  assert.doesNotMatch(setupSource, /label: 'Primary agents'/)
})

test('profile picker exposes vertically stacked model summaries, default management, editing, and confirmed deletion', () => {
  assert.match(source, /Plan \$\{selectionLabel\(profile\.plan\)\}/)
  assert.match(source, /Action \$\{selectionLabel\(profile\.auto\)\}/)
  assert.match(source, /profileLabels\(profile\)\.map\(\(label\) => <span key=\{label\} className="block truncate">/)
  assert.doesNotMatch(source, /profileLabels\(profile\)\.join\(' · '\)/)
  assert.match(source, />Default</)
  assert.match(source, /Make .* default/)
  assert.match(source, /window\.confirm/)
  assert.match(source, /Delete profile/)
})

test('agent setup offers explicit temporary and saved outcomes with customized detection', () => {
  assert.match(setupSource, /Profile name/)
  assert.match(setupSource, /Account default/)
  assert.match(setupSource, /Customized — this draft differs from the saved baseline/)
  assert.match(setupSource, /Your profiles/)
  assert.match(setupSource, /Switch profiles to inspect or edit their model setup/)
  assert.match(setupSource, /<div className="grid gap-1\.5">/)
  assert.match(setupSource, /`Plan · \$\{savedProfileSelectionLabel\(profile\.plan\)\}`/)
  assert.match(setupSource, /`Action · \$\{savedProfileSelectionLabel\(profile\.auto\)\}`/)
  assert.match(setupSource, /savedProfileModelLabels\(profile\)\.map\(\(label\) => <span key=\{label\} className="block truncate">/)
  assert.doesNotMatch(setupSource, /Your profiles[\s\S]*?sm:grid-cols-2[\s\S]*?No saved profiles yet/)
  assert.match(setupSource, /Use without saving/)
  assert.match(setupSource, /Create profile and use/)
  assert.match(setupSource, /Save changes and use/)
  assert.match(setupSource, /Save as new/)
})

test('composer trigger keeps a single-profile model visible beside its profile name', () => {
  const display = profileTriggerDisplay({
    activeProfile: activeSaved('single'),
    profiles: [savedProfile('single')],
    mode: 'auto',
    modelDetail: 'GPT-5.4 · thinking high · tier standard',
  })

  assert.equal(display.profileLabel, 'My models')
  assert.equal(display.modelLabel, 'GPT-5.4 · thinking high · tier standard')
  assert.equal(display.combinedLabel, 'My models · GPT-5.4 · thinking high · tier standard')
})

test('composer trigger identifies the currently resolved Plan and Action models for a split profile', () => {
  const profile = savedProfile('split')
  assert.equal(profileTriggerDisplay({ activeProfile: activeSaved('split'), profiles: [profile], mode: 'plan', modelDetail: '' }).modelLabel, 'Plan anthropic/claude-opus-4-6 · thinking high')
  assert.equal(profileTriggerDisplay({ activeProfile: activeSaved('split'), profiles: [profile], mode: 'auto', modelDetail: '' }).modelLabel, 'Action openai/gpt-5.4 · thinking high')
})

test('temporary and agent-default selections keep their resolved models visible', () => {
  assert.equal(profileTriggerDisplay({
    activeProfile: { source: 'temporary', profileId: '', name: 'Temporary/customized', modelMode: 'split' },
    profiles: [],
    mode: 'auto',
    modelDetail: 'GPT-5.4 · thinking medium · tier standard',
  }).combinedLabel, 'Temporary/customized · Action GPT-5.4 · thinking medium · tier standard')
  assert.equal(profileTriggerDisplay({ profiles: [], mode: 'plan', modelDetail: 'Claude Opus 4.6 · thinking high · tier standard' }).modelLabel, 'Claude Opus 4.6 · thinking high · tier standard')
})

test('desktop and mobile composer variants share the model-visible profile picker', () => {
  assert.equal((composerSource.match(/<ProfileAgentPicker/g) ?? []).length, 2)
  assert.equal((composerSource.match(/modelDetail=\{modelControlDetail\}/g) ?? []).length, 2)
  assert.equal((composerSource.match(/renderTrigger=\{\(\{ openPicker, open \}\) => renderComposerControl\(openPicker, open\)\}/g) ?? []).length, 2)
  assert.match(source, /data-testid="selected-model-detail"/)
  assert.doesNotMatch(composerSource, /<select\s+aria-label="Model profile"/)
})

test('profile picker supports a custom compact trigger without duplicating its dropdown', () => {
  assert.match(source, /renderTrigger\?:/)
  assert.match(source, /renderTrigger\(\{ \.\.\.triggerDisplay, open, openPicker, disabled \}\)/)
  assert.match(source, /aria-haspopup="menu"/)
})
