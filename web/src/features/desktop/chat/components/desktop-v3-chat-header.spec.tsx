import assert from 'node:assert/strict'
import test from 'node:test'
import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'

import { DesktopV3ChatHeader, type DesktopV3ChatHeaderSessionActions } from './desktop-v3-chat-header'

const actions: DesktopV3ChatHeaderSessionActions = {
  pinned: false,
  canPin: true,
  onTogglePinned: () => {},
  onArchive: () => {},
  onRename: async () => {},
}

test('existing-session header exposes accessible rename controls on desktop and mobile', () => {
  const markup = renderToStaticMarkup(
    <DesktopV3ChatHeader title="Current title" workspaceName="Workspace" sessionActions={actions} />,
  )

  assert.equal((markup.match(/aria-label="Rename conversation: Current title"/g) ?? []).length, 2)
  assert.match(markup, /click to rename/)
})

test('new-session header title remains non-editable', () => {
  const markup = renderToStaticMarkup(
    <DesktopV3ChatHeader title="New conversation" workspaceName="Workspace" />,
  )

  assert.doesNotMatch(markup, /Rename conversation:/)
  assert.doesNotMatch(markup, /Conversation title/)
})

test('resolved header shows the Git branch and canonical model without a mode label', () => {
  const markup = renderToStaticMarkup(
    <DesktopV3ChatHeader
      title="Resolved conversation"
      workspaceName="Workspace"
      branchName="agent/fix-header"
      modelLabel="GPT-5.6 Codex"
    />,
  )

  assert.match(markup, /data-testid="desktop-v3-git-branch"/)
  assert.match(markup, /agent\/fix-header/)
  assert.doesNotMatch(markup, /Runtime:/)
  assert.match(markup, /data-testid="desktop-v3-resolved-model"/)
  assert.match(markup, /GPT-5.6 Codex/)
  assert.doesNotMatch(markup, /desktop-v3-plan-mode-badge/)
})

test('header places branch, workspaces, and provider model on the second row together', () => {
  const markup = renderToStaticMarkup(
    <DesktopV3ChatHeader
      title="Header layout conversation"
      workspaceName="Frontend Workspace"
      branchName="agent/header-workspaces-layout"
      modelLabel="Google Gemini 3.8 Flash"
    />,
  )
  assert.doesNotMatch(markup, /Runtime:/)
  const branchIndex = markup.indexOf('data-testid="desktop-v3-git-branch"')
  const workspaceIndex = markup.indexOf('data-testid="session-workspace-row"')
  const modelIndex = markup.indexOf('data-testid="desktop-v3-resolved-model"')
  assert.ok(branchIndex !== -1, 'branch exists')
  assert.ok(workspaceIndex !== -1, 'workspace exists')
  assert.ok(modelIndex !== -1, 'model exists')
  assert.ok(branchIndex < workspaceIndex, 'branch comes before workspace on row 2')
  assert.ok(workspaceIndex < modelIndex, 'workspace comes before model on row 2')
})

test('video session header exposes a bidirectional Studio switch', () => {
  const sessionMarkup = renderToStaticMarkup(
    <DesktopV3ChatHeader title="Video session" workspaceName="Workspace" studioMode="session" onToggleStudioMode={() => {}} />,
  )
  const studioMarkup = renderToStaticMarkup(
    <DesktopV3ChatHeader title="Video session" workspaceName="Workspace" studioMode="studio" onToggleStudioMode={() => {}} />,
  )

  assert.match(sessionMarkup, /aria-label="Switch to Video Studio"/)
  assert.match(sessionMarkup, />Studio</)
  assert.match(sessionMarkup, /sm:inline-flex/)
  assert.match(sessionMarkup, /sm:hidden/)
  assert.match(studioMarkup, /aria-label="Switch to session mode"/)
  assert.match(studioMarkup, />Chat</)
})

test('ordinary session header does not expose the Studio switch', () => {
  const markup = renderToStaticMarkup(
    <DesktopV3ChatHeader title="Ordinary session" workspaceName="Workspace" />,
  )

  assert.doesNotMatch(markup, /desktop-v3-video-studio-toggle/)
})

test('header omits unresolved branch placeholders and the plan indicator', () => {
  const markup = renderToStaticMarkup(
    <DesktopV3ChatHeader title="Plan conversation" workspaceName="Workspace" branchName="undefined" />,
  )

  assert.doesNotMatch(markup, /desktop-v3-git-branch/)
  assert.doesNotMatch(markup, /desktop-v3-plan-mode-badge/)
  assert.doesNotMatch(markup, /aria-label="Plan mode"/)
  assert.doesNotMatch(markup, />undefined</)
})

// Requirement: mobile workspace identity belongs below the title, without runtime
// or model metadata. DesktopV3ChatHeader owns placement; SSR guards the single
// attachment-control mount and row contents, not computed browser geometry.
test('session workspace row contains one attachment control without runtime metadata', () => {
  const markup = renderToStaticMarkup(
    <DesktopV3ChatHeader title="Conversation" workspaceName="Project" sessionId="fixture-session" branchName="agent/header-fix" modelLabel="Example model" />,
  )
  const row = markup.slice(markup.indexOf('data-testid="session-workspace-row"'))
  const button = row.slice(0, row.indexOf('</button>'))
  assert.match(button, /aria-haspopup="dialog"/)
  assert.doesNotMatch(button, /Working on|Runtime:|agent\/header-fix|Example model/)
  assert.equal((markup.match(/aria-haspopup="dialog"/g) ?? []).length, 1)
  assert.ok(markup.indexOf('</h1>') < markup.indexOf('data-testid="session-workspace-row"'))
  assert.match(markup, /data-testid="desktop-v3-git-branch">agent\/header-fix/)
})

// Requirement: On mobile, the header title and workspace identity must be constrained
// with overflow-hidden and truncate so long titles or workspace identities do not overflow
// horizontally into the new chat / session button or actions.
test('mobile header constrains title and container with overflow protection before the new chat button', () => {
  const longTitle = 'A very long conversation title that must truncate cleanly and not run into the new chat button on mobile screens'
  const editableMarkup = renderToStaticMarkup(
    <DesktopV3ChatHeader
      title={longTitle}
      workspaceName="Frontend Workspace"
      sessionId="test-session-123"
      onNewSession={() => {}}
      onOpenChats={() => {}}
      sessionActions={actions}
    />,
  )

  // Header has the new session button
  assert.match(editableMarkup, /aria-label="New session"/)

  // Title container has overflow-hidden preventing spill into action controls
  assert.match(editableMarkup, /class="[^"]*min-w-0 flex-1 overflow-hidden[^"]*"/)

  // Mobile h1 has flex, min-w-0, and overflow-hidden
  assert.match(editableMarkup, /<h1 class="[^"]*flex min-w-0 items-center overflow-hidden[^"]*"/)

  // Rename button on mobile has max-w-full and truncate
  assert.match(editableMarkup, /<button[^>]*class="[^"]*max-w-full min-w-0 truncate[^"]*"[^>]*title="A very long conversation title/)

  // Read-only title (no rename) also has block, max-w-full, min-w-0, and truncate
  const readOnlyMarkup = renderToStaticMarkup(
    <DesktopV3ChatHeader
      title={longTitle}
      workspaceName="Frontend Workspace"
      onNewSession={() => {}}
      onOpenChats={() => {}}
    />,
  )
  assert.match(readOnlyMarkup, /<span class="[^"]*block max-w-full min-w-0 truncate[^"]*" title="A very long conversation title/)
})

