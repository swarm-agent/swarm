import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

test('worktree settings removes only the obsolete automatic-worktree toggle', async () => {
  const source = await readFile(new URL('./worktree-settings-page.tsx', import.meta.url), 'utf8')

  assert.doesNotMatch(source, /Enable Automatic Worktrees/)
  assert.doesNotMatch(source, /enabled:\s*input\.enabled/)
  assert.match(source, /Created branch prefix/)
  assert.match(source, /Branch-off source/)
  assert.match(source, /use_current_branch:\s*input\.useCurrentBranch/)
  assert.match(source, /branch_name:\s*normalizeBranchPrefix\(input\.branchName\)/)
})
