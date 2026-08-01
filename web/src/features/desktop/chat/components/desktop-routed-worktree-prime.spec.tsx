import assert from 'node:assert/strict'
import test from 'node:test'
import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'

import { DesktopRoutedWorktreePrime } from './desktop-routed-worktree-prime'

function render(requested: boolean, disabled = false): string {
  return renderToStaticMarkup(
    <DesktopRoutedWorktreePrime
      requested={requested}
      disabled={disabled}
      onRequestedChange={() => undefined}
    />,
  )
}

test('worktree prime renders removable boolean intent without naming route authority', () => {
  const markup = render(false)

  assert.match(markup, /data-testid="desktop-routed-worktree-prime"/)
  assert.match(markup, /data-worktree-requested="false"/)
  assert.match(markup, /aria-pressed="false"/)
  assert.match(markup, /Use managed worktree/)
  assert.doesNotMatch(markup, /branch|worktree name|workspace path|agent\//i)
  assert.doesNotMatch(markup, /<input|<select/)
})

test('worktree prime toggled presentation remains boolean and can be disabled', () => {
  const markup = render(true, true)

  assert.match(markup, /data-worktree-requested="true"/)
  assert.match(markup, /aria-pressed="true"/)
  assert.match(markup, /Use current workspace/)
  assert.match(markup, /disabled=""/)
  assert.doesNotMatch(markup, /branch|worktree name|workspace path|agent\//i)
})
