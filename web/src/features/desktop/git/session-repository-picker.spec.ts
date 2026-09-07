import assert from 'node:assert/strict'
import { test } from 'node:test'
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { SessionRepositoryPicker } from './session-repository-picker'
import type { SessionRepository } from './types'

// Requirement: unavailable/retained inventory must be visibly distinct from clean
// Git state. The picker is the narrow rendered boundary; no sidebar-node fixture.
test('repository picker renders grouped retained identity and honest stale/error state', () => {
  const row: SessionRepository = { id: 'a', session_id: 'worker', workspace_id: 'one', workspace_name: 'Project', source_path: '/project', workspace_path: '/project/worker', kind: 'worker', attached: false, default: false, branch: 'agent/work', base_commit: '', lifecycle: 'failed', retained: true, availability: 'unavailable', error: 'Authorization expired', files_truncated: false }
  const html = renderToStaticMarkup(createElement(SessionRepositoryPicker, {
    inventory: { items: [row], selectedKey: 'missing', loading: false, stale: true, error: 'Refresh failed', nextCursor: 'opaque', historyCoverage: 'retained' },
    onSelect() {}, onRefresh() {}, onLoadMore() {},
  }))
  for (const text of ['Project', '/project/worker', 'worker', 'failed', 'Retained', 'Authorization expired', 'Stale inventory', 'Refresh failed', 'Selected repository is not', 'History:']) assert.ok(html.includes(text), text)
  assert.match(html, /disabled="">Load more repositories/)
  assert.ok(!html.includes('Clean working tree'))
})
