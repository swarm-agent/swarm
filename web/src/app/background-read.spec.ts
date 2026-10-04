import assert from 'node:assert/strict'
import test from 'node:test'
import { backgroundRead } from './background-read'

// Purpose: backgroundRead is the shared optional-card admission boundary. Deferred
// promises prove the four-request cap, cancellation before invocation, and queue
// recovery after failures; this is a scheduling unit test, not a benchmark.
test('optional reads cap concurrency and release capacity after failure or cancellation', async () => {
  let active = 0, peak = 0, started = 0
  const finish: Array<() => void> = []
  const enqueue = (fail = false, signal?: AbortSignal) => backgroundRead(() => {
    started++; active++; peak = Math.max(peak, active)
    return new Promise<void>((resolve, reject) => finish.push(() => {
      active--; if (fail) reject(new Error('optional failed')); else resolve()
    }))
  }, signal)
  const first = Array.from({ length: 4 }, (_, n) => enqueue(n === 0).catch(error => error.message))
  const abort = new AbortController()
  const cancelled = enqueue(false, abort.signal)
  const rejection = assert.rejects(cancelled, { name: 'AbortError' })
  const last = enqueue()
  await new Promise<void>(resolve => setImmediate(resolve))
  assert.equal(started, 4)
  abort.abort()
  await rejection
  finish[0]()
  await new Promise<void>(resolve => setImmediate(resolve))
  assert.equal(started, 5, 'cancelled queued read never invokes its callback')
  for (const done of finish.slice(1)) done()
  assert.equal((await Promise.all(first))[0], 'optional failed')
  await last
  assert.equal(peak, 4)
  assert.equal(active, 0)
})
