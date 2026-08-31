import assert from 'node:assert/strict'
import test from 'node:test'
import { readFile } from 'node:fs/promises'

test('new-chat flow leaves worktree and branch authority to the server', async () => {
  const source = await readFile(new URL('./desktop-app-page.tsx', import.meta.url), 'utf8')

  assert.match(source, /handleStartNewSessionInWorkspace/)
  assert.doesNotMatch(source, /newWorktree|worktreeRequested|workspaceWorktreeMatch/)
  assert.doesNotMatch(source, /titleToWorktreeBranchSlug|composeWorktreeBranchName|Branch suffix:/)
  assert.doesNotMatch(source, /worktree_branch_name|branchName:/)
})
