import assert from 'node:assert/strict'
import test from 'node:test'

import { desktopRoutedSessionMetadata } from './desktop-routed-worktree-intent'

test('routed metadata omits retired managed-worktree intent', () => {
  assert.deepEqual(desktopRoutedSessionMetadata({ source: 'desktop-v3', managed_worktree_requested: false }), {
    source: 'desktop-v3',
  })
})
