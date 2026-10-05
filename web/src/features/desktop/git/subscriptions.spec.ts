import assert from 'node:assert/strict'
import test from 'node:test'
import { subscribeGit, type GitWatchNotice, type GitWatchSelector } from './subscriptions'

const flush = async () => { for (let i = 0; i < 40; i++) await Promise.resolve() }

// Requirement: subscribeGit must admit at most 256 selectors on one transport,
// expose only overflow as unavailable, and treat scoped setup rejection as data,
// not a reconnect trigger. This transport-layer test checks reader/timer/abort
// behavior; real HTTP authorization/native ownership is covered by the Go fixture.
test('bounded transport isolates selector errors and overflow without reconnect loops', { timeout: 5000 }, async t => {
  t.mock.timers.enable({ apis: ['setTimeout'] })
  const repositories: GitWatchSelector[] = Array.from({ length: 258 }, (_, index) => ({ workspace_path: `/fixture/${index}`, branch: 'dev' }))
  let starts = 0
  let cancelled = 0
  let signal: AbortSignal | null | undefined
  let stream!: ReadableStreamDefaultController<Uint8Array>
  t.mock.method(globalThis, 'fetch', async (_url: unknown, init: RequestInit) => {
    starts++
    signal = init.signal
    assert.deepEqual(JSON.parse(String(init.body)).repositories, repositories.slice(0, 256))
    return new Response(new ReadableStream<Uint8Array>({
      start(controller) { stream = controller }, cancel() { cancelled++ },
    }), { headers: { 'Content-Type': 'text/event-stream' } })
  })
  const notices: GitWatchNotice[] = []
  const stop = subscribeGit(repositories, notice => notices.push(notice))
  const send = (notice: GitWatchNotice) => stream.enqueue(new TextEncoder().encode(`data: ${JSON.stringify(notice)}\n\n`))
  try {
    await flush()
    assert.deepEqual(notices.map(notice => notice.index), [256, 257])
    assert.ok(notices.every(notice => notice.kind === 'lost' && notice.reason_code === 'watch_capacity'))
    send({ index: 0, kind: 'ready' })
    send({ index: 1, kind: 'lost', reason_code: 'selector_unavailable', error: 'selector unavailable' })
    await flush()
    for (let i = 0; i < 10; i++) { t.mock.timers.tick(60_000); await flush() }
    assert.equal(starts, 1)
    assert.equal(notices.length, 4, 'failed selector did not invalidate admitted healthy selectors')
    send({ index: 0, kind: 'changed' })
    await flush()
    assert.deepEqual(notices.at(-1), { index: 0, kind: 'changed' })
    stop()
    assert.equal(signal?.aborted, true)
    stream.close() // browser fetch abort normally settles the reader
    await flush()
    assert.equal(cancelled, 0, 'closed reader has already released underlying source')
    t.mock.timers.tick(60_000); await flush()
    assert.equal(starts, 1)
  } finally { stop() }
})

// Requirement: permanent HTTP authorization/request errors happen before SSE and
// must not become indefinite whole-board setup loops. subscribeGit owns retry
// classification; a 403 plus elapsed timers proves no retry or silent freshness.
test('permanent HTTP rejection fences once and waits for explicit reconnect', { timeout: 5000 }, async t => {
  t.mock.timers.enable({ apis: ['setTimeout'] })
  let starts = 0
  t.mock.method(globalThis, 'fetch', async () => { starts++; return new Response('forbidden', { status: 403 }) })
  const notices: GitWatchNotice[] = []
  const stop = subscribeGit([{ workspace_path: '/fixture', branch: 'dev' }], notice => notices.push(notice))
  try {
    await flush()
    t.mock.timers.tick(3_600_000); await flush()
    assert.equal(starts, 1)
    assert.equal(notices.length, 1)
    assert.equal(notices[0].kind, 'lost')
  } finally { stop() }
})
