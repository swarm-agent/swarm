import assert from 'node:assert/strict'
import { afterEach, test } from 'node:test'

import { GIT_REALTIME_TIMEOUT_MS, commitWorkspaceChanges, fetchGitStatus, startGitRealtime, suggestWorkspaceCommitMessage } from './api'

const originalFetch = globalThis.fetch

afterEach(() => {
  globalThis.fetch = originalFetch
})

test('git status APIs normalize null file lists and scope requests to the session', async () => {
  const capturedInputs: string[] = []
  globalThis.fetch = (async (input: RequestInfo | URL) => {
    capturedInputs.push(String(input))
    return new Response(JSON.stringify({
    ok: true,
    workspace_path: '/workspace/project',
    watch_token: 'watch-1',
    status: {
      workspace_path: '/workspace/project',
      has_git: true,
      clean: true,
      files: null,
    },
    }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    })
  }) as typeof fetch

  const status = await fetchGitStatus('/workspace/project', 12, 'session-1')
  const realtime = await startGitRealtime('/workspace/project', 'session-1')

  assert.deepEqual(status.status.files, [])
  assert.deepEqual(realtime.status.files, [])
  assert.deepEqual(capturedInputs, [
    '/v1/workspace/git/status?workspace_path=%2Fworkspace%2Fproject&session_id=session-1&recent_limit=12',
    '/v1/workspace/git/realtime?workspace_path=%2Fworkspace%2Fproject&session_id=session-1',
  ])
})

test('startGitRealtime coalesces duplicate in-flight cache-hit requests', async () => {
  let resolveFetch!: (response: Response) => void
  let calls = 0
  globalThis.fetch = (async () => {
    calls++
    return new Promise<Response>((resolve) => { resolveFetch = resolve })
  }) as typeof fetch

  const first = startGitRealtime('/workspace/project', 'session-1', 'watch-1')
  const second = startGitRealtime('/workspace/project', 'session-1', 'watch-1')
  // Coalescing shares the transport, not cancellation ownership.
  assert.notEqual(first, second)
  assert.equal(calls, 1)

  resolveFetch(new Response(JSON.stringify({
    ok: true,
    workspace_path: '/workspace/project',
    watch_token: 'watch-1',
    status: { workspace_path: '/workspace/project', has_git: true, clean: true, files: [] },
  }), { status: 200, headers: { 'Content-Type': 'application/json' } }))
  await Promise.all([first, second])
})

test('suggestWorkspaceCommitMessage posts repository identity without browser-supplied diff text', async () => {
  let capturedInput: RequestInfo | URL | undefined
  let capturedInit: RequestInit | undefined
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    capturedInput = input
    capturedInit = init
    return new Response(JSON.stringify({
      ok: true,
      workspace_path: '/workspace/project',
      cwd: '/workspace/project',
      message: 'feat: add AI commit message suggestions',
    }), { status: 200, headers: { 'Content-Type': 'application/json' } })
  }) as typeof fetch

  const response = await suggestWorkspaceCommitMessage({
    workspacePath: '/workspace/project',
    sessionId: 'session-1',
  })

  assert.equal(response.message, 'feat: add AI commit message suggestions')
  assert.equal(capturedInput, '/v1/workspace/git/commit/suggestion?session_id=session-1')
  assert.equal(capturedInit?.method, 'POST')
  assert.deepEqual(JSON.parse(String(capturedInit?.body)), {
    workspace_path: '/workspace/project',
    cwd: '/workspace/project',
  })
})

test('commitWorkspaceChanges posts exact manual commit request to git commit API', async () => {
  let capturedInput: RequestInfo | URL | undefined
  let capturedInit: RequestInit | undefined
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    capturedInput = input
    capturedInit = init
    return new Response(JSON.stringify({ ok: true, exit_code: 0 }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    })
  }) as typeof fetch

  await commitWorkspaceChanges({
    workspacePath: '/workspace/project',
    cwd: '/workspace/project',
    message: 'feat: direct manual commit',
    sessionId: 'session-1',
  })

  assert.equal(capturedInput, '/v1/workspace/git/commit?session_id=session-1')
  assert.equal(capturedInit?.method, 'POST')
  assert.equal(new Headers(capturedInit?.headers).get('Content-Type'), 'application/json')
  assert.deepEqual(JSON.parse(String(capturedInit?.body)), {
    workspace_path: '/workspace/project',
    cwd: '/workspace/project',
    message: 'feat: direct manual commit',
    all: true,
  })
})

// Purpose: fetchGitStatus/startGitRealtime must abort obsolete transport while
// retaining other live consumers. Fake transport/time proves cancellation,
// 25s long-poll compatibility, deadline eviction and a successful retry.
test('Git cancellation aborts only after the last live consumer leaves', async () => {
  const signals: AbortSignal[] = []
  globalThis.fetch = async (_input, init) => { signals.push(init!.signal!); return new Promise(() => {}) }
  const a = new AbortController(), b = new AbortController()
  const first = startGitRealtime('/workspace/project', 'session-1', '', a.signal)
  const second = startGitRealtime('/workspace/project', 'session-1', '', b.signal)
  a.abort()
  await assert.rejects(first, { name: 'AbortError' })
  assert.equal(signals.length, 1)
  assert.equal(signals[0].aborted, false)
  b.abort()
  await assert.rejects(second, { name: 'AbortError' })
  assert.equal(signals[0].aborted, true)
  const c = new AbortController()
  const replacement = startGitRealtime('/workspace/project', 'session-1', '', c.signal)
  assert.equal(signals.length, 2)
  c.abort()
  await assert.rejects(replacement, { name: 'AbortError' })
  const d = new AbortController()
  const status = fetchGitStatus('/workspace/project', 12, 'session-1', d.signal)
  d.abort()
  await assert.rejects(status, { name: 'AbortError' })
  assert.equal(signals[2].aborted, true)
})

test('Git long-poll body timeout evicts coalesced state and permits retry', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] })
  let signal!: AbortSignal
  globalThis.fetch = async (_input, init) => {
    signal = init!.signal!
    return new Response(new ReadableStream({ start() {} }))
  }
  const first = startGitRealtime('/workspace/project', 'session-1', 'watch')
  const second = startGitRealtime('/workspace/project', 'session-1', 'watch')
  const rejected = Promise.all([assert.rejects(first, { name: 'TimeoutError' }), assert.rejects(second, { name: 'TimeoutError' })])
  for (let i = 0; i < 20; i++) await Promise.resolve()
  t.mock.timers.tick(25_000)
  assert.equal(signal.aborted, false)
  t.mock.timers.tick(GIT_REALTIME_TIMEOUT_MS - 25_000)
  await rejected
  assert.equal(signal.aborted, true)
  globalThis.fetch = async () => Response.json({ ok: true, watch_token: 'next', status: { files: [] } })
  assert.equal((await startGitRealtime('/workspace/project', 'session-1', 'watch')).watch_token, 'next')
})
