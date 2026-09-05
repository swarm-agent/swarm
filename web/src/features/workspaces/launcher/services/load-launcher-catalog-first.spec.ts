import test from 'node:test'
import assert from 'node:assert/strict'
import { loadLauncherCatalogFirst } from './load-launcher-catalog-first'
import type { WorkspaceOverviewResponse } from '../types/workspace-overview'
import type { WorkspaceDiscoverEntry } from '../types/workspace'

// Requirement: loadLauncherCatalogFirst must publish usable catalog data without
// waiting for enrichment/discovery, propagate catalog failure, and reject stale
// publications. Deferred promises prove this ordering at the narrow async boundary
// without network, React rendering, timing thresholds, or live daemon state.
function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (error: unknown) => void
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}
const catalog: WorkspaceOverviewResponse = { ok: true, currentWorkspace: null, workspaces: [], discovered: [], swarmTarget: null }
const nextTask = () => new Promise<void>((resolve) => setTimeout(resolve, 0))

function fixture() {
  const details = deferred<WorkspaceOverviewResponse>()
  const discovery = deferred<WorkspaceDiscoverEntry[]>()
  const events: string[] = []
  const errors: unknown[] = []
  let current = true
  const load = {
    loadCatalog: async () => catalog,
    publishCatalog: () => { events.push('catalog') },
    loadDetails: () => { events.push('details-start'); return details.promise },
    publishDetails: () => { events.push('details') },
    discover: () => { events.push('discovery-start'); return discovery.promise },
    publishDiscovery: () => { events.push('discovery') },
    reportBackgroundError: (error: unknown) => { errors.push(error) },
    isCurrent: () => current,
  }
  return { load, details, discovery, events, errors, cancel: () => { current = false } }
}

test('catalog completes before optional requests even start; discovery is independent of stalled details', { timeout: 1000 }, async () => {
  const f = fixture()
  await loadLauncherCatalogFirst(f.load)
  assert.deepEqual(f.events, ['catalog'])
  await nextTask()
  assert.deepEqual(f.events, ['catalog', 'details-start', 'discovery-start'])
  f.discovery.resolve([])
  await nextTask()
  assert.equal(f.events.at(-1), 'discovery')
  f.details.resolve(catalog)
  await nextTask()
  assert.equal(f.events.at(-1), 'details')
})

test('background failures remain visible without rejecting the usable catalog', { timeout: 1000 }, async () => {
  const f = fixture()
  await loadLauncherCatalogFirst(f.load)
  await nextTask()
  const detailError = new Error('details unavailable')
  const discoveryError = new Error('discovery unavailable')
  f.details.reject(detailError)
  f.discovery.reject(discoveryError)
  await nextTask()
  assert.deepEqual(f.errors, [detailError, discoveryError])
  assert.deepEqual(f.events, ['catalog', 'details-start', 'discovery-start'])
})

test('catalog failure rejects without launching optional work or publishing success', { timeout: 1000 }, async () => {
  const f = fixture()
  const error = new Error('catalog unavailable')
  f.load.loadCatalog = async () => { throw error }
  await assert.rejects(loadLauncherCatalogFirst(f.load), error)
  await nextTask()
  assert.deepEqual(f.events, [])
})

test('superseded load cannot publish late details, discovery, or errors', { timeout: 1000 }, async () => {
  const f = fixture()
  await loadLauncherCatalogFirst(f.load)
  await nextTask()
  f.cancel()
  f.details.resolve(catalog)
  f.discovery.reject(new Error('obsolete'))
  await nextTask()
  assert.deepEqual(f.events, ['catalog', 'details-start', 'discovery-start'])
  assert.deepEqual(f.errors, [])
})

test('unmount before deferred startup prevents background requests', { timeout: 1000 }, async () => {
  const f = fixture()
  await loadLauncherCatalogFirst(f.load)
  f.cancel()
  await nextTask()
  assert.deepEqual(f.events, ['catalog'])
})
