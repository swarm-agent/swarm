import assert from 'node:assert/strict'
import test from 'node:test'
import { DesktopAutomationRuntime } from './desktop-automations'
import { automationPageKey, reduceAutomationPages, type AutomationPages } from '../state/desktop-automation-state'
import type { AutomationResponse, AutomationRead, AutomationMutation } from '../state/desktop-automation-api'

// Purpose: DesktopAutomationRuntime coalesces durable invalidation during hydration
// without polling and never applies a cross-workspace response. Injected promises
// exercise the actual runtime/state boundary without transport or clock dependence.
test('in-flight event burst repairs once; unrelated chatter does not refresh', async () => {
  let pages: AutomationPages = {}
  const pending: ((response: AutomationResponse) => void)[] = []
  let reads = 0
  const runtime = new DesktopAutomationRuntime({
    pages: () => pages,
    dispatch: action => { pages = reduceAutomationPages(pages, action) },
    read: (_input: AutomationRead) => { reads++; return new Promise<AutomationResponse>(resolve => pending.push(resolve)) },
    mutate: async (_input: AutomationMutation) => { throw new Error('denied') },
  })
  const input = { action: 'list' as const, workspace_id: 'workspace' }
  const key = automationPageKey(input)
  const lease = runtime.acquire(input)
  runtime.acceptFrame({ kind: 'event' })
  assert.equal(reads, 1)
  runtime.acceptFrame({ kind: 'automation.updated' })
  runtime.acceptFrame({ kind: 'automation.updated' })
  assert.equal(reads, 1)
  pending.shift()!({ records: [], next_cursor: 'obsolete' })
  await lease.ready
  assert.equal(reads, 2)
  assert.equal(pages[key].data, undefined)
  const repaired = runtime.refresh(input)
  pending.shift()!({ records: [], next_cursor: 'current' })
  await repaired
  assert.equal(pages[key].data?.next_cursor, 'current')
  const mismatch = runtime.refresh(input)
  pending.shift()!({ record: { scope: { account_id: 'account', workspace_id: 'other' }, automation_id: 'automation', kind: 'definition', id: 'automation', revision: 1, subject_id: 'user', actor: 'user', written_at: 1 } })
  await mismatch
  assert.equal(pages[key].data?.next_cursor, 'current')
  assert.equal(pages[key].error, 'Automation response scope mismatch')
  const readsBeforeRelease = reads
  lease.release()
  runtime.invalidate()
  assert.equal(reads, readsBeforeRelease)
  assert.equal(pages[key], undefined)
})

// Purpose: final release must invalidate request ownership independently of page
// generation. Deferred reads at DesktopAutomationRuntime/reduceAutomationPages
// prove StrictMode-style setup/cleanup/setup cannot reuse a released promise,
// publish obsolete data/errors, or clear a newer in-flight slot. No browser,
// timers, network, or transport cancellation is needed to prove this boundary.
for (const obsoleteFirst of [true, false]) {
  for (const obsoleteFails of [true, false]) {
    test(`remount hydrates independently: obsoleteFirst=${obsoleteFirst}, obsoleteFails=${obsoleteFails}`, { timeout: 1000 }, async () => {
      let pages: AutomationPages = {}
      const pending: { resolve: (data: AutomationResponse) => void; reject: (error: Error) => void }[] = []
      const runtime = new DesktopAutomationRuntime({
        pages: () => pages,
        dispatch: action => { pages = reduceAutomationPages(pages, action) },
        read: (_input: AutomationRead) => new Promise<AutomationResponse>((resolve, reject) => pending.push({ resolve, reject })),
        mutate: async (_input: AutomationMutation) => { throw new Error('unexpected mutation') },
      })
      const input = { action: 'list' as const, workspace_id: 'workspace' }
      const key = automationPageKey(input)
      const old = runtime.acquire(input)
      const generation = pages[key].generation
      old.release()
      assert.equal(pages[key], undefined)
      const current = runtime.acquire(input)
      old.release() // Repeated cleanup must not release the new lease.
      assert.equal(pending.length, 2)
      assert.notEqual(current.ready, old.ready)
      assert.equal(pages[key].generation, generation) // Deliberate generation collision.
      const finishObsolete = async () => {
        if (obsoleteFails) pending[0].reject(new Error('obsolete failure'))
        else pending[0].resolve({ records: [], next_cursor: 'obsolete' })
        await old.ready
      }
      if (obsoleteFirst) {
        await finishObsolete()
        assert.equal(pages[key].loading, true)
        assert.equal(pages[key].data, undefined)
        assert.equal(pages[key].error, undefined)
        assert.equal(runtime.refresh(input), current.ready)
      }
      pending[1].resolve({ records: [], next_cursor: 'fresh' })
      await current.ready
      if (!obsoleteFirst) await finishObsolete()
      assert.equal(pages[key].data?.next_cursor, 'fresh')
      assert.equal(pages[key].loading, false)
      assert.equal(pages[key].stale, false)
      assert.equal(pages[key].error, undefined)
      assert.equal(pending.length, 2)

      // A shared consumer retains ownership until its own final cleanup.
      const shared = runtime.acquire(input)
      current.release()
      assert.ok(pages[key])
      shared.release()
      assert.equal(pages[key], undefined)
      pending[2].resolve({ records: [], next_cursor: 'released' })
      await shared.ready
      assert.equal(pages[key], undefined)
      runtime.invalidate()
      assert.equal(pending.length, 3)
    })
  }
}

// Requirement: ordinary-chat proposals and page controls share one cache authority.
// Threat: successful writes leave a sibling consumer stale, or failed CAS writes
// fabricate grants/retry automatically. The injected runtime/reducer boundary is
// the narrowest deterministic layer for these postconditions (not live socket proof).
test('chat and page mutations hydrate shared state and never retry failed approval', async () => {
  let pages: AutomationPages = {}
  let revision = 1
  let writes = 0
  const reads: string[] = []
  const runtime = new DesktopAutomationRuntime({
    pages: () => pages,
    dispatch: action => { pages = reduceAutomationPages(pages, action) },
    read: async input => { reads.push(input.workspace_id); return { records: [], next_cursor: String(revision) } },
    mutate: async input => {
      writes++
      if (input.action === 'approve') throw new Error('Automation changed; refresh before retrying.')
      revision++
      return { fresh: true }
    },
  })
  const input = { action: 'list' as const, workspace_id: 'workspace' }
  const foreign = { ...input, workspace_id: 'other' }
  const chat = runtime.acquire(input)
  const page = runtime.acquire(input)
  const other = runtime.acquire(foreign)
  await Promise.all([chat.ready, page.ready, other.ready])
  reads.length = 0
  await runtime.mutate({ action: 'pause', workspace_id: 'workspace', id: 'automation', mutation_id: 'pause', expected_revision: 1 })
  await runtime.refresh(input)
  assert.deepEqual(reads, ['workspace'])
  assert.equal(pages[automationPageKey(input)].data?.next_cursor, '2')
  assert.equal(pages[automationPageKey(foreign)].data?.next_cursor, '1')
  await assert.rejects(runtime.mutate({ action: 'approve', workspace_id: 'workspace', id: 'automation', mutation_id: 'approve', expected_revision: 1, policy_sha256: 'stale' }), /changed/)
  await runtime.refresh(input)
  assert.equal(writes, 2)
  assert.equal(pages[automationPageKey(input)].data?.approval, undefined)
  assert.equal(pages[automationPageKey(input)].data?.next_cursor, '2')
  chat.release()
  assert.ok(pages[automationPageKey(input)])
  page.release(); other.release()
  assert.deepEqual(pages, {})
})
