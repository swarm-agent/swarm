import assert from 'node:assert/strict'
import test from 'node:test'
import { readFile } from 'node:fs/promises'

test('new-chat worktree entry only primes routed boolean intent', async () => {
  const appSource = await readFile(new URL('./desktop-app-page.tsx', import.meta.url), 'utf8')
  const newSessionSource = await readFile(new URL('../chat/components/desktop-v3-new-session-pane.tsx', import.meta.url), 'utf8')

  assert.match(appSource, /handleStartNewSessionInWorkspace\(workspace\.path, workspace\.workspaceName, \{ worktreeRequested: true \}\)/)
  assert.match(appSource, /Worktree intent is chosen before routing/)
  assert.doesNotMatch(appSource, /createDesktopV3CreateOnlySessionOperation|startDesktopV3CreateOnlySession/)
  assert.doesNotMatch(appSource, /worktreeSessionTitle|worktreeSessionBranch|titleToWorktreeBranchSlug/)
  assert.doesNotMatch(appSource, /newSessionModeByWorkspace|explicitMode:/)
  assert.doesNotMatch(newSessionSource, /modeCommand|initialMode|onModeChange|pendingWorktreeBranch/)
})
