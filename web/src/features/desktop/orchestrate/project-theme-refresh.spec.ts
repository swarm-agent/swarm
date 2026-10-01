import test from 'node:test'
import assert from 'node:assert/strict'
import { createProjectThemeRefresh } from './project-theme-refresh'

// Purpose: durable project events must not cause overlapping catalog reads or discard a
// newer assignment while the first read is in flight. Boundary: project update subscription.
test('project theme invalidations coalesce in flight and ignore unrelated projects', async () => {
  let reads = 0
  let release!: () => void
  const first = new Promise<void>((resolve) => { release = resolve })
  const refresh = createProjectThemeRefresh('selected', async () => {
    reads++
    if (reads === 1) await first
  })
  refresh.invalidate('other')
  assert.equal(reads, 0)
  refresh.invalidate('selected')
  refresh.invalidate('selected')
  assert.equal(reads, 1)
  release()
  await new Promise<void>((resolve) => setImmediate(resolve))
  assert.equal(reads, 2)
  refresh.dispose()
  refresh.invalidate('selected')
  assert.equal(reads, 2)
})
