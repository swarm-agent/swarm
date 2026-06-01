import test from 'node:test'
import assert from 'node:assert/strict'

async function withFetchStub(
  run: (calls: Array<{ input: RequestInfo | URL; init?: RequestInit }>) => Promise<void>,
): Promise<void> {
  const calls: Array<{ input: RequestInfo | URL; init?: RequestInit }> = []
  const originalFetch = globalThis.fetch
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    calls.push({ input, init })
    const url = String(input)
    if (url === '/v1/auth/attach/token') {
      throw new Error('unexpected legacy attach-token bootstrap')
    }
    if (url.startsWith('/v2/sessions/') && url.endsWith('/run/stream')) {
      return new Response(JSON.stringify({ ok: true, run_id: 'run-1', status: 'accepted' }), {
        status: 202,
        headers: { 'Content-Type': 'application/json' },
      })
    }
    if (url.startsWith('/v2/sessions/') && url.endsWith('/mode')) {
      const body = JSON.parse(String(init?.body ?? '{}')) as Record<string, unknown>
      const mode = String(body.mode ?? '').trim()
      if (mode !== 'plan' && mode !== 'auto') {
        return new Response(JSON.stringify({ error: `invalid mode ${JSON.stringify(mode)}` }), {
          status: 400,
          headers: { 'Content-Type': 'application/json' },
        })
      }
      return new Response(JSON.stringify({ ok: true, mode }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    }
    throw new Error(`unexpected fetch: ${url}`)
  }) as typeof fetch

  try {
    await run(calls)
  } finally {
    globalThis.fetch = originalFetch
  }
}

test('normal desktop session start does not send background-targeted metadata', async () => {
  const { startSessionRun } = await import('./chat-queries')

  await withFetchStub(async (calls) => {
    await startSessionRun({
      sessionId: 'session-normal',
      prompt: 'hello',
      agentName: 'swarm',
      background: false,
    })

    const runCall = calls.find((entry) => String(entry.input).includes('/v2/sessions/session-normal/run/stream'))
    assert.ok(runCall, 'expected run start request')
    const body = JSON.parse(String(runCall?.init?.body ?? '{}')) as Record<string, unknown>
    assert.equal(body.background, false)
    assert.equal(Object.hasOwn(body, 'target_kind'), false)
    assert.equal(Object.hasOwn(body, 'target_name'), false)
    assert.equal(Object.hasOwn(body, 'target_swarm_id'), false)
    assert.equal(Object.hasOwn(body, 'session_id'), false)
    assert.equal(Object.hasOwn(body, 'tool_scope'), false)
    assert.equal(Object.hasOwn(body, 'execution_context'), false)
    assert.equal(new Headers(runCall?.init?.headers).get('X-Swarm-Token'), null)
    assert.equal(runCall?.init?.credentials, 'same-origin')
  })
})

test('primary commit session start uses native v2 authority without request-time routing overrides', async () => {
  const { startSessionRun } = await import('./chat-queries')

  await withFetchStub(async (calls) => {
    await startSessionRun({
      sessionId: 'session-commit',
      prompt: 'commit',
      background: true,
      targetKind: 'background',
      targetName: 'memory',
      executionContext: { workspace_path: '/must/not/be/sent' },
    })

    const runCall = calls.find((entry) => String(entry.input).includes('/v2/sessions/session-commit/run/stream'))
    assert.ok(runCall, 'expected commit run start request')
    const body = JSON.parse(String(runCall?.init?.body ?? '{}')) as Record<string, unknown>
    assert.equal(body.background, true)
    assert.equal(Object.hasOwn(body, 'target_kind'), false)
    assert.equal(Object.hasOwn(body, 'target_name'), false)
    assert.equal(Object.hasOwn(body, 'execution_context'), false)
  })
})

test('subagent-targeted primary desktop session start uses native v2 authority', async () => {
  const { startSessionRun } = await import('./chat-queries')

  await withFetchStub(async (calls) => {
    await startSessionRun({
      sessionId: 'session-subagent',
      prompt: 'investigate desktop mentions',
      agentName: 'swarm',
      background: false,
      targetKind: 'subagent',
      targetName: 'explorer',
    })

    const runCall = calls.find((entry) => String(entry.input).includes('/v2/sessions/session-subagent/run/stream'))
    assert.ok(runCall, 'expected subagent run start request')
    const body = JSON.parse(String(runCall?.init?.body ?? '{}')) as Record<string, unknown>
    assert.equal(body.background, false)
    assert.equal(body.prompt, 'investigate desktop mentions')
    assert.equal(body.agent_name, 'explorer')
    assert.equal(Object.hasOwn(body, 'target_kind'), false)
    assert.equal(Object.hasOwn(body, 'target_name'), false)
  })
})

test('session mode update only sends runtime plan or auto modes', async () => {
  const { updateSessionMode } = await import('./chat-queries')

  await withFetchStub(async (calls) => {
    const resolved = await updateSessionMode('session-memory', 'auto')

    assert.equal(resolved, 'auto')
    const modeCall = calls.find((entry) => String(entry.input).includes('/v2/sessions/session-memory/mode'))
    assert.ok(modeCall, 'expected session mode request')
    const body = JSON.parse(String(modeCall?.init?.body ?? '{}')) as Record<string, unknown>
    assert.equal(body.mode, 'auto')
  })
})

test('direct primary desktop session run can select memory as the agent without routing overrides', async () => {
  const { startSessionRun } = await import('./chat-queries')

  await withFetchStub(async (calls) => {
    await startSessionRun({
      sessionId: 'session-memory',
      prompt: 'please update your AGENTS.md notes',
      agentName: 'memory',
      background: false,
    })

    const runCall = calls.find((entry) => String(entry.input).includes('/v2/sessions/session-memory/run/stream'))
    assert.ok(runCall, 'expected memory run start request')
    const body = JSON.parse(String(runCall?.init?.body ?? '{}')) as Record<string, unknown>
    assert.equal(body.background, false)
    assert.equal(body.agent_name, 'memory')
    assert.equal(Object.hasOwn(body, 'target_kind'), false)
    assert.equal(Object.hasOwn(body, 'target_name'), false)
  })
})
