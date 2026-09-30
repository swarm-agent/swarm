import React from 'react'
import assert from 'node:assert/strict'
import test from 'node:test'
import { renderToStaticMarkup } from 'react-dom/server'
import { WorkerChangeHistory, WorkerRunRow } from './worker-hub'
import { dispatchDesktopV3Cache } from '../state/desktop-v3-cache-store'
import { workerPageKey } from '../state/desktop-workers-state'
import type { WorkerRead, WorkerRecord } from '../state/desktop-workers-api'

// Requirement: durable change/run history links exact revisions and keeps opaque
// pagination bounded. Threat: current jobs imply historical runs used today's
// revision, or errors hide behind empty history. Cache-backed SSR at history/row
// is the narrowest observable layer; it does not prove backend execution.
test('change history shows durable revisions, paging and run revision linkage', () => {
  const worker: WorkerRecord = { id: 'history-worker', account_scope_id: 'history-account', name: 'Stable', instructions: '', lifecycle_state: 'active', revision: 4, created_at: 1, updated_at: 4 }
  const input: WorkerRead = { kind: 'history', accountScopeId: worker.account_scope_id, workerId: worker.id, limit: 25 }
  const key = workerPageKey(input)
  try {
    dispatchDesktopV3Cache({ type: 'workers.begin', key, input, requestId: key })
    dispatchDesktopV3Cache({ type: 'workers.finish', key, requestId: key, generation: 0, data: { revisions: [{ worker_id: worker.id, account_scope_id: worker.account_scope_id, revision: 3, committed_at: 3, change_summary: 'Added hello job', worker }], next_cursor: 'opaque-older' } })
    const html = renderToStaticMarkup(<WorkerChangeHistory worker={worker} accountScopeId={worker.account_scope_id} />)
    assert.match(html, /Revision 3/)
    assert.match(html, /Added hello job/)
    assert.match(html, /Oldest first/)
    assert.match(html, /Later changes/)
    const row = renderToStaticMarkup(<WorkerRunRow worker={worker} accountScopeId={worker.account_scope_id} run={{ id: 'run', worker_id: worker.id, account_scope_id: worker.account_scope_id, worker_revision: 2, automation_revision: 1, request_source: 'schedule', status: 'failed', error: 'Execution failed', created_at: 2 }} />)
    assert.match(row, /Worker revision 2 · job revision 1/)
    assert.match(row, /Execution failed/)
  } finally { dispatchDesktopV3Cache({ type: 'workers.evict', key }) }
})
