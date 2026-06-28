import test from 'node:test'
import assert from 'node:assert/strict'

import {
  acceptDesktopPlanCheckpoint,
  continueDesktopPlanCheckpoint,
  pauseDesktopPlanRun,
  restartDesktopPlanCheckpoint,
  resumeDesktopPlanAutomatic,
  resumeDesktopPlanCheckpointed,
  rewindDesktopPlanCheckpoint,
  startDesktopPlanAutomatic,
  startDesktopPlanCheckpoint,
  startDesktopPlanCheckpointed,
} from './plan-execution-api'

const originalFetch = globalThis.fetch

test('startDesktopPlanAutomatic calls the dedicated start-automatic lifecycle endpoint only', async () => {
  const calls: Array<{ url: string; body: unknown }> = []
  globalThis.fetch = jsonFetch(calls, { ok: true, plan_id: 'plan-1', transition: 'start_plan_automatic' })

  try {
    await startDesktopPlanAutomatic('session-1', 'plan-1', { executionGranularity: 'run_through' })
  } finally {
    globalThis.fetch = originalFetch
  }

  assert.deepEqual(calls, [{
    url: '/v3/sessions/session-1/plan-mode/plans/plan-1/start-automatic',
    body: { execution_granularity: 'run_through' },
  }])
})

test('startDesktopPlanCheckpointed calls the dedicated start-checkpointed lifecycle endpoint only', async () => {
  const calls: Array<{ url: string; body: unknown }> = []
  globalThis.fetch = jsonFetch(calls, { ok: true, plan_id: 'plan-1', transition: 'start_plan_checkpointed' })

  try {
    await startDesktopPlanCheckpointed('session-1', 'plan-1', {
      executionGranularity: 'checkpointed',
      continuationPolicy: 'review_each_checkpoint',
    })
  } finally {
    globalThis.fetch = originalFetch
  }

  assert.deepEqual(calls, [{
    url: '/v3/sessions/session-1/plan-mode/plans/plan-1/start-checkpointed',
    body: { execution_granularity: 'checkpointed', continuation_policy: 'review_each_checkpoint' },
  }])
})

test('checkpoint buttons call checkpoint-specific lifecycle endpoints without run stream chaining', async () => {
  const calls: Array<{ url: string; body: unknown }> = []
  globalThis.fetch = jsonFetch(calls, {
    ok: true,
    plan_id: 'plan-1',
    transition: 'continue_checkpoint',
    checkpoint_id: 'cp-1',
    run_intent: { run_id: 'run-1' },
    run_queued: true,
  })

  try {
    await continueDesktopPlanCheckpoint('session-1', 'cp-1')
  } finally {
    globalThis.fetch = originalFetch
  }

  assert.deepEqual(calls, [{
    url: '/v3/sessions/session-1/plan-mode/checkpoints/cp-1/continue',
    body: {},
  }])
})

test('acceptDesktopPlanCheckpoint ignores stale fresh-run hints from the response', async () => {
  const calls: Array<{ url: string; body: unknown }> = []
  globalThis.fetch = jsonFetch(calls, {
    ok: true,
    plan_id: 'plan-1',
    transition: 'accept_checkpoint',
    checkpoint_id: 'cp-2',
    next_action: 'run_checkpoint_with_fresh_context',
    run_request: { plan_checkpoint_context: { plan_id: 'plan-1', checkpoint_id: 'cp-2', attempt_id: 'cp-2:attempt-1' } },
  })

  try {
    await acceptDesktopPlanCheckpoint('session-1', 'cp-2')
  } finally {
    globalThis.fetch = originalFetch
  }

  assert.deepEqual(calls, [{
    url: '/v3/sessions/session-1/plan-mode/checkpoints/cp-2/accept',
    body: {},
  }])
})

test('current-run lifecycle controls call dedicated endpoints without run stream chaining', async () => {
  const calls: Array<{ url: string; body: unknown }> = []
  globalThis.fetch = jsonFetch(calls, { ok: true, plan_id: 'plan-1', transition: 'resume_automatic' })

  try {
    await pauseDesktopPlanRun('session-1', { planId: 'plan-1' })
    await resumeDesktopPlanAutomatic('session-1', { planId: 'plan-1' })
    await resumeDesktopPlanCheckpointed('session-1', { planId: 'plan-1' })
  } finally {
    globalThis.fetch = originalFetch
  }

  assert.deepEqual(calls, [
    {
      url: '/v3/sessions/session-1/plan-mode/runs/current/pause',
      body: { plan_id: 'plan-1' },
    },
    {
      url: '/v3/sessions/session-1/plan-mode/runs/current/resume-automatic',
      body: { plan_id: 'plan-1' },
    },
    {
      url: '/v3/sessions/session-1/plan-mode/runs/current/resume-checkpointed',
      body: { plan_id: 'plan-1' },
    },
  ])
})

test('checkpoint start reset controls call dedicated endpoints without run stream chaining', async () => {
  const calls: Array<{ url: string; body: unknown }> = []
  globalThis.fetch = jsonFetch(calls, { ok: true, plan_id: 'plan-1', run_intent: { run_id: 'run-1' }, run_queued: true })

  try {
    await startDesktopPlanCheckpoint('session-1', 'cp-1', { planId: 'plan-1' })
    await restartDesktopPlanCheckpoint('session-1', 'cp-1', { planId: 'plan-1' })
    await rewindDesktopPlanCheckpoint('session-1', 'cp-1', { planId: 'plan-1' })
  } finally {
    globalThis.fetch = originalFetch
  }

  assert.deepEqual(calls, [
    {
      url: '/v3/sessions/session-1/plan-mode/checkpoints/cp-1/start',
      body: { plan_id: 'plan-1' },
    },
    {
      url: '/v3/sessions/session-1/plan-mode/checkpoints/cp-1/restart',
      body: { plan_id: 'plan-1' },
    },
    {
      url: '/v3/sessions/session-1/plan-mode/checkpoints/cp-1/rewind',
      body: { plan_id: 'plan-1' },
    },
  ])
})

function jsonFetch(calls: Array<{ url: string; body: unknown }>, payload: Record<string, unknown>): typeof fetch {
  return (async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input)
    calls.push({ url, body: init?.body ? JSON.parse(String(init.body)) : null })
    assert.doesNotMatch(url, /\/plans\/execution$/)
    assert.doesNotMatch(url, /\/run\/stream$/)
    return new Response(JSON.stringify(payload), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    })
  }) as typeof fetch
}
