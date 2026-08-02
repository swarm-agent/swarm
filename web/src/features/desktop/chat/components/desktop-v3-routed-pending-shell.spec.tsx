import assert from 'node:assert/strict'
import test from 'node:test'
import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'

import { DesktopV3RoutedPendingShell } from './desktop-v3-routed-pending-shell'

function render(state: Parameters<typeof DesktopV3RoutedPendingShell>[0]['state'], pendingPrompt = '', error = ''): string {
  return renderToStaticMarkup(
    <DesktopV3RoutedPendingShell
      state={state}
      pendingPrompt={pendingPrompt}
      error={error}
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

test('routing immediately shows a pending chat header, first user message, and status below it', () => {
  const markup = render('routing', 'Route this prompt safely')

  assert.match(markup, /data-pending-state="routing"/)
  assert.match(markup, /data-local-only="true"/)
  assert.match(markup, /data-testid="desktop-v3-pending-chat-header"/)
  assert.match(markup, />New chat</)
  assert.match(markup, />Pending setup</)
  assert.match(markup, /data-testid="desktop-v3-local-pending-prompt"/)
  assert.match(markup, /Route this prompt safely/)
  assert.match(markup, /data-testid="desktop-v3-routing-status"/)
  assert.ok(markup.indexOf('Route this prompt safely') < markup.indexOf('Choosing the setup for this chat'))
  assert.match(markup, /aria-busy="true"/)
  assert.match(markup, /animate-pulse/)
  assert.doesNotMatch(markup, /animate-spin|rounded-full border px-/)
  assert.doesNotMatch(markup, /<button/)
  assert.doesNotMatch(markup, /model|favorite|workspace|branch|Action|Plan/)
})

test('fast unresolved routing still renders the same local message without authoritative details', () => {
  const markup = render('routing', 'Fast route')

  assert.equal((markup.match(/Fast route/g) ?? []).length, 1)
  assert.match(markup, /New chat/)
  assert.match(markup, />Routing…</)
  assert.doesNotMatch(markup, /Choosing setup/)
})

test('failure preserves the message in the same chat and offers retry beside the error', () => {
  const markup = render('failed', 'Keep my prompt', 'Router is unavailable')

  assert.match(markup, /data-pending-state="failed"/)
  assert.match(markup, /Setup needs attention/)
  assert.match(markup, /Keep my prompt/)
  assert.match(markup, /Routing failed/)
  assert.match(markup, /Router is unavailable/)
  assert.match(markup, /<button[^>]*>.*Try again.*<\/button>/)
  assert.doesNotMatch(markup, /aria-busy/)
  assert.doesNotMatch(markup, /model|favorite|workspace|branch|Action|Plan/)
})
