import assert from 'node:assert/strict'
import test from 'node:test'
import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { WorkerTaskActivity } from './worker-task-activity'
import { DurableWorkerCount } from '../layout/durable-worker-sidebar'
import { dispatchDesktopV3Cache } from '../state/desktop-v3-cache-store'
import { workerPageKey } from '../state/desktop-workers-state'
import type { WorkerRead, WorkerReadResult, WorkerRecord } from '../state/desktop-workers-api'

// Requirement: accepted workers remain in Tasks even with zero runs; sidebar counts
// are worker records, not today's occurrences or legacy automation sessions.
// Threat: acceptance makes a worker disappear or enablement is called execution.
// Authority: canonical worker pages -> WorkerTaskActivity / DurableWorkerCount.
// SSR with explicit cache fixtures proves presentation only, not network/execution.
test('accepted workers retain identity, tagged runs and durable sidebar counts', () => {
  const accountScopeId = 'account_worker_presentation_fixture'
  const worker: WorkerRecord = { id: 'worker_presentation_fixture', account_scope_id: accountScopeId, name: 'Daily audit', instructions: 'Check dependencies', lifecycle_state: 'active', revision: 2, created_at: 1, updated_at: 2, automations: [] }
  const seed = (input: WorkerRead, data: WorkerReadResult) => {
    const key = workerPageKey(input)
    dispatchDesktopV3Cache({ type: 'workers.begin', key, input, requestId: key })
    dispatchDesktopV3Cache({ type: 'workers.finish', key, requestId: key, generation: 0, data })
  }
  const list: WorkerRead = { kind: 'list', accountScopeId, limit: 10 }
  const count: WorkerRead = { kind: 'list', accountScopeId, limit: 100 }
  const runs: WorkerRead = { kind: 'runs', accountScopeId, workerId: worker.id, limit: 25 }
  try {
    seed(list, { workers: [worker] })
    seed(count, { workers: [worker], next_cursor: 'opaque_more' })
    seed(runs, { runs: [{ id: 'run_fixture', account_scope_id: accountScopeId, worker_id: worker.id, worker_revision: 2, request_source: 'direct', status: 'failed', error: 'Workspace unavailable', session_id: 'session_fixture', created_at: 3 }] })
    const markup = renderToStaticMarkup(<WorkerTaskActivity accountScopeId={accountScopeId} workspaceSlug="demo" />)
    assert.match(markup, /Daily audit/)
    assert.match(markup, /Enabled/)
    assert.match(markup, /durable-worker-run/)
    assert.match(markup, /Workspace unavailable/)
    assert.match(markup, /href="\/demo\/session_fixture"/)
    assert.doesNotMatch(markup, /<form|<textarea|<select/)
    assert.match(renderToStaticMarkup(<DurableWorkerCount accountScopeId={accountScopeId} />), />1\+<\/span>/)
    seed(runs, { runs: [] })
    const emptyRuns = renderToStaticMarkup(<WorkerTaskActivity accountScopeId={accountScopeId} />)
    assert.match(emptyRuns, /Daily audit/)
    assert.match(emptyRuns, /No runs on this page/)
  } finally {
    for (const input of [list, count, runs]) dispatchDesktopV3Cache({ type: 'workers.evict', key: workerPageKey(input) })
  }
})
