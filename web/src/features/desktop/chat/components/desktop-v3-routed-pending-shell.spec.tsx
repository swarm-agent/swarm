import assert from 'node:assert/strict'
import test from 'node:test'
import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'

import { DesktopV3RoutedPendingShell } from './desktop-v3-routed-pending-shell'
import type { WorkspaceEntry } from '../../../workspaces/launcher/types/workspace'

const workspace: WorkspaceEntry = {
  path: '/workspace/swarm-go',
  localWorkspaceBindingId: 'binding-1',
  workspaceName: 'Swarm Go',
  themeId: '',
  directories: ['/workspace/swarm-go'],
  isGitRepo: true,
  sortIndex: 0,
  addedAt: 1,
  updatedAt: 1,
  lastSelectedAt: 1,
  active: true,
  worktreeEnabled: false,
  topologyRoutes: [],
}

function render(
  state: Parameters<typeof DesktopV3RoutedPendingShell>[0]['state'],
  pendingPrompt = '',
  error = '',
  startPath: Parameters<typeof DesktopV3RoutedPendingShell>[0]['startPath'] = 'router',
  showTips = true,
): string {
  return renderToStaticMarkup(
    <DesktopV3RoutedPendingShell
      state={state}
      startPath={startPath}
      pendingPrompt={pendingPrompt}
      error={error}
      onRetry={state === 'failed' ? () => undefined : undefined}
      showTips={showTips}
      onDisableTips={showTips ? () => undefined : undefined}
      workspace={workspace}
    />,
  )
}

test('draft is a neutral Swarm shell without authoritative setup details', () => {
  const markup = render('draft')

  assert.match(markup, /data-pending-state="draft"/)
  assert.match(markup, />Swarm Go</)
  assert.match(markup, /data-testid="desktop-home-tip"/)
  assert.match(markup, /title="Click to disable tips"/)
  assert.match(markup, /aria-label="Click to disable tips"/)
  assert.match(markup, /class="mt-4 [^"]*hover:text-/)
  assert.match(markup, /lucide-lightbulb/)
  assert.match(markup, /stroke="currentColor"/)
  assert.match(markup, /text-\[var\(--app-accent\)\]/)
  assert.doesNotMatch(markup, /text-\[var\(--app-text-muted\)\]/)
  assert.match(markup, /Tip: /)
  assert.doesNotMatch(markup, /💡/)
  assert.match(markup, /max-w-xl/)
  assert.doesNotMatch(markup, /What would you like to work on\?/)
  assert.doesNotMatch(markup, /aria-busy/)
  assert.doesNotMatch(markup, /model|branch/)
})

test('draft omits the subtitle when home tips are disabled', () => {
  const markup = render('draft', '', '', 'router', false)

  assert.match(markup, />Swarm Go</)
  assert.doesNotMatch(markup, /desktop-home-tip|lucide-lightbulb|💡|Tip:/)
  assert.doesNotMatch(markup, /What would you like to work on\?/)
})

test('routing shows the first user message and status without duplicating the parent-owned header', () => {
  const markup = render('routing', 'Route this prompt safely')

  assert.match(markup, /data-pending-state="routing"/)
  assert.match(markup, /data-local-only="true"/)
  assert.doesNotMatch(markup, /<header|desktop-v3-pending-chat-header/)
  assert.match(markup, /data-start-path="router"/)
  assert.match(markup, /data-testid="desktop-v3-local-pending-prompt"/)
  assert.match(markup, /Route this prompt safely/)
  assert.match(markup, /data-testid="desktop-v3-routing-status"/)
  assert.ok(markup.indexOf('Route this prompt safely') < markup.indexOf('Router is choosing the setup for this worktree chat'))
  assert.match(markup, /aria-busy="true"/)
  assert.match(markup, /animate-pulse/)
  assert.doesNotMatch(markup, /animate-spin|rounded-full border px-/)
  assert.doesNotMatch(markup, /<button/)
  assert.doesNotMatch(markup, /model|branch/)
})

test('normal session start never renders Router presentation', () => {
  const markup = render('routing', 'Start this directly', '', 'session')

  assert.match(markup, /data-start-path="session"/)
  assert.doesNotMatch(markup, /<header|desktop-v3-pending-chat-header/)
  assert.match(markup, />Starting…</)
  assert.match(markup, /Creating and starting this chat/)
  assert.doesNotMatch(markup, /Router|Routing|worktree/)
})

test('fast unresolved routing still renders the same local message without authoritative details', () => {
  const markup = render('routing', 'Fast route')

  assert.equal((markup.match(/Fast route/g) ?? []).length, 1)
  assert.doesNotMatch(markup, /<header|desktop-v3-pending-chat-header/)
  assert.match(markup, />Routing…</)
  assert.doesNotMatch(markup, /Choosing setup/)
})

test('failure preserves the message in the same chat and offers retry beside the error', () => {
  const markup = render('failed', 'Keep my prompt', 'Router is unavailable')

  assert.match(markup, /data-pending-state="failed"/)
  assert.doesNotMatch(markup, /<header|desktop-v3-pending-chat-header/)
  assert.match(markup, /Keep my prompt/)
  assert.match(markup, /Router failed/)
  assert.match(markup, /Router is unavailable/)
  assert.match(markup, /<button[^>]*>.*Try again.*<\/button>/)
  assert.doesNotMatch(markup, /aria-busy/)
  assert.doesNotMatch(markup, /model|branch/)
})
