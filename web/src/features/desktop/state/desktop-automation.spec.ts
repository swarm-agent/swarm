import assert from 'node:assert/strict'
import test from 'node:test'
import { automationReadURL, validateAutomationMutation } from './desktop-automation-api'
import { reduceAutomationPages, automationPageKey, type AutomationPages } from './desktop-automation-state'
import { realtimeFrameToActions } from './desktop-v3-cache-wire'

// Purpose: /v3/automations uses bounded, scoped reads and exact mutation revisions.
// Prevent accidental broad reads or unguarded execution at the narrow client boundary;
// server authorization remains authoritative and is not simulated by these tests.
test('automation client preserves opaque cursor and rejects unbounded reads and unguarded run', () => {
  const input = { workspace_id: 'workspace', action: 'search' as const, cursor: 'opaque+/=?', limit: 50 }
  const url = new URL(automationReadURL(input), 'http://localhost')
  assert.equal(url.searchParams.get('cursor'), input.cursor)
  assert.equal(url.searchParams.get('workspace_id'), 'workspace')
  assert.throws(() => automationReadURL({ ...input, limit: 51 }))
  assert.throws(() => automationReadURL({ ...input, workspace_id: '' }))
  assert.throws(() => validateAutomationMutation({ action: 'run', workspace_id: 'workspace', id: 'automation', mutation_id: 'mutation', expected_revision: 0 }))
  assert.throws(() => validateAutomationMutation({ action: 'pause', workspace_id: 'workspace', id: 'automation', mutation_id: 'mutation', expected_revision: Number.MAX_SAFE_INTEGER + 1 }))
})

// Purpose: reduceAutomationPages owns hydration postconditions. Old requests and
// invalidated responses must not replace visible data; failed refresh stays stale.
// Pure reducer tests are the narrowest layer for deterministic race ordering.
test('automation reducer preserves loaded data across refresh, errors and late results', () => {
  const input = { workspace_id: 'workspace', action: 'list' as const }
  const key = automationPageKey(input)
  const data = { records: [], next_cursor: 'next' }
  let pages: AutomationPages = {}
  pages = reduceAutomationPages(pages, { type: 'automation.begin', key, input, requestId: 'first' })
  pages = reduceAutomationPages(pages, { type: 'automation.finish', key, requestId: 'first', generation: 0, data })
  pages = reduceAutomationPages(pages, { type: 'automation.begin', key, input, requestId: 'second' })
  assert.equal(pages[key].data, data)
  pages = reduceAutomationPages(pages, { type: 'automation.invalidate', workspaceId: 'unrelated' })
  assert.equal(pages[key].generation, 0)
  pages = reduceAutomationPages(pages, { type: 'automation.invalidate', workspaceId: 'workspace' })
  pages = reduceAutomationPages(pages, { type: 'automation.finish', key, requestId: 'second', generation: 0, data: { records: [] } })
  assert.equal(pages[key].data, data)
  assert.equal(pages[key].stale, true)
  pages = reduceAutomationPages(pages, { type: 'automation.begin', key, input, requestId: 'third' })
  const previous = pages
  assert.equal(reduceAutomationPages(pages, { type: 'automation.finish', key, requestId: 'second', generation: 1, data: {} }), previous)
  pages = reduceAutomationPages(pages, { type: 'automation.finish', key, requestId: 'third', generation: 1, error: 'denied' })
  assert.equal(pages[key].data, data)
  assert.equal(pages[key].error, 'denied')
  assert.equal(pages[key].stale, true)
  pages = reduceAutomationPages(pages, { type: 'automation.evict', key })
  assert.deepEqual(reduceAutomationPages(pages, { type: 'automation.finish', key, requestId: 'third', generation: 1, data }), {})
})

// Purpose: canonical realtimeFrameToActions must recognize metadata-free backend
// automation.updated frames without fabricating a session or rejecting valid V3;
// malformed protocol remains rejected at the same parsing boundary.
test('automation event uses canonical control cursor path and rejects wrong protocol', () => {
  const frame = { protocol: 'v3.realtime', protocol_version: 1, kind: 'automation.updated', endpoint_cursor: 'opaque-cursor' }
  assert.deepEqual(realtimeFrameToActions(frame), [{ type: 'realtime.control', frame }])
  assert.throws(() => realtimeFrameToActions({ ...frame, protocol_version: 2 }))
})
