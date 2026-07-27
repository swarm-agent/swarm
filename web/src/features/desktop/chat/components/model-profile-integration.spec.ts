import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'

const onboardingSource = readFileSync(new URL('../../onboarding/components/desktop-onboarding-gate.tsx', import.meta.url), 'utf8')
const newSessionSource = readFileSync(new URL('./desktop-v3-new-session-pane.tsx', import.meta.url), 'utf8')
const existingSessionSource = readFileSync(new URL('./desktop-v3-existing-conversation-pane.tsx', import.meta.url), 'utf8')
const controlSource = readFileSync(new URL('./agent-model-control.tsx', import.meta.url), 'utf8')

test('provider onboarding refreshes the canonical profile query while skip remains profile-free', () => {
  assert.match(onboardingSource, /queryKey: modelProfilesQueryOptions\(\)\.queryKey/)
  assert.match(onboardingSource, /Skip for now/)
  assert.doesNotMatch(onboardingSource, /createModelProfile|\/v2\/agent-model-profiles/)
})

test('new sessions render and submit the account default without a stale draft-model frame', () => {
  assert.match(newSessionSource, /const defaultModelProfilePreference = defaultModelProfile/)
  assert.match(newSessionSource, /defaultModelProfilePreference \? \{ kind: 'account-default' \} : undefined/)
  assert.match(newSessionSource, /defaultModelProfilePreference \?\? \(\{/)
  assert.match(newSessionSource, /modelProfileAuthorityPending/)
  assert.match(newSessionSource, /preference: preferenceForRequest\(effectivePreference\)/)
  assert.match(newSessionSource, /modelProfileChoice: effectiveModelProfileChoice/)
  assert.match(newSessionSource, /profileManuallyChangedRef\.current \|\| modelProfileChoice \|\| !modelProfileState\.defaultProfileId/)
  assert.match(newSessionSource, /setModelProfileChoice\(\{ kind: 'account-default' \}\)/)
  assert.match(newSessionSource, /profileManuallyChangedRef\.current = true/)
  assert.match(newSessionSource, /preferenceManuallyChangedRef\.current = true/)
})

test('existing sessions route temporary and saved choices through the V3 profile mutation only', () => {
  assert.match(existingSessionSource, /profileChoice = \{ kind: 'temporary', profile: input\.modelProfile \}/)
  assert.match(existingSessionSource, /profileChoice = \{ kind: 'saved', profileId: saved\.profileId \}/)
  assert.match(existingSessionSource, /updateSessionV3ModelProfile\(normalizedSessionId, profileChoice\)/)
  assert.doesNotMatch(existingSessionSource, /updateAgentProfile|\/v2\/agents/)
})

test('existing composer makes the durable session profile authoritative across mode changes', () => {
  assert.match(existingSessionSource, /cacheSession\?\.metadata \?\? session\?\.metadata \?\? metadata/)
  assert.match(existingSessionSource, /const sessionProfilePreference = useMemo\(/)
  assert.match(existingSessionSource, /const displayedPreference = sessionProfilePreference \?\? sessionAgentPreference \?\? preference/)
  assert.match(existingSessionSource, /resolveDesktopV3SessionAgentModelLock\(sessionMetadata, mode\)/)
  assert.match(existingSessionSource, /resolveDesktopV3SessionAgentModelLock\(agentResponse\.metadata, mode\)/)
  assert.match(existingSessionSource, /preferenceFromModelProfileMetadata\(\s*sessionMetadata,\s*nextMode/)
  assert.match(existingSessionSource, /if \(nextProfilePreference\) \{\s*setPreference\(nextProfilePreference\);\s*return;/)
})

test('new-session composer resolves the active chosen profile when mode changes', () => {
  assert.match(newSessionSource, /const activeProfile = activeModelProfile\.source === 'saved'/)
  assert.match(newSessionSource, /preferenceFromModelProfile\(activeProfile, nextMode/)
  assert.match(newSessionSource, /handleModeSelect\(nextMode\)/)
  assert.match(newSessionSource, /modelProfileChoice\?\.kind === 'agent-default' \|\| !modelProfileChoice/)
  assert.match(newSessionSource, /modelProfileChoice && modelProfileChoice\.kind !== 'agent-default'/)
  assert.match(newSessionSource, /onUseAgentModelDefault=\{\(\) => \{[\s\S]*preferenceFromAgentModelLock\(selectedAgentModelLock, current, modelOptions\)/)
})

test('profile modal preserves rename, star-default persistence, and explicit action routing', () => {
  assert.match(controlSource, /setDraftProfileName\(name\)/)
  assert.match(controlSource, /setDraftMakeDefault\(makeDefault\)/)
  assert.match(controlSource, /onSetDefaultModelProfile\(profile\.profileId\)/)
  assert.match(controlSource, /fill=\{profile\.isDefault \? 'currentColor' : 'none'\}/)
  assert.doesNotMatch(controlSource, /type="checkbox" checked=\{draftMakeDefault\}/)
  assert.match(controlSource, /persistence: 'temporary' \| 'create' \| 'update' \| 'create-copy'/)
  assert.match(controlSource, /confirm\('temporary'\)/)
  assert.match(controlSource, /Continue for this chat only/)
  assert.match(controlSource, /confirm\(editingProfileId \? 'update' : 'create'\)/)
  assert.match(controlSource, /Save and apply/)
  assert.match(controlSource, /Save as new/)
})
