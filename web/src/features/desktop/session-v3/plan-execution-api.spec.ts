import test from 'node:test'
import assert from 'node:assert/strict'

import { executeDesktopPlanActionAndStartRun } from './plan-execution-api'

const originalFetch = globalThis.fetch

test('accept_checkpoint approves final review without starting a run stream', async () => {
  const calls: Array<{ url: string; body: unknown }> = []
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input)
    calls.push({ url, body: init?.body ? JSON.parse(String(init.body)) : null })
    assert.doesNotMatch(url, /\/run\/stream$/)
    return jsonResponse({ ok: true, plan_id: 'plan-1', action: 'accept_checkpoint', next_action: 'plan_complete' })
  }) as typeof fetch

  try {
    await executeDesktopPlanActionAndStartRun('session-1', { action: 'accept_checkpoint', checkpointId: 'cp-2' })
  } finally {
    globalThis.fetch = originalFetch
  }

  assert.equal(calls.length, 1)
  assert.equal(calls[0]?.url, '/v3/sessions/session-1/plans/execution')
  assert.deepEqual(calls[0]?.body, {
    action: 'accept_checkpoint',
    checkpoint_id: 'cp-2',
  })
})

test('accept_checkpoint ignores stale fresh-run hints from the plan response', async () => {
  const calls: Array<{ url: string; body: unknown }> = []
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input)
    calls.push({ url, body: init?.body ? JSON.parse(String(init.body)) : null })
    assert.doesNotMatch(url, /\/run\/stream$/)
    return jsonResponse({
      ok: true,
      plan_id: 'plan-1',
      action: 'accept_checkpoint',
      checkpoint_id: 'cp-2',
      next_action: 'run_checkpoint_with_fresh_context',
      run_request: { plan_checkpoint_context: { plan_id: 'plan-1', checkpoint_id: 'cp-2', attempt_id: 'cp-2:attempt-1' } },
    })
  }) as typeof fetch

  try {
    await executeDesktopPlanActionAndStartRun('session-1', { action: 'accept_checkpoint', checkpointId: 'cp-1' })
  } finally {
    globalThis.fetch = originalFetch
  }

  assert.equal(calls.length, 1)
  assert.equal(calls[0]?.url, '/v3/sessions/session-1/plans/execution')
})

function jsonResponse(payload: unknown): Response {
  return new Response(JSON.stringify(payload), {
    status: 200,
    headers: { 'Content-Type': 'application/json' },
  })
}
