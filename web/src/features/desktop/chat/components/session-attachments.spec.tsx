import assert from 'node:assert/strict'
import test from 'node:test'
import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { SessionAttachmentsView, type SessionAttachmentsViewProps } from './session-attachments'

const props: SessionAttachmentsViewProps = { items: [], loading: false, stale: false, error: false, more: false, onMore() {}, onRefresh() {} }
// Requirement: incomplete/error reads cannot imply an empty successful inventory.
// SessionAttachmentsView owns these accessible states; rendered output is the
// narrowest assertion layer (browser fixtures separately prove layout/effects).
test('attachment header distinguishes loading, unavailable, incomplete and empty inventories', () => {
  const render = (patch: Partial<SessionAttachmentsViewProps>) => renderToStaticMarkup(<SessionAttachmentsView {...props} {...patch} />)
  assert.match(render({ loading: true }), /Loading workspaces/)
  assert.doesNotMatch(render({ loading: true }), /No attached workspaces/)
  assert.match(render({ error: true }), /Workspaces unavailable/)
  assert.doesNotMatch(render({ error: true }), /No attached workspaces/)
  assert.match(render({ more: true }), /Load more workspaces/)
  assert.doesNotMatch(render({ more: true }), /No attached workspaces/)
  assert.match(render({}), /No attached workspaces/)
  assert.match(render({ stale: true }), /displayed entries may no longer be attached/)
})
