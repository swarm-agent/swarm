import assert from 'node:assert/strict'
import test from 'node:test'
import { ChildCardHydrationQueue } from './child-card-hydration-queue'
import { backgroundRead } from '../../../app/background-read'
const tick = () => new Promise<void>(resolve => setImmediate(resolve))

// Requirement: visible pending controls must neither wait behind optional reads
// nor issue one HTTP request per card. The production queue is the narrowest
// boundary proving overlap, 8-session batch limits and failure/cancellation
// isolation. These held promises assert ordering, not simulated latency.
test('13 visible owners coalesce and overlap despite four stalled optional reads', async () => {
  const release: Array<() => void> = []
  const optional = Array.from({ length: 4 }, () => backgroundRead(() => new Promise<void>(resolve => release.push(resolve))))
  await tick()
  const batches: string[][] = []
  const finish: Array<() => void> = []
  const queue = new ChildCardHydrationQueue<void>(ids => { batches.push(ids); return new Promise(resolve => finish.push(resolve)) })
  const ready: number[] = []
  const cards = Array.from({ length: 13 }, (_, i) => queue.enqueue(String(i)).then(() => { ready.push(i) }))
  await tick()
  assert.deepEqual(batches.map(ids => ids.length), [8, 5])
  finish[1]()
  await tick()
  assert.deepEqual(ready, [8, 9, 10, 11, 12], 'slow first batch cannot serialize the second')
  assert.equal(batches.length, 2)
  finish[0]()
  await Promise.all(cards)
  release.forEach(resolve => resolve()); await Promise.all(optional)
})

// Requirement: the queue bounds actual in-flight work, rejects canceled routes,
// and continues draining after failures; cancellation must not imply publication.
test('bounded batches survive failure and exclude a canceled route before start', async () => {
  const calls: string[][] = []
  const finish: Array<{ resolve: () => void; reject: (e: Error) => void }> = []
  const queue = new ChildCardHydrationQueue<void>(ids => { calls.push(ids); return new Promise((resolve, reject) => finish.push({ resolve, reject })) }, 2, 2)
  const controller = new AbortController()
  const values = Array.from({ length: 7 }, (_, i) => queue.enqueue(String(i), i === 6 ? controller.signal : undefined).then(() => 'ok', () => 'failed'))
  controller.abort()
  await tick()
  assert.deepEqual(calls, [['0', '1'], ['2', '3']])
  finish[1].reject(new Error('read failed'))
  await tick()
  assert.deepEqual(calls, [['0', '1'], ['2', '3'], ['4', '5']])
  finish[0].resolve(); finish[2].resolve()
  assert.deepEqual(await Promise.all(values), ['ok', 'ok', 'failed', 'failed', 'ok', 'ok', 'failed'])
})

// Requirement: shared batches retain live consumers but never publish canceled
// route resources. The production callback's currentIds fence is checked after
// transport completion (including a transport that ignores cancellation).
test('mixed batch cancellation retains only current consumers', async () => {
  const canceled = new AbortController()
  let finish!: () => void
  let published: string[] = []
  const queue = new ChildCardHydrationQueue<void>(async (_ids, _signal, currentIds) => {
    await new Promise<void>(resolve => { finish = resolve })
    published = currentIds()
  })
  const old = queue.enqueue('old-route', canceled.signal).catch(() => undefined)
  const current = queue.enqueue('current-route')
  await tick(); canceled.abort(); finish()
  await Promise.all([old, current])
  assert.deepEqual(published, ['current-route'])
})
