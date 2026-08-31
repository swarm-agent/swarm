import assert from 'node:assert/strict'
import test from 'node:test'
import { readFile } from 'node:fs/promises'

test('new-chat routing makes isolation implicit and keeps Plan selectable', async () => {
  const appSource = await readFile(new URL('./desktop-app-page.tsx', import.meta.url), 'utf8')
  const newSessionSource = await readFile(new URL('../chat/components/desktop-v3-new-session-pane.tsx', import.meta.url), 'utf8')

  assert.match(appSource, /planModeRequested/)
  assert.doesNotMatch(newSessionSource, /worktreePrimed|managed_worktree_requested/)
  assert.match(newSessionSource, /initialPlanModeRequested/)
  assert.doesNotMatch(appSource, /newWorktree|worktreeRequested|workspaceWorktreeMatch/)
  assert.doesNotMatch(newSessionSource, /initialWorktreeRequested|onRoutedWorktreeRequestedChange|pendingWorktreeBranch/)
  assert.doesNotMatch(appSource, /worktreeSessionTitle|worktreeSessionBranch|titleToWorktreeBranchSlug/)
})
