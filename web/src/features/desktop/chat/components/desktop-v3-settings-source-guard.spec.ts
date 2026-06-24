import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'

test('Desktop V3 existing composer derives agent model locks from existing agent-state and sequential submit mutations', async () => {
  const source = await readFile(new URL('./desktop-v3-existing-conversation-pane.tsx', import.meta.url), 'utf8')

  assert.match(source, /useQuery\(agentStateQueryOptions\(\)\)/)
  assert.match(source, /resolveDesktopV3AgentModelLock\(agentState\.profiles/)
  assert.match(source, /rawCachedPreference\s*=\s*useDesktopV3CacheSelector\(\(state\)\s*=>\s*state\.preferencesBySession\[normalizedSessionId\]\)/)
  assert.match(source, /cachedPreference\s*=\s*useMemo\(\(\)\s*=>\s*normalizePreference\(rawCachedPreference\), \[rawCachedPreference\]\)/)
  assert.match(source, /function buildDesktopV3ExistingSettingsSnapshot\([\s\S]*metadataString\(input\.metadata, 'agent_name'\)[\s\S]*metadataString\(input\.metadata, 'resolved_agent_name'\)/)
  assert.match(source, /initializedSettingsSessionRef\.current !== normalizedSessionId/)
  assert.match(source, /localSettingsDirtyRef\.current\.agent = true/)
  assert.match(source, /settingsActionLabel=\{showRestartSettingsAction \? 'Restart loop with new settings\?' : ''\}/)
  assert.match(source, /await stopSessionV3Run[\s\S]*await persistVisibleSettings\(\)[\s\S]*sendSessionMessage\([\s\S]*'system'/)
  assert.doesNotMatch(source, /const initialAgent =/)
  assert.doesNotMatch(source, /setSelectedAgent\(initialAgent\)/)
  assert.doesNotMatch(source, /resolveDesktopV3AgentModelLock\(agentState\.profiles, selectedAgent \|\| agentState\.activePrimary/)
  assert.doesNotMatch(source, /useDesktopV3CacheSelector\(\(state\)\s*=>\s*normalizePreference\(state\.preferencesBySession/)
  assert.doesNotMatch(source, /fetchAgentState\(/)
  assert.doesNotMatch(source, /fetchAgentProfile/)
  assert.doesNotMatch(source, /Promise\.all\(tasks\)/)
  assert.match(
    source,
    /await updateSessionV3Mode[\s\S]*dispatchDesktopV3Cache\(\{[\s\S]*sessionV3ModeSettingsMutationResponse[\s\S]*await updateSessionV3Agent[\s\S]*dispatchDesktopV3Cache\(\{[\s\S]*sessionV3AgentSettingsMutationResponse[\s\S]*await updateSessionV3Preference[\s\S]*dispatchDesktopV3Cache\(\{[\s\S]*sessionV3PreferenceSettingsMutationResponse/,
  )
})
