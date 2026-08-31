import assert from 'node:assert/strict'
import test from 'node:test'
import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'

import { DesktopRoutedWorktreePrime } from './desktop-routed-worktree-prime'

test('managed worktree status is mandatory and has no toggle state', () => {
  const markup = renderToStaticMarkup(<DesktopRoutedWorktreePrime />)

  assert.match(markup, /data-testid="desktop-routed-worktree-prime"/)
  assert.match(markup, /Managed worktree isolation active/)
  assert.doesNotMatch(markup, /aria-pressed|data-worktree-requested|Use managed worktree|Disable managed worktree/)
  assert.doesNotMatch(markup, /<button|<input|<select/)
})
