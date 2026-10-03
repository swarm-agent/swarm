import test from 'node:test'
import assert from 'node:assert/strict'
import { archiveProjectTask, projectTaskArchiveQueue } from './project-task-archive'
import { archiveQueue } from './archive-queue'

const flush = () => new Promise<void>(resolve => setImmediate(resolve))

// Purpose: production task lane + archiveProjectTask own the four-request cap,
// duplicate reservation and receipt checking. Injected HTTP responses establish
// deterministic concurrency/failure contracts, never benchmark performance.
test('task archive lane caps parallel POSTs, deduplicates, releases failed slots and is independent of designs', async () => {
  const releases: Array<() => void> = []
  const design = Array.from({ length: 4 }, (_, i) => archiveQueue.run(`design-${i}`, () => new Promise<void>(resolve => releases.push(resolve))))
  await flush()
  let active = 0, peak = 0
  const calls: Array<{ url: string; revision: number; finish: () => void }> = []
  const request = (async (url: string, init: RequestInit) => {
    assert.equal(init.method, 'POST')
    assert.match(url, /^\/v3\/projects\/project\/tasks\/\d+\/archive$/)
    const revision = JSON.parse(init.body as string).revision
    active++; peak = Math.max(peak, active)
    const id = url.split('/').at(-2)!
    return new Promise((resolve, reject) => calls.push({ url, revision, finish: () => {
      active--
      if (id === '2') reject(new Error('409 stale revision'))
      else resolve({ task: { id, revision: revision + 1, archived: true } })
    } }))
  }) as any
  const jobs = Array.from({ length: 9 }, (_, i) => projectTaskArchiveQueue.run(`task-${i}`, () => archiveProjectTask('project', { id: String(i), revision: 4 }, () => true, request)))
  const duplicate = projectTaskArchiveQueue.run('task-0', async () => { throw new Error('duplicate executed') })
  assert.equal(duplicate, jobs[0])
  const result = Promise.allSettled(jobs)
  await flush()
  assert.equal(calls.length, 4)
  for (let next = 0; next < 9;) {
    const end = calls.length
    for (; next < end; next++) calls[next].finish()
    await flush()
  }
  const results = await result
  assert.equal(peak, 4)
  assert.equal(active, 0)
  assert.equal(results.filter(result => result.status === 'fulfilled').length, 8)
  assert.equal(results[2].status, 'rejected')
  assert.ok(calls.every(call => call.revision === 4))
  releases.forEach(release => release())
  await Promise.all(design)
  const retry = await projectTaskArchiveQueue.run('task-2', () => archiveProjectTask('project', { id: '2', revision: 5 }, () => true,
    (async () => ({ task: { id: '2', revision: 6, archived: true } })) as any))
  assert.equal(retry?.revision, 6)
})

// Purpose: archiveProjectTask rejects stale/invalid receipts and scope changes;
// this transport boundary must never issue DELETE/stop or accept partial success.
test('archive rejects missing revisions, changed scope and invalid acknowledgement', async () => {
  let calls = 0
  const unexpected = (async () => { calls++; throw new Error('unexpected request') }) as any
  await assert.rejects(archiveProjectTask('p', { id: 'a' }, () => true, unexpected), /revision/)
  await assert.rejects(archiveProjectTask('p', { id: 'a', revision: 1 }, () => false, unexpected), /changed/)
  assert.equal(calls, 0)
  for (const task of [undefined, { id: 'other', revision: 2, archived: true }, { id: 'a', revision: 1, archived: true }, { id: 'a', revision: 2, archived: false }]) {
    await assert.rejects(archiveProjectTask('p', { id: 'a', revision: 1 }, () => true,
      (async () => ({ task })) as any), /Invalid task archive receipt/)
  }
  let current = true
  const ignored = await archiveProjectTask('p', { id: 'a', revision: 1 }, () => current,
    (async () => { current = false; return { task: { id: 'a', revision: 2, archived: true } } }) as any)
  assert.equal(ignored, undefined)
})

// Purpose: a project/account epoch change must cancel queued archive work before
// transport and suppress acknowledgements for in-flight work. Exercise the real
// bounded lane and helper, rather than asserting source strings.
test('scope change prevents queued work from sending and suppresses active receipts', async () => {
  let current = true, calls = 0
  const release: Array<() => void> = []
  const request = (async (url: string) => {
    calls++
    return new Promise(resolve => release.push(() => resolve({ task: { id: url.split('/').at(-2), revision: 2, archived: true } })))
  }) as any
  const jobs = Array.from({ length: 6 }, (_, i) => projectTaskArchiveQueue.run(`scope-${i}`, () => archiveProjectTask('p', { id: String(i), revision: 1 }, () => current, request)))
  const done = Promise.allSettled(jobs)
  await flush()
  assert.equal(calls, 4)
  current = false
  release.forEach(resolve => resolve())
  const results = await done
  assert.equal(calls, 4)
  assert.ok(results.slice(0, 4).every(result => result.status === 'fulfilled' && result.value === undefined))
  assert.ok(results.slice(4).every(result => result.status === 'rejected'))
})
