import test from 'node:test'
import assert from 'node:assert/strict'

import { DESKTOP_SELECTED_HYDRATE_RESPONSE_BYTE_BUDGET, DESKTOP_STARTUP_MESSAGE_LIMIT, DESKTOP_STARTUP_SESSION_LIMIT, DESKTOP_V3_BOOTSTRAP_DEFAULT_INPUT, buildDesktopV3BootstrapInput, buildDesktopV3SelectedSessionHydrateInput, countArrayMapItems, postDesktopV3SyncBootstrap, postDesktopV3SyncHydrate } from './desktop-v3-sync-api'
import { snapshotFixture } from './desktop-v3-cache.backend-fixtures'

test('postDesktopV3SyncBootstrap posts metadata-only bounded startup workset payload to bootstrap endpoint', async () => {
  const originalFetch = globalThis.fetch
  const calls: Array<{ url: string; init: RequestInit }> = []
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    calls.push({ url: String(input), init: init ?? {} })
    return new Response(JSON.stringify(snapshotFixture({ messages_by_session: {}, events_by_session: {} })), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    })
  }) as typeof fetch

  try {
    await postDesktopV3SyncBootstrap()
  } finally {
    globalThis.fetch = originalFetch
  }

  assert.equal(calls.length, 1)
  assert.equal(calls[0].url, '/v3/sync/bootstrap')
  assert.equal(calls[0].init.method, 'POST')
  const body = JSON.parse(String(calls[0].init.body))
  assert.equal(body.selector.recent.limit, DESKTOP_STARTUP_SESSION_LIMIT)
  assert.equal(body.selector.attention.pending_permissions, true)
  assert.equal(body.history.mode, 'none')
  assert.equal('max_messages_per_session' in body.history, false)
  assert.equal('manifest_policy' in body.history, false)
  assert.equal(body.resources.messages, false)
  assert.equal(body.resources.events, false)
  assert.equal(body.resources.run_intents, false)
  assert.equal(body.resources.session_view, false)
  assert.equal(body.resources.permission_summaries, true)
  assert.equal(body.resources.active_plan, false)
  assert.equal(body.resources.plan_revisions, false)
  assert.equal(body.include_active, true)
  assert.deepEqual(DESKTOP_V3_BOOTSTRAP_DEFAULT_INPUT.history, { mode: 'none' })
  assert.equal(DESKTOP_V3_BOOTSTRAP_DEFAULT_INPUT.resources.messages, false)
  assert.equal(DESKTOP_V3_BOOTSTRAP_DEFAULT_INPUT.resources.permission_summaries, true)
})


test('buildDesktopV3BootstrapInput does not mix preferred session into recent selector', () => {
  const input = buildDesktopV3BootstrapInput({}, ' session-a ')
  assert.equal(input.selector.kind, 'recent')
  assert.equal(input.selector.global, true)
  assert.deepEqual(input.selector.recent, { limit: DESKTOP_STARTUP_SESSION_LIMIT })
  assert.deepEqual(input.selector.session_ids, undefined)
})


test('buildDesktopV3BootstrapInput only prepends preferred session for session_ids selector', () => {
  const input = buildDesktopV3BootstrapInput({
    selector: { kind: 'session_ids', session_ids: ['session-b'] },
  }, ' session-a ')

  assert.equal(input.selector.kind, 'session_ids')
  assert.deepEqual(input.selector.session_ids, ['session-a', 'session-b'])
})


test('buildDesktopV3SelectedSessionHydrateInput requests one selected session tail manifest and durable active plan state', () => {
  const input = buildDesktopV3SelectedSessionHydrateInput(' session-a ')
  assert.deepEqual(input.session_ids, ['session-a'])
  assert.equal(DESKTOP_STARTUP_MESSAGE_LIMIT, 200)
  assert.equal(DESKTOP_SELECTED_HYDRATE_RESPONSE_BYTE_BUDGET, 512 * 1024)
  assert.deepEqual(
    input.history,
    {
      mode: 'tail',
      max_messages_per_session: DESKTOP_STARTUP_MESSAGE_LIMIT,
      manifest_policy: 'manifest',
    },
  )
  assert.equal(input.resources.session_view, true)
  assert.equal(input.resources.active_plan, true)
})


test('countArrayMapItems totals present arrays and ignores missing values', () => {
  assert.equal(countArrayMapItems({ a: [1, 2], b: undefined, c: [], d: [3] }), 3)
  assert.equal(countArrayMapItems(undefined), 0)
})


test('postDesktopV3SyncHydrate posts exact selected-session bounded tail payload to hydrate endpoint', async () => {
  const originalFetch = globalThis.fetch
  const calls: Array<{ url: string; init: RequestInit }> = []
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    calls.push({ url: String(input), init: init ?? {} })
    return new Response(JSON.stringify(snapshotFixture({ selector: { kind: 'session_ids', session_ids: ['session-b', 'session-a'] } })), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    })
  }) as typeof fetch

  try {
    await postDesktopV3SyncHydrate(buildDesktopV3SelectedSessionHydrateInput('session-b'))
  } finally {
    globalThis.fetch = originalFetch
  }

  assert.equal(calls.length, 1)
  assert.equal(calls[0].url, '/v3/sync/hydrate')
  assert.equal(calls[0].init.method, 'POST')
  assert.deepEqual(JSON.parse(String(calls[0].init.body)), {
    surface: 'desktop',
    session_ids: ['session-b'],
    history: {
      mode: 'tail',
      max_messages_per_session: DESKTOP_STARTUP_MESSAGE_LIMIT,
      manifest_policy: 'manifest',
    },
    resources: {
      messages: true,
      events: false,
      run_intents: true,
      current_run_state: true,
      session_view: true,
      active_plan: true,
      plan_revisions: false,
      permission_summaries: false,
    },
    include_active: true,
  })
})
