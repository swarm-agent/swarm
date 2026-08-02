import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'

const onboardingSource = readFileSync(new URL('../../onboarding/components/desktop-onboarding-gate.tsx', import.meta.url), 'utf8')
const newSessionSource = readFileSync(new URL('./desktop-v3-new-session-pane.tsx', import.meta.url), 'utf8')
const existingSessionSource = readFileSync(new URL('./desktop-v3-existing-conversation-pane.tsx', import.meta.url), 'utf8')
const controlSource = readFileSync(new URL('./agent-model-control.tsx', import.meta.url), 'utf8')

test('provider onboarding refreshes direct Swarm settings and generic profile queries while skip remains profile-free', () => {
  assert.match(onboardingSource, /queryKey: modelProfilesQueryOptions\(\)\.queryKey/)
  assert.match(onboardingSource, /queryKey: \['swarm-model-settings'\]/)
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

test('agent setup removes profile management while preserving direct model persistence', () => {
  assert.doesNotMatch(controlSource, /Saved profiles|Profile settings|onSetDefaultModelProfile|Continue for this chat only|Save as new/)
  assert.match(controlSource, /title="Default Model"/)
  assert.match(controlSource, /title="Plan Model"/)
  assert.match(controlSource, /saveSystemAgentSettings/)
  assert.match(controlSource, /saveSwarmModelSettings/)
  assert.match(controlSource, /action: \{ provider: action\.provider/)
  assert.doesNotMatch(controlSource, /createModelProfile\(action|updateModelProfile\(currentAction|actionFavoriteId|planFavoriteId/)
  assert.match(newSessionSource, /input\.agentName\.trim\(\)\.toLowerCase\(\) === 'swarm'/)
  assert.match(existingSessionSource, /input\.agentName\.trim\(\)\.toLowerCase\(\) === 'swarm'/)
})
