import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'
import { profilePickerAgentSections, profileTriggerDisplay } from './profile-agent-picker'
import { modelProfileDraftIsCustomized, resolveInitialModelProfileId } from './agent-model-control'
import type { ActiveModelProfileState, AgentProfileRecord, ModelProfileRecord } from '../types/chat'

const source = readFileSync(new URL('./profile-agent-picker.tsx', import.meta.url), 'utf8')
const setupSource = readFileSync(new URL('./agent-model-control.tsx', import.meta.url), 'utf8')
const composerSource = readFileSync(new URL('./desktop-v3-agentic-composer.tsx', import.meta.url), 'utf8')
const existingConversationSource = readFileSync(new URL('./desktop-v3-existing-conversation-pane.tsx', import.meta.url), 'utf8')

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

test('profile picker keeps readable outlined profile cards before separated agent sections', () => {
  assert.match(source, /aria-label="Profiles"/)
  assert.match(source, /No \{modelProfilePolicyGroupLabel\(profileGroup\)\.toLowerCase\(\)\} profiles yet/)
  assert.match(source, /Add profile/)
  assert.match(source, /<div className="grid gap-2">/)
  assert.match(source, /rounded-lg border \$\{selected \? 'border-\[var\(--app-primary\)\] bg-\[var\(--app-surface-subtle\)\] shadow-sm'/)
  assert.match(source, /text-sm font-semibold leading-5/)
  assert.match(source, /mt-1 grid gap-0\.5 text-xs leading-4/)
  assert.doesNotMatch(source, /shadow-\[inset_2px_0_0_var\(--app-primary\)\]/)
  assert.doesNotMatch(source, /sm:grid-cols-2/)
  assert.match(source, /Agent setup/)
  assert.doesNotMatch(source, /Primary agents/)
  assert.match(source, /Subagents/)
  assert.ok(source.indexOf('<section aria-label="Profiles"') < source.indexOf('{agentSections.map'))
})

test('profile-first quick menu hides Swarm when profiles exist and retains it only as the no-profile fallback', () => {
  const agents = [agent('swarm'), agent('writer'), agent('system-finder', 'subagent')]
  const withProfiles = profilePickerAgentSections(agents, true)
  const withoutProfiles = profilePickerAgentSections(agents, false)

  assert.deepEqual(withProfiles.map((section) => section.label), ['Agents', 'Subagents'])
  assert.deepEqual(withProfiles.flatMap((section) => section.agents.map((entry) => entry.name)), ['writer', 'system-finder'])
  assert.deepEqual(withoutProfiles.find((section) => section.label === 'Default agent')?.agents.map((entry) => entry.name), ['swarm'])
  assert.equal(withProfiles.some((section) => section.agents.some((entry) => entry.name === 'swarm')), false)
})

test('agent setup keeps Swarm first and selectable in Agents regardless of saved profiles', () => {
  assert.match(setupSource, /label: 'Agents', profiles: \[\.\.\.\(swarmProfile \? \[swarmProfile\] : \[\]\), \.\.\.primaryProfiles\]/)
  assert.doesNotMatch(setupSource, /label: 'Default agent'/)
  assert.doesNotMatch(setupSource, /label: 'Primary agents'/)
})

test('existing session selector restores the active profile from hydrated session metadata', () => {
  assert.match(existingConversationSource, /activeModelProfileFromMetadata\(sessionMetadata\)/)
  assert.doesNotMatch(existingConversationSource, /activeModelProfileFromPolicy\(cachedAgentModelPolicy\)/)
})

test('profile menus combine single and plan-action profiles without policy group switches', () => {
  assert.match(source, /Saved profiles/)
  assert.match(source, /profiles\.map/)
  assert.match(source, /max-h-\[310px\].*overflow-y-auto/)
  assert.doesNotMatch(source, /PolicyGroupSwitch|visibleProfiles/)
  assert.match(setupSource, /aria-label="Agent setup sections"/)
  assert.match(setupSource, /grid-cols-\[240px_280px_minmax\(0,1fr\)\]/)
  assert.match(setupSource, /aria-label="Agents"[\s\S]*aria-label="Saved model profiles"[\s\S]*aria-label="Selected profile settings"/)
  assert.match(setupSource, /Saved profiles/)
  assert.match(setupSource, /compatibleModelProfiles\.map/)
  assert.match(setupSource, /chooseModelProfile\(profile\)/)
  assert.doesNotMatch(setupSource, /aria-label="Saved model profile"|SetupProfileGroupSwitch|visibleModelProfiles/)
})

test('profile picker exposes model summaries, star default management, direct row application, and confirmed deletion', () => {
  assert.match(source, /Plan \$\{selectionLabel\(profile\.plan\)\}/)
  assert.match(source, /Action \$\{selectionLabel\(profile\.auto\)\}/)
  assert.match(source, /profileLabels\(profile\)\.map\(\(label\) => <span key=\{label\} className="block truncate">/)
  assert.doesNotMatch(source, /profileLabels\(profile\)\.join\(' · '\)/)
  assert.doesNotMatch(source, /\bCheck\b/)
  assert.doesNotMatch(source, />Preferred</)
  assert.match(source, /fill=\{profile\.isDefault \? 'currentColor' : 'none'\}/)
  assert.match(source, /Make .* the account default/)
  assert.match(source, /aria-label=\{`Apply \$\{profile\.name\}`\}/)
  assert.match(source, /Pencil/)
  assert.match(source, /aria-label=\{`Edit \$\{profile\.name\}`\}/)
  assert.match(source, /GripVertical/)
  assert.match(source, /onReorderProfiles/)
  assert.match(source, /window\.confirm/)
  assert.match(source, /Delete profile/)
})

test('agent setup loads the selected or account-default profile as the editable baseline', () => {
  const accountDefault = savedProfile('single')
  const otherProfile = { ...savedProfile('single'), profileId: 'profile-2', name: 'Other', isDefault: false }

  assert.equal(resolveInitialModelProfileId(undefined, undefined, [otherProfile, accountDefault]), accountDefault.profileId)
  assert.equal(resolveInitialModelProfileId(undefined, activeSaved('single'), [otherProfile, accountDefault]), accountDefault.profileId)
  assert.equal(resolveInitialModelProfileId(otherProfile.profileId, activeSaved('single'), [otherProfile, accountDefault]), otherProfile.profileId)
  assert.equal(resolveInitialModelProfileId('', activeSaved('single'), [otherProfile, accountDefault]), '')
  assert.equal(modelProfileDraftIsCustomized('saved', 'saved'), false)
  assert.equal(modelProfileDraftIsCustomized('saved', 'changed'), true)
  assert.match(setupSource, /initializedOpenRef\.current/)
  assert.match(setupSource, /const saved = requestedProfileId \? modelProfiles\.find/)
  assert.match(setupSource, /Unsaved changes — choose whether to update the saved profile or use this draft only in the current chat/)
  assert.match(composerSource, /setAgentSetupProfileId\(undefined\)/)
  assert.match(composerSource, /setAgentSetupProfileId\(''\)/)
  assert.match(setupSource, /initialModelProfileId === '' \? ''/)
  assert.match(setupSource, /ref=\{profileNameInputRef\}/)
  assert.match(setupSource, /input\.focus\(\)[\s\S]*input\.setSelectionRange\(0, 0\)/)
})

test('agent setup offers explicit chat-only and persisted outcomes for saved profiles', () => {
  assert.match(setupSource, /Profile name/)
  assert.match(setupSource, /Editing your account default profile\. Saving updates it everywhere; continuing for this chat only leaves it unchanged/)
  assert.match(setupSource, /onSetDefaultModelProfile\(profile\.profileId\)/)
  assert.match(setupSource, /fill=\{profile\.isDefault \? 'currentColor' : 'none'\}/)
  assert.doesNotMatch(setupSource, /type="checkbox" checked=\{draftMakeDefault\}/)
  assert.match(setupSource, /aria-label="Agent setup sections"/)
  assert.match(setupSource, /aria-label="Agents"[\s\S]*aria-label="Saved model profiles"[\s\S]*aria-label="Selected profile settings"/)
  assert.match(setupSource, /grid-cols-\[240px_280px_minmax\(0,1fr\)\]/)
  assert.match(setupSource, /Choose a model preset/)
  assert.match(setupSource, /text-sm font-semibold leading-5/)
  assert.match(setupSource, /text-\[10px\] leading-4 text-\[var\(--app-text-subtle\)\]/)
  assert.doesNotMatch(setupSource, /shadow-\[inset_2px_0_0_var\(--app-primary\)\]/)
  assert.match(setupSource, /`Plan · \$\{savedProfileSelectionLabel\(profile\.plan\)\}`/)
  assert.match(setupSource, /`Action · \$\{savedProfileSelectionLabel\(profile\.auto\)\}`/)
  assert.match(setupSource, /savedProfileModelLabels\(profile\)\.map/)
  assert.match(setupSource, /return \[selection\.provider\.trim\(\), selection\.model\.trim\(\)\]/)
  assert.doesNotMatch(setupSource, /selection\.thinking\.trim\(\) \? `thinking/)
  assert.doesNotMatch(setupSource, /selection\.serviceTier\.trim\(\)/)
  assert.match(setupSource, /group-hover:opacity-100 group-focus-within:opacity-100/)
  assert.match(setupSource, /Make \$\{profile\.name\} the account default/)
  assert.match(setupSource, /onReorderModelProfiles/)
  assert.match(setupSource, /moveModelProfileByOffset/)
  assert.match(setupSource, /GripVertical/)
  assert.doesNotMatch(setupSource, /aria-label="Saved model profile"/)
  assert.match(setupSource, /Continue for this chat only/)
  assert.match(setupSource, /Create profile and apply/)
  assert.match(setupSource, /Save and apply/)
  assert.match(setupSource, /Save as new/)
  assert.match(composerSource, /onSetDefaultModelProfile=\{onModelProfileSetDefault\}/)
})

test('agent setup hides split profiles from agents that only support one model', () => {
  assert.match(setupSource, /function modelProfileAvailableForAgent[\s\S]*profile\.modelMode === 'single' \|\| isPlanCapableAgent\(agent\)/)
  assert.match(setupSource, /const compatibleModelProfiles = modelProfiles\.filter/)
  assert.match(setupSource, /requestedSaved && modelProfileAvailableForAgent\(requestedSaved, profile\)/)
  assert.match(setupSource, /saved && !modelProfileAvailableForAgent\(saved, draftProfile\)/)
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
