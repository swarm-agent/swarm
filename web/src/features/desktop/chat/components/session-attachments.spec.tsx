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

// Requirement: SessionAttachmentsView must name working roots rather than count
// access grants. Server rendering is sufficient to assert the collapsed label.
test('collapsed header names the default and working roots without attachment count', () => {
  const items = Array.from({ length: 9 }, (_, index) => ({ id: `row-${index}`, workspace_id: `workspace-${index}`, workspace_name: `Project ${index}`, source_path: `/workspaces/project-${index}`, kind: 'source', attached: true, default: index === 0, availability: 'available' }))
  const html = renderToStaticMarkup(<SessionAttachmentsView {...props} items={items} workingSources={[items[2].source_path]} />)
  const button = html.slice(0, html.indexOf('</button>'))
  assert.match(button, /Working on: Project 0, Project 2/)
  assert.doesNotMatch(button, /9 workspaces|Default:|Project 1|Active workspaces/)
  assert.match(html, /<details><summary[^>]*>Available workspaces/)
})
