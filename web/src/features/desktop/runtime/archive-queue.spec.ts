import assert from 'node:assert/strict'
import test from 'node:test'
import { ArchiveQueue } from './archive-queue'

// Purpose: ArchiveQueue is the narrow mutation scheduler boundary. A burst or
// duplicate click must never exceed four requests, and failure must free a slot.
test('archive lane bounds fanout, deduplicates queued clicks and continues after failure', async () => {
  const queue = new ArchiveQueue()
  const releases: Array<() => void> = []
  let active = 0; let peak = 0; let calls = 0
  const work = () => new Promise<number>(resolve => { calls++; active++; peak = Math.max(peak, active); releases.push(() => { active--; resolve(calls) }) })
  const jobs = Array.from({ length: 8 }, (_, i) => queue.run(String(i), work))
  assert.equal(queue.run('7', work), jobs[7])
  await Promise.resolve()
  assert.equal(calls, 4)
  for (let wave = 0; wave < 2; wave++) { releases.splice(0).forEach(release => release()); for (let i = 0; i < 8; i++) await Promise.resolve() }
  assert.equal(calls, 8)
  await Promise.all(jobs)
  assert.equal(peak, 4)
  await assert.rejects(queue.run('fail', async () => { throw new Error('conflict') }), /conflict/)
  for (let i = 0; i < 8; i++) await Promise.resolve()
  assert.equal(await queue.run('fail', async () => 9), 9)
})
