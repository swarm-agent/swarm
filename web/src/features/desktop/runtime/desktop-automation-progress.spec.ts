import assert from 'node:assert/strict'
import test from 'node:test'
import { DesktopAutomationRuntime } from './desktop-automations'
import { automationPageKey, reduceAutomationPages, type AutomationPages } from '../state/desktop-automation-state'
import type { AutomationProgress, AutomationResponse } from '../state/desktop-automation-api'

// Requirement: canonical progress follows durable invalidation/reconnect and rejects
// mismatched identity/timezone. Threat: foreign or obsolete progress presented as
// current daily totals. Injected transport at the runtime/reducer is the narrowest proof.
test('progress shares hydration, repairs reconnect, and rejects foreign projections', { timeout: 1000 }, async () => {
  let pages: AutomationPages = {}
  let reads = 0
  const input = { workspace_id: 'workspace', id: 'automation', action: 'progress' as const, display_timezone: 'UTC' }
  let response: AutomationResponse = { progress: { automation_id: 'automation', display_timezone: 'UTC' } as AutomationProgress }
  const runtime = new DesktopAutomationRuntime({
    pages: () => pages, dispatch: action => { pages = reduceAutomationPages(pages, action) },
    read: async () => { reads++; return response }, mutate: async () => { throw new Error('unexpected mutation') },
  })
  const left = runtime.acquire(input)
  const right = runtime.acquire(input)
  await Promise.all([left.ready, right.ready])
  assert.equal(reads, 1)
  runtime.acceptFrame({ kind: 'rehydrate.required' })
  await runtime.refresh(input)
  assert.equal(reads, 2)
  const key = automationPageKey(input)
  const previous = pages[key].data
  for (const bad of [{ automation_id: 'foreign', display_timezone: 'UTC' }, { automation_id: 'automation', display_timezone: 'America/New_York' }]) {
    response = { progress: bad as AutomationProgress }
    await runtime.refresh(input)
    assert.equal(pages[key].error, 'Automation progress scope mismatch')
    assert.equal(pages[key].stale, true)
    assert.equal(pages[key].data, previous)
  }
  left.release(); right.release()
  assert.equal(pages[key], undefined)
})
