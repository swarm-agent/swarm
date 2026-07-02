import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'

test('Desktop V3 existing composer stages agent/model setup and persists profile settings through Agents API', async () => {
  const source = await readFile(new URL('./desktop-v3-existing-conversation-pane.tsx', import.meta.url), 'utf8')
  const composerSource = await readFile(new URL('./desktop-v3-agentic-composer.tsx', import.meta.url), 'utf8')
  const controlSource = await readFile(new URL('./agent-model-control.tsx', import.meta.url), 'utf8')

  assert.match(source, /useQuery\(agentStateQueryOptions\(\)\)/)
  assert.match(source, /resolveDesktopV3AgentModelLock\(agentState\.profiles/)
  assert.match(source, /rawCachedPreference\s*=\s*useDesktopV3CacheSelector\(\(state\)\s*=>\s*state\.preferencesBySession\[normalizedSessionId\]\)/)
  assert.match(source, /cachedPreference\s*=\s*useMemo\(\(\)\s*=>\s*normalizePreference\(rawCachedPreference\), \[rawCachedPreference\]\)/)
  assert.match(source, /function buildDesktopV3ExistingSettingsSnapshot\([\s\S]*metadataString\(input\.metadata, 'agent_name'\)[\s\S]*metadataString\(input\.metadata, 'resolved_agent_name'\)/)
  assert.match(source, /initializedSettingsSessionRef\.current !== normalizedSessionId/)
  assert.match(source, /await updateAgentProfile\(input\.profile, input\.patch\)/)
  assert.match(source, /await queryClient\.fetchQuery\(agentStateQueryOptions\(\)\)/)
  assert.match(source, /if \(input\.agentName\.trim\(\) && input\.agentName\.trim\(\) !== currentAgent\)[\s\S]*await updateSessionV3Agent\(normalizedSessionId, input\.agentName\.trim\(\)\)/)
  assert.doesNotMatch(source, /updateSessionV3Preference/)
  assert.match(source, /handleModeSelect\(nextMode: DesktopSessionMode\)[\s\S]*localSettingsDirtyRef\.current\.mode = true[\s\S]*setMode\(nextMode\)/)
  assert.match(source, /await updateSessionV3Mode\(normalizedSessionId, mode\)/)
  assert.doesNotMatch(source, /updateDraftModelPreference/)
  assert.doesNotMatch(source, /switchAgentToSingleModel/)
  assert.doesNotMatch(source, /switchAgentToDefaultModel/)

  assert.match(composerSource, /<button type="button" onClick=\{handleModeToggle\}[\s\S]*\{mode\}[\s\S]*<AgentModelControl/)
  assert.match(composerSource, /<AgentModelControl[\s\S]*triggerDetail=\{modelControlDetail/)
  assert.match(composerSource, /<AgentModelControl[\s\S]*onConfirmAgentSettings=\{onConfirmAgentSettings\}/)
  assert.doesNotMatch(composerSource, /<ModelPicker/)
  assert.doesNotMatch(composerSource, /<ThinkingPicker/)
  assert.doesNotMatch(composerSource, /onModelSelect/)
  assert.doesNotMatch(composerSource, /onThinkingChange/)
  assert.doesNotMatch(composerSource, /onFastChange/)

  assert.match(controlSource, /setDraftAgentName\(profile\.name\)/)
  assert.match(controlSource, /await onConfirmAgentSettings\?\.\(\{ agentName: profile\.name, profile, patch \}\)/)
  assert.doesNotMatch(controlSource, /onAgentSelect\(profile\.name\)/)
})
