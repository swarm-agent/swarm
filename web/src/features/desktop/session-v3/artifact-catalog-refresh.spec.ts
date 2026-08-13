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
