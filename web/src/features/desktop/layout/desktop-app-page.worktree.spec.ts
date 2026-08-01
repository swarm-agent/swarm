import assert from 'node:assert/strict'
import test from 'node:test'
import { readFile } from 'node:fs/promises'

test('new-chat worktree flow has no manual branch or title authority', async () => {
  const source = await readFile(new URL('./desktop-app-page.tsx', import.meta.url), 'utf8')

  assert.match(source, /document\.querySelector<HTMLButtonElement>\('\[data-testid="desktop-routed-worktree-prime"\]'\)/)
  assert.match(source, /prime\.dataset\.worktreeRequested !== 'true'/)
  assert.doesNotMatch(source, /titleToWorktreeBranchSlug|composeWorktreeBranchName|Branch suffix:/)
  assert.doesNotMatch(source, /worktree_branch_name|branchName:/)
})
