import assert from 'node:assert/strict'
import test from 'node:test'
import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'

import { DesktopRoutedWorktreePrime } from './desktop-routed-worktree-prime'

function render(requested: boolean, disabled = false, readOnly = false): string {
  return renderToStaticMarkup(
    <DesktopRoutedWorktreePrime
      requested={requested}
      disabled={disabled}
      readOnly={readOnly}
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
  assert.match(markup, /border-transparent/)
  assert.doesNotMatch(markup, /ring-1 ring-\[var\(--app-border-accent\)\]/)
  assert.doesNotMatch(markup, /branch|worktree name|workspace path|agent\//i)
  assert.doesNotMatch(markup, /<input|<select/)
})

test('worktree prime toggled presentation remains boolean and can be disabled', () => {
  const markup = render(true, true)

  assert.match(markup, /data-worktree-requested="true"/)
  assert.match(markup, /aria-pressed="true"/)
  assert.match(markup, /Disable managed worktree/)
  assert.match(markup, /disabled=""/)
  assert.match(markup, /border-\[var\(--app-border-accent\)\]/)
  assert.match(markup, /ring-1 ring-\[var\(--app-border-accent\)\]/)
  assert.doesNotMatch(markup, /branch|worktree name|workspace path|agent\//i)
})

test('durable worktree state stays visibly active and read-only after creation', () => {
  const markup = render(true, false, true)

  assert.match(markup, /data-worktree-requested="true"/)
  assert.match(markup, /aria-readonly="true"/)
  assert.match(markup, /Managed worktree active/)
  assert.match(markup, /border-\[var\(--app-border-accent\)\]/)
})
