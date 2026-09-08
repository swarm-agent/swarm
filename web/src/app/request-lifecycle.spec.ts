// Purpose: bounded browser reads must settle and release timers/listeners on
// stalls/cancellation, without aborting another shared consumer. These unit tests
// exercise withRequestDeadline/waitForSignal/SharedRequestPool at their owning
// boundary with fake time and deliberately non-cooperative adapters.
import assert from 'node:assert/strict'
import { getEventListeners } from 'node:events'
import { test } from 'node:test'
import { SharedRequestPool, waitForSignal, withRequestDeadline } from './request-lifecycle'

const never = () => new Promise<never>(() => {})

test('deadline aborts a non-cooperative operation and removes caller listeners', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] })
  const caller = new AbortController()
  let transport!: AbortSignal
  const pending = withRequestDeadline((signal) => { transport = signal; return never() }, 100, caller.signal)
  const rejected = assert.rejects(pending, { name: 'TimeoutError' })
  t.mock.timers.tick(100)
  await rejected
  assert.equal(transport.aborted, true)
  assert.equal(caller.signal.aborted, false)
  assert.equal(getEventListeners(caller.signal, 'abort').length, 0)
  assert.equal(getEventListeners(transport, 'abort').length, 0)
})

test('success and failure clear deadline timers; caller abort reason is preserved', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] })
  let transport!: AbortSignal
  assert.equal(await withRequestDeadline(async (signal) => { transport = signal; return 7 }, 100), 7)
  t.mock.timers.tick(100)
  assert.equal(transport.aborted, false)
  await assert.rejects(withRequestDeadline(async (signal) => { transport = signal; throw new Error('offline') }, 100), /offline/)
  t.mock.timers.tick(100)
  assert.equal(transport.aborted, false)
  const caller = new AbortController()
  const reason = new Error('route left')
  const pending = withRequestDeadline(() => never(), 100, caller.signal)
  caller.abort(reason)
  await assert.rejects(pending, (error) => error === reason)
  assert.equal(getEventListeners(caller.signal, 'abort').length, 0)
  let called = false
  await assert.rejects(withRequestDeadline(async () => { called = true }, 100, caller.signal), (error) => error === reason)
  assert.equal(called, false)
})

test('waiter cancellation detaches immediately from a never-settling shared source', async () => {
  const caller = new AbortController()
  const pending = waitForSignal(never(), caller.signal)
  caller.abort()
  await assert.rejects(pending, { name: 'AbortError' })
  assert.equal(getEventListeners(caller.signal, 'abort').length, 0)
})

test('shared ownership keeps live consumers and protects replacement from stale cleanup', async () => {
  const pool = new SharedRequestPool<number>()
  const a = new AbortController(), b = new AbortController()
  let calls = 0
  const transports: AbortSignal[] = []
  const resolves: Array<(value: number) => void> = []
  const operation = (signal: AbortSignal) => {
    calls++
    transports.push(signal)
    return new Promise<number>((resolve) => resolves.push(resolve))
  }
  const first = pool.run('same', operation, a.signal)
  const second = pool.run('same', operation, b.signal)
  a.abort()
  await assert.rejects(first, { name: 'AbortError' })
  assert.equal(transports[0].aborted, false)
  assert.equal(calls, 1)
  b.abort()
  await assert.rejects(second, { name: 'AbortError' })
  assert.equal(transports[0].aborted, true)
  const replacement = pool.run('same', operation)
  resolves[0](1)
  await Promise.resolve(); await Promise.resolve()
  const joined = pool.run('same', operation)
  assert.equal(calls, 2)
  resolves[1](2)
  assert.deepEqual(await Promise.all([replacement, joined]), [2, 2])
  assert.equal(getEventListeners(a.signal, 'abort').length, 0)
  assert.equal(getEventListeners(b.signal, 'abort').length, 0)
})

test('synchronous adapter failure does not pin shared state', async () => {
  const pool = new SharedRequestPool<number>()
  await assert.rejects(pool.run('key', () => { throw new Error('adapter failed') }), /adapter failed/)
  assert.equal(await pool.run('key', async () => 1), 1)
})
