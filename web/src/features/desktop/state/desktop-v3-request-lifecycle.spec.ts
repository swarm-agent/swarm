// Purpose: prove the production V3 bootstrap/hydrate transport budgets release
// controller shared state, preserve prior cache on failure and reject late data.
// This narrow fake-fetch test uses the real bootstrap controller and wire fixture;
// it does not claim daemon/browser or transport-security coverage.
import assert from 'node:assert/strict'
import { test } from 'node:test'
import { STARTUP_REQUEST_TIMEOUT_MS } from '../../../app/request-lifecycle'
import { bootstrapDesktopV3SidebarMetadataOnly, resetDesktopV3BootstrapControllerForTests } from './desktop-v3-bootstrap-controller'
import { postDesktopV3SyncHydrate, buildDesktopV3SelectedSessionHydrateInput } from './desktop-v3-sync-api'
import { snapshotFixture } from './desktop-v3-cache.backend-fixtures'

const flush = async () => { for (let i = 0; i < 20; i++) await Promise.resolve() }

test('V3 bootstrap body timeout releases shared state without applying late data', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] })
  resetDesktopV3BootstrapControllerForTests()
  const original = globalThis.fetch
  let signal!: AbortSignal
  let finish!: (value: string) => void
  let calls = 0, applied = 0
  const deps = { dispatch: () => {}, dispatchBatch: () => { applied++ } }
  globalThis.fetch = async (_input, init) => {
    calls++; signal = init!.signal!
    return { ok: true, status: 200, text: () => new Promise<string>((resolve) => { finish = resolve }) } as Response
  }
  try {
    const first = bootstrapDesktopV3SidebarMetadataOnly(deps)
    const second = bootstrapDesktopV3SidebarMetadataOnly(deps)
    assert.equal(first, second)
    const rejected = assert.rejects(first, { name: 'TimeoutError' })
    await flush()
    t.mock.timers.tick(STARTUP_REQUEST_TIMEOUT_MS)
    await rejected
    assert.equal(calls, 1)
    assert.equal(signal.aborted, true)
    assert.equal(applied, 0)
    globalThis.fetch = async () => Response.json(snapshotFixture())
    await bootstrapDesktopV3SidebarMetadataOnly(deps)
    assert.equal(applied, 1)
    finish(JSON.stringify(snapshotFixture()))
    await flush()
    assert.equal(applied, 1)
  } finally { globalThis.fetch = original; resetDesktopV3BootstrapControllerForTests() }
})

test('selected hydrate aborts caller cancellation and timeout, then retries', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] })
  const original = globalThis.fetch
  const input = buildDesktopV3SelectedSessionHydrateInput('session-a')
  let signal!: AbortSignal
  globalThis.fetch = async (_input, init) => { signal = init!.signal!; return new Promise(() => {}) }
  try {
    const caller = new AbortController()
    const cancelled = postDesktopV3SyncHydrate(input, caller.signal)
    caller.abort()
    await assert.rejects(cancelled, { name: 'AbortError' })
    assert.equal(signal.aborted, true)
    const stalled = postDesktopV3SyncHydrate(input)
    const rejected = assert.rejects(stalled, { name: 'TimeoutError' })
    t.mock.timers.tick(STARTUP_REQUEST_TIMEOUT_MS)
    await rejected
    assert.equal(signal.aborted, true)
    globalThis.fetch = async () => Response.json(snapshotFixture())
    assert.ok(await postDesktopV3SyncHydrate(input))
  } finally { globalThis.fetch = original }
})
