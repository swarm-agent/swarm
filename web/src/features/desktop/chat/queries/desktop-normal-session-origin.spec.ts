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
    if (url.startsWith('/v1/sessions/') && url.endsWith('/run/stream')) {
      return new Response(JSON.stringify({ ok: true, run_id: 'run-1', status: 'accepted' }), {
        status: 202,
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

    const runCall = calls.find((entry) => String(entry.input).includes('/v1/sessions/session-normal/run/stream'))
    assert.ok(runCall, 'expected run start request')
    const body = JSON.parse(String(runCall?.init?.body ?? '{}')) as Record<string, unknown>
    assert.equal(body.background, false)
    assert.equal(body.target_kind, '')
    assert.equal(body.target_name, '')
    assert.equal(new Headers(runCall?.init?.headers).get('X-Swarm-Token'), null)
    assert.equal(runCall?.init?.credentials, 'same-origin')
  })
})

test('commit session start keeps explicit background commit lineage', async () => {
  const { startSessionRun } = await import('./chat-queries')

  await withFetchStub(async (calls) => {
    await startSessionRun({
      sessionId: 'session-commit',
      prompt: 'commit',
      background: true,
      targetKind: 'background',
      targetName: 'commit',
    })

    const runCall = calls.find((entry) => String(entry.input).includes('/v1/sessions/session-commit/run/stream'))
    assert.ok(runCall, 'expected commit run start request')
    const body = JSON.parse(String(runCall?.init?.body ?? '{}')) as Record<string, unknown>
    assert.equal(body.background, true)
    assert.equal(body.target_kind, 'background')
    assert.equal(body.target_name, 'commit')
  })
})

test('subagent-targeted desktop session start sends targeted lineage metadata', async () => {
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

    const runCall = calls.find((entry) => String(entry.input).includes('/v1/sessions/session-subagent/run/stream'))
    assert.ok(runCall, 'expected subagent run start request')
    const body = JSON.parse(String(runCall?.init?.body ?? '{}')) as Record<string, unknown>
    assert.equal(body.background, false)
    assert.equal(body.prompt, 'investigate desktop mentions')
    assert.equal(body.agent_name, 'swarm')
    assert.equal(body.target_kind, 'subagent')
    assert.equal(body.target_name, 'explorer')
  })
})
