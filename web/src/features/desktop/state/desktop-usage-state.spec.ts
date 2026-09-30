// Purpose: reduceUsagePages owns canonical receipt snapshots. Prevent duplicate
// deltas, delayed hydration rollback, cross-account/project leakage and stale
// request resurrection. Pure reducer is the narrowest observable cache boundary.
import test from 'node:test'
import assert from 'node:assert/strict'
import { reduceUsagePages, usageKey, type UsageInput, type UsageScopeTotal } from './desktop-usage-state'
const input: UsageInput = { accountScopeId: 'account-a', scope: { kind: 'task', id: 'task-a', project_id: 'project-a' } }
export function total(revision: number, tokens = 10): UsageScopeTotal {
  return { ...input.scope, revision, total_tokens: tokens, input_tokens: tokens, output_tokens: 0, cache_read_tokens: 0, cache_write_tokens: 0, thinking_tokens: 0, media_cost_usd: 0, media_receipts: 0, coverage: 'observed_receipts_only', catalog_cost_usd: 1, provider_cost_usd: 0, provider_estimate_cost_usd: 0, nominal_subscription_cost_usd: 0, unknown_receipts: 0, free_receipts: 0, subscription_receipts: 0, receipt_count: 1, history_complete: false }
}
test('replacement revisions are idempotent and delayed reads cannot roll back', () => {
  let pages = reduceUsagePages({}, { type: 'usage.begin', input, requestId: 'read' })
  pages = reduceUsagePages(pages, { type: 'usage.snapshot', accountScopeId: 'account-a', totals: [total(3, 20)] })
  pages = reduceUsagePages(pages, { type: 'usage.snapshot', accountScopeId: 'account-a', totals: [total(3, 20), total(1)] })
  pages = reduceUsagePages(pages, { type: 'usage.finish', input, requestId: 'read', generation: 0, usage: total(2), recorded: true })
  assert.equal(pages[usageKey(input)].usage?.total_tokens, 20)
  assert.equal(pages[usageKey(input)].usage?.revision, 3)
})
test('foreign identity and invalidated reads leave old data untouched', () => {
  let pages = reduceUsagePages({}, { type: 'usage.begin', input, requestId: 'read' })
  pages = reduceUsagePages(pages, { type: 'usage.snapshot', accountScopeId: 'account-b', totals: [total(3)] })
  pages = reduceUsagePages(pages, { type: 'usage.snapshot', accountScopeId: 'account-a', totals: [{ ...total(3), project_id: 'foreign' }] })
  assert.equal(pages[usageKey(input)].usage, undefined)
  pages = reduceUsagePages(pages, { type: 'usage.invalidate', accountScopeId: 'account-a' })
  pages = reduceUsagePages(pages, { type: 'usage.finish', input, requestId: 'read', generation: 0, usage: total(2), recorded: true })
  assert.equal(pages[usageKey(input)].usage, undefined)
  assert.equal(pages[usageKey(input)].stale, true)
})
