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
  stripDesktopRoutedWorktreeDirective,
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

test('only a leading /worktree directive activates routed worktree intent', () => {
  for (const message of ['/worktree fix the sidebar', '  /WORKTREE\nfix the sidebar']) {
    assert.equal(desktopRoutedMessageRequestsWorktree(message), true, message)
    assert.deepEqual(resolveDesktopRoutedWorktreeIntent(createDesktopRoutedWorktreeIntent(false), message), { requested: true })
  }
  for (const message of ['use a worktree', 'worktree please', 'worktrees please', 'myworktree', 'fix /worktree handling']) {
    assert.equal(desktopRoutedMessageRequestsWorktree(message), false, message)
    assert.deepEqual(resolveDesktopRoutedWorktreeIntent(createDesktopRoutedWorktreeIntent(false), message), { requested: false })
  }
  assert.deepEqual(resolveDesktopRoutedWorktreeIntent(createDesktopRoutedWorktreeIntent(true), 'current workspace'), { requested: true })
})

test('/worktree is removed as a routing directive without rewriting ordinary prompts', () => {
  assert.equal(stripDesktopRoutedWorktreeDirective('/worktree fix the sidebar'), 'fix the sidebar')
  assert.equal(stripDesktopRoutedWorktreeDirective('  /WORKTREE\nfix the sidebar'), 'fix the sidebar')
  assert.equal(stripDesktopRoutedWorktreeDirective('/worktree'), '')
  assert.equal(stripDesktopRoutedWorktreeDirective('please fix /worktree handling'), 'please fix /worktree handling')
  assert.equal(stripDesktopRoutedWorktreeDirective('use a worktree'), 'use a worktree')
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
