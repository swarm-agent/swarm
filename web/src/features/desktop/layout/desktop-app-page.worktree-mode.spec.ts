import assert from 'node:assert/strict'
import test from 'node:test'
import { readFile } from 'node:fs/promises'

test('worktree session creation preserves the workspace mode selected in the composer', async () => {
  const appSource = await readFile(new URL('./desktop-app-page.tsx', import.meta.url), 'utf8')
  const newSessionSource = await readFile(new URL('../chat/components/desktop-v3-new-session-pane.tsx', import.meta.url), 'utf8')
  const existingSessionSource = await readFile(new URL('../chat/components/desktop-v3-existing-conversation-pane.tsx', import.meta.url), 'utf8')

  assert.match(appSource, /const \[newSessionModeByWorkspace, setNewSessionModeByWorkspace\] = useState<Record<string, DesktopSessionMode>>\(\{\}\)/)
  assert.match(appSource, /explicitMode: newSessionModeByWorkspace\[worktreeSessionModal\.workspacePath\]/)
  assert.match(appSource, /mode: defaults\.mode/)
  assert.doesNotMatch(appSource, /createDesktopV3CreateOnlySessionOperation\(\{[\s\S]*?mode: 'auto'/)
  assert.match(appSource, /agentName: defaults\.agentName/)
  assert.match(appSource, /modelProfileChoice: defaults\.modelProfileChoice/)
  assert.doesNotMatch(appSource, /activePrimary\?\.trim\(\) \|\| 'swarm'/)
  assert.match(appSource, /Desktop agent and model-profile settings are still loading\./)
  assert.match(appSource, /initialMode=\{newSessionModeByWorkspace\[routeWorkspace\.path\]\}/)
  assert.match(newSessionSource, /onModeChange\?\.\(nextMode\)/)
  assert.match(existingSessionSource, /onModeChange\?\.\(nextMode\)/)
})
