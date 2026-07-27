import assert from 'node:assert/strict'
import test from 'node:test'

import { titleToWorktreeBranchSlug } from './desktop-app-page'

test('worktree title derives a hyphenated editable branch suggestion', () => {
  assert.equal(titleToWorktreeBranchSlug('Fix the backend'), 'fix-the-backend')
  assert.equal(titleToWorktreeBranchSlug('  Fix: API / Backend!  '), 'fix-api-backend')
})
