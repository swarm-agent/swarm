import assert from 'node:assert/strict'
import test from 'node:test'
import { readFile } from 'node:fs/promises'

test('worktree session creation preserves the workspace mode selected in the composer', async () => {
  const appSource = await readFile(new URL('./desktop-app-page.tsx', import.meta.url), 'utf8')
  const newSessionSource = await readFile(new URL('../chat/components/desktop-v3-new-session-pane.tsx', import.meta.url), 'utf8')
  const existingSessionSource = await readFile(new URL('../chat/components/desktop-v3-existing-conversation-pane.tsx', import.meta.url), 'utf8')

  assert.match(appSource, /const \[newSessionModeByWorkspace, setNewSessionModeByWorkspace\] = useState<Record<string, DesktopSessionMode>>\(\{\}\)/)
  assert.match(appSource, /mode: newSessionModeByWorkspace\[worktreeSessionModal\.workspacePath\][\s\S]*normalizeDefaultNewSessionMode/)
  assert.doesNotMatch(appSource, /createDesktopV3CreateOnlySessionOperation\(\{[\s\S]*?mode: 'auto'/)
  assert.match(appSource, /initialMode=\{newSessionModeByWorkspace\[routeWorkspace\.path\]\}/)
  assert.match(newSessionSource, /onModeChange\?\.\(nextMode\)/)
  assert.match(existingSessionSource, /onModeChange\?\.\(nextMode\)/)
})
