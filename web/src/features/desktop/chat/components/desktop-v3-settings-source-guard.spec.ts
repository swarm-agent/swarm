import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'

test('Desktop V3 chat switches agents canonically and keeps mode separate', async () => {
  const source = await readFile(new URL('./desktop-v3-existing-conversation-pane.tsx', import.meta.url), 'utf8')
  const newSessionSource = await readFile(new URL('./desktop-v3-new-session-pane.tsx', import.meta.url), 'utf8')
  const composerSource = await readFile(new URL('./desktop-v3-agentic-composer.tsx', import.meta.url), 'utf8')
  const pickerSource = await readFile(new URL('./agent-picker.tsx', import.meta.url), 'utf8')
  const settingsSource = await readFile(new URL('./agent-model-control.tsx', import.meta.url), 'utf8')

  assert.match(source, /useQuery\(agentStateQueryOptions\(\)\)/)
  assert.match(source, /handleAgentSelect\(nextAgentName: string\)[\s\S]*await updateSessionV3Agent\([\s\S]*normalizedAgentName[\s\S]*setSelectedAgent\(normalizedAgentName\)/)
  assert.match(source, /onAgentSelect=\{handleAgentSelect\}/)
  assert.match(newSessionSource, /handleAgentSelect\(nextAgentName: string\)[\s\S]*setSelectedAgent\(normalizedAgentName\)/)
  assert.match(newSessionSource, /onAgentSelect=\{handleAgentSelect\}/)

  assert.match(composerSource, /<ModePicker mode=\{mode\}[\s\S]*<AgentPicker/)
  assert.doesNotMatch(pickerSource, /onModeSelect|Session mode for/)
  assert.match(settingsSource, /title="Default Model"/)
  assert.match(settingsSource, /title="Plan Model"/)
  assert.match(settingsSource, /System agents/)
  assert.doesNotMatch(settingsSource, /Saved profiles|Profile settings/)
})
