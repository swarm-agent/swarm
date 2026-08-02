import assert from 'node:assert/strict'
import test from 'node:test'

import {
  DESKTOP_ROUTED_MANAGED_WORKTREE_REQUESTED_METADATA_KEY,
  captureDesktopRoutedWorktreeIntent,
  createDesktopRoutedWorktreeIntent,
  desktopRoutedMessageRequestsWorktree,
  encodeDesktopRoutedWorktreeIntentMetadata,
  resolveDesktopRoutedWorktreeIntent,
  restoreDesktopRoutedWorktreeIntent,
  setDesktopRoutedWorktreeIntent,
  toggleDesktopRoutedWorktreeIntent,
} from './desktop-routed-worktree-intent'

test('routed worktree intent is framework-neutral boolean state', () => {
  const initial = createDesktopRoutedWorktreeIntent()
  const currentWorkspace = toggleDesktopRoutedWorktreeIntent(initial)

  assert.deepEqual(initial, { requested: false })
  assert.deepEqual(currentWorkspace, { requested: true })
  assert.deepEqual(toggleDesktopRoutedWorktreeIntent(currentWorkspace), initial)
  assert.deepEqual(setDesktopRoutedWorktreeIntent(currentWorkspace, false), initial)
  assert.deepEqual(Object.keys(currentWorkspace), ['requested'])
  assert.throws(
    () => createDesktopRoutedWorktreeIntent('named-worktree' as never),
    /must be boolean/,
  )
})

test('routed worktree intent captures and restores the exact pre-submit value', () => {
  for (const requested of [false, true]) {
    const before = createDesktopRoutedWorktreeIntent(requested)
    const snapshot = captureDesktopRoutedWorktreeIntent(before)
    const changedDuringRouting = toggleDesktopRoutedWorktreeIntent(before)

    assert.notDeepEqual(changedDuringRouting, before)
    assert.deepEqual(restoreDesktopRoutedWorktreeIntent(snapshot), before)
  }

  assert.throws(
    () => restoreDesktopRoutedWorktreeIntent({ version: 2, requested: true } as never),
    /snapshot is invalid/,
  )
})

test('standalone worktree keyword activates intent without partial-word false positives', () => {
  for (const message of ['use a worktree', 'WORKTREE please', 'worktree-based session']) {
    assert.equal(desktopRoutedMessageRequestsWorktree(message), true, message)
    assert.deepEqual(resolveDesktopRoutedWorktreeIntent(createDesktopRoutedWorktreeIntent(false), message), { requested: true })
  }
  for (const message of ['worktrees please', 'myworktree', 'current workspace']) {
    assert.equal(desktopRoutedMessageRequestsWorktree(message), false, message)
  }
  assert.deepEqual(resolveDesktopRoutedWorktreeIntent(createDesktopRoutedWorktreeIntent(true), 'current workspace'), { requested: true })
})

test('metadata encoding carries only boolean intent and creates no operation identity', () => {
  const metadata = encodeDesktopRoutedWorktreeIntentMetadata(
    createDesktopRoutedWorktreeIntent(true),
    { source: 'desktop-v3' },
  )

  assert.deepEqual(metadata, {
    source: 'desktop-v3',
    [DESKTOP_ROUTED_MANAGED_WORKTREE_REQUESTED_METADATA_KEY]: true,
  })
  assert.equal('client_request_id' in metadata, false)
  assert.equal('idempotency_key' in metadata, false)
  assert.equal('worktree_name' in metadata, false)
  assert.equal('branch' in metadata, false)
})
