import test from 'node:test'
import assert from 'node:assert/strict'

import { DesktopV3ArtifactCatalogRefreshCoordinator } from './artifact-catalog-refresh'

test('artifact catalog refresh coordinator refreshes only open catalogs and coalesces event bursts', async () => {
  const coordinator = new DesktopV3ArtifactCatalogRefreshCoordinator()
  let refreshes = 0

  await coordinator.schedule()
  assert.equal(refreshes, 0)

  const lease = coordinator.open(() => { refreshes += 1 })
  await Promise.all([coordinator.schedule(), coordinator.schedule(), coordinator.schedule()])
  assert.equal(refreshes, 1)

  lease.release()
  await coordinator.schedule()
  assert.equal(refreshes, 1)
  assert.deepEqual(coordinator.diagnostics(), { openCatalogs: 0, pending: false })
})

test('artifact catalog refresh coordinator cleanup drops queued closed listeners', async () => {
  const coordinator = new DesktopV3ArtifactCatalogRefreshCoordinator()
  let refreshes = 0
  const lease = coordinator.open(() => { refreshes += 1 })
  const pending = coordinator.schedule()
  lease.release()
  await pending
  assert.equal(refreshes, 0)
  coordinator.dispose()
  assert.deepEqual(coordinator.diagnostics(), { openCatalogs: 0, pending: false })
})

// Requirement: a durable draft change arriving during a request must be fetched.
// Threat: coalescing into the stale in-flight response loses first-create/status updates.
// Authority: the refresh coordinator; a controlled promise proves trailing demand.
test('artifact invalidation during an in-flight refresh gets a trailing fetch', { timeout: 2000 }, async () => {
  const coordinator = new DesktopV3ArtifactCatalogRefreshCoordinator()
  let release!: () => void
  let started!: () => void
  const entered = new Promise<void>((resolve) => { started = resolve })
  const blocked = new Promise<void>((resolve) => { release = resolve })
  let count = 0
  const lease = coordinator.open(async () => { if (++count === 1) { started(); await blocked } })
  const first = coordinator.schedule()
  await entered
  const second = coordinator.schedule()
  release()
  await Promise.all([first, second])
  assert.equal(count, 2)
  lease.release()
  coordinator.dispose()
})
