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

test('resolved Action header shows canonical favorite and model without a mode label', () => {
  const markup = renderToStaticMarkup(
    <DesktopV3ChatHeader
      title="Resolved conversation"
      workspaceName="Workspace"
      mode="auto"
      favoriteName="Action Fast"
      modelLabel="GPT-5.6 Codex"
    />,
  )

  assert.match(markup, /data-testid="desktop-v3-resolved-model"/)
  assert.match(markup, /Action Fast · GPT-5.6 Codex/)
  assert.doesNotMatch(markup, />auto</)
  assert.doesNotMatch(markup, /desktop-v3-plan-mode-badge/)
})

test('resolved Plan header adds a read-only Plan badge and never invents unresolved model state', () => {
  const markup = renderToStaticMarkup(
    <DesktopV3ChatHeader title="Plan conversation" workspaceName="Workspace" mode="plan" />,
  )

  assert.match(markup, /data-testid="desktop-v3-plan-mode-badge"/)
  assert.match(markup, /aria-label="Plan mode"/)
  assert.doesNotMatch(markup, /desktop-v3-resolved-model/)
  assert.doesNotMatch(markup, /Action mode|auto mode/i)
})
