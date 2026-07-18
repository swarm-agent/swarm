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

test('new sessions adopt the account default without overwriting a manual profile choice', () => {
  assert.match(newSessionSource, /profileManuallyChangedRef\.current \|\| modelProfileChoice \|\| !modelProfileState\.defaultProfileId/)
  assert.match(newSessionSource, /setModelProfileChoice\(\{ kind: 'account-default' \}\)/)
  assert.match(newSessionSource, /profileManuallyChangedRef\.current = true/)
  assert.match(newSessionSource, /preferenceManuallyChangedRef\.current = true/)
  assert.match(newSessionSource, /modelProfileChoice,/)
})

test('existing sessions route temporary and saved choices through the V3 profile mutation only', () => {
  assert.match(existingSessionSource, /profileChoice = \{ kind: 'temporary', profile: input\.modelProfile \}/)
  assert.match(existingSessionSource, /profileChoice = \{ kind: 'saved', profileId: saved\.profileId \}/)
  assert.match(existingSessionSource, /updateSessionV3ModelProfile\(normalizedSessionId, profileChoice\)/)
  assert.doesNotMatch(existingSessionSource, /updateAgentProfile|\/v2\/agents/)
})

test('profile modal preserves rename/default inputs and explicit action routing', () => {
  assert.match(controlSource, /setDraftProfileName\(name\)/)
  assert.match(controlSource, /setDraftMakeDefault\(makeDefault\)/)
  assert.match(controlSource, /persistence: 'temporary' \| 'create' \| 'update' \| 'create-copy'/)
  assert.match(controlSource, /Use for this session/)
  assert.match(controlSource, /Save changes and use/)
  assert.match(controlSource, /Save as new/)
})
