import assert from 'node:assert/strict'
import test from 'node:test'
import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'

import { DesktopV3RoutedPendingShell } from './desktop-v3-routed-pending-shell'

function render(state: Parameters<typeof DesktopV3RoutedPendingShell>[0]['state'], pendingPrompt = ''): string {
  return renderToStaticMarkup(
    <DesktopV3RoutedPendingShell
      state={state}
      pendingPrompt={pendingPrompt}
      onRetry={state === 'failed' ? () => undefined : undefined}
    />,
  )
}

test('draft is a neutral Swarm shell without authoritative setup details', () => {
  const markup = render('draft')

  assert.match(markup, /data-pending-state="draft"/)
  assert.match(markup, />Swarm</)
  assert.match(markup, /What would you like to work on\?/)
  assert.doesNotMatch(markup, /aria-busy/)
  assert.doesNotMatch(markup, /model|favorite|workspace|branch|Action|Plan/)
})

test('worktree-primed keeps the prompt local without claiming a selected branch or setup', () => {
  const markup = render('worktree-primed', 'Refine the desktop shell')

  assert.match(markup, /data-pending-state="worktree-primed"/)
  assert.match(markup, /aria-busy="true"/)
  assert.match(markup, /aria-disabled="true"/)
  assert.match(markup, /Refine the desktop shell/)
  assert.match(markup, /choose the setup when you send it/)
  assert.doesNotMatch(markup, /model|favorite|workspace|branch|Action|Plan/)
})

test('routing announces setup selection, preserves the local prompt, and disables interaction', () => {
  const markup = render('routing', 'Route this prompt safely')

  assert.match(markup, /data-pending-state="routing"/)
  assert.match(markup, /data-local-only="true"/)
  assert.match(markup, /Choosing setup/)
  assert.match(markup, /Route this prompt safely/)
  assert.match(markup, /aria-busy="true"/)
  assert.match(markup, /aria-disabled="true"/)
  assert.match(markup, /animate-spin/)
  assert.doesNotMatch(markup, /<button/)
  assert.doesNotMatch(markup, /model|favorite|workspace|branch|Action|Plan/)
})

test('failure remains local and offers a retry without presenting resolved metadata', () => {
  const markup = render('failed', 'Keep my prompt')

  assert.match(markup, /data-pending-state="failed"/)
  assert.match(markup, /Setup not chosen/)
  assert.match(markup, /Keep my prompt/)
  assert.match(markup, /<button[^>]*>.*Try again.*<\/button>/)
  assert.doesNotMatch(markup, /aria-busy/)
  assert.doesNotMatch(markup, /model|favorite|workspace|branch|Action|Plan/)
})
