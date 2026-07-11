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
