import assert from 'node:assert/strict'
import test from 'node:test'
import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'

import { DesktopComposerPlanToggle } from './desktop-composer-plan-toggle'

function render(active: boolean, disabled = false): string {
  return renderToStaticMarkup(
    <DesktopComposerPlanToggle
      active={active}
      disabled={disabled}
      onActiveChange={() => undefined}
    />,
  )
}

test('Plan toggle mirrors Worktree on/off semantics and active styling', () => {
  const inactive = render(false)
  const active = render(true)

  assert.match(inactive, /data-plan-active="false"/)
  assert.match(inactive, /aria-pressed="false"/)
  assert.match(inactive, /Enable plan mode/)
  assert.match(inactive, /border-transparent/)
  assert.doesNotMatch(inactive, /ring-1 ring-\[var\(--app-border-accent\)\]/)

  assert.match(active, /data-plan-active="true"/)
  assert.match(active, /aria-pressed="true"/)
  assert.match(active, /Disable plan mode/)
  assert.match(active, /border-\[var\(--app-border-accent\)\]/)
  assert.match(active, /ring-1 ring-\[var\(--app-border-accent\)\]/)
})

test('Plan toggle exposes disabled mutation state without faking a selection', () => {
  const markup = render(false, true)
  assert.match(markup, /disabled=""/)
  assert.match(markup, /data-plan-active="false"/)
})
