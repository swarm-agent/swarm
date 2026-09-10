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
