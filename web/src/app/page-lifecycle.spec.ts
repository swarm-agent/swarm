// Purpose: page/route epochs own Git transports and retry timers. Exercise real
// polling helpers with synthetic browser events and late completions, proving
// hide/unmount aborts, foreground restart, stale-publication rejection and cleanup.
import assert from 'node:assert/strict'
import { getEventListeners } from 'node:events'
import { test } from 'node:test'
import { startPagePolling, withPageRequest, type PageLifecycle } from './page-lifecycle'

class PageDocument extends EventTarget { visibilityState = 'visible' }
const flush = async () => { for (let i = 0; i < 12; i++) await Promise.resolve() }

test('hidden mount, foreground, pagehide/pageshow and teardown own distinct epochs', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] })
  const document = new PageDocument(), window = new EventTarget()
  document.visibilityState = 'hidden'
  const signals: AbortSignal[] = []
  const resolve: Array<() => void> = []
  const published: number[] = []
  const stop = startPagePolling(async (signal) => {
    const id = signals.push(signal)
    await new Promise<void>((done) => resolve.push(done))
    if (!signal.aborted) published.push(id)
    return 250
  }, { document, window } as PageLifecycle)
  assert.equal(signals.length, 0)
  document.visibilityState = 'visible'; document.dispatchEvent(new Event('visibilitychange'))
  assert.equal(signals.length, 1)
  window.dispatchEvent(new Event('pagehide'))
  assert.equal(signals[0].aborted, true)
  document.dispatchEvent(new Event('visibilitychange'))
  assert.equal(signals.length, 1)
  window.dispatchEvent(new Event('pageshow'))
  assert.equal(signals.length, 2)
  resolve[0](); resolve[1](); await flush()
  assert.deepEqual(published, [2])
  stop()
  assert.equal(signals[1].aborted, true)
  t.mock.timers.tick(10_000); await flush()
  window.dispatchEvent(new Event('pageshow'))
  assert.equal(signals.length, 2)
  assert.equal(getEventListeners(document, 'visibilitychange').length, 0)
  assert.equal(getEventListeners(window, 'pagehide').length, 0)
  assert.equal(getEventListeners(window, 'pageshow').length, 0)
})

test('rapid route replacement aborts every obsolete poll and has no stale retry', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] })
  const document = new PageDocument(), window = new EventTarget()
  const signals: AbortSignal[] = []
  const finishes: Array<() => void> = []
  const published: string[] = []
  let stop = () => {}
  for (const route of ['a', 'b', 'a']) {
    stop()
    stop = startPagePolling(async (signal) => {
      signals.push(signal)
      await new Promise<void>((resolve) => finishes.push(resolve))
      if (!signal.aborted) published.push(route)
      return 5_000
    }, { document, window } as PageLifecycle)
  }
  assert.deepEqual(signals.map((signal) => signal.aborted), [true, true, false])
  finishes.forEach((finish) => finish()); await flush()
  assert.deepEqual(published, ['a'])
  document.visibilityState = 'hidden'; document.dispatchEvent(new Event('visibilitychange'))
  t.mock.timers.tick(10_000); await flush()
  assert.equal(signals.length, 3)
  stop()
})

test('page-scoped status request aborts on hide and removes listeners', async () => {
  const document = new PageDocument(), window = new EventTarget()
  const oldDocument = Object.getOwnPropertyDescriptor(globalThis, 'document')
  const oldWindow = Object.getOwnPropertyDescriptor(globalThis, 'window')
  Object.defineProperty(globalThis, 'document', { configurable: true, value: document })
  Object.defineProperty(globalThis, 'window', { configurable: true, value: window })
  try {
    let transport!: AbortSignal
    const pending = withPageRequest(async (signal) => { transport = signal; return new Promise(() => {}) })
    window.dispatchEvent(new Event('pagehide'))
    await assert.rejects(pending, { name: 'AbortError' })
    assert.equal(transport.aborted, true)
    assert.equal(getEventListeners(document, 'visibilitychange').length, 0)
    assert.equal(getEventListeners(window, 'pagehide').length, 0)
    document.visibilityState = 'hidden'
    let called = false
    await assert.rejects(withPageRequest(async () => { called = true }), { name: 'AbortError' })
    assert.equal(called, false)
  } finally {
    if (oldDocument) Object.defineProperty(globalThis, 'document', oldDocument)
    else Reflect.deleteProperty(globalThis, 'document')
    if (oldWindow) Object.defineProperty(globalThis, 'window', oldWindow)
    else Reflect.deleteProperty(globalThis, 'window')
  }
})
