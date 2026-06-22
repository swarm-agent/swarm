import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'

test('Desktop V3 existing composer derives agent model locks from existing agent-state and sequential submit mutations', async () => {
  const source = await readFile(new URL('./desktop-v3-existing-conversation-pane.tsx', import.meta.url), 'utf8')

  assert.match(source, /useQuery\(agentStateQueryOptions\(\)\)/)
  assert.match(source, /resolveDesktopV3AgentModelLock\(agentState\.profiles/)
  assert.doesNotMatch(source, /fetchAgentState\(/)
  assert.doesNotMatch(source, /fetchAgentProfile/)
  assert.doesNotMatch(source, /Promise\.all\(tasks\)/)
  assert.match(
    source,
    /await updateSessionV3Mode[\s\S]*await updateSessionV3Agent[\s\S]*await updateSessionV3Preference/,
  )
})
