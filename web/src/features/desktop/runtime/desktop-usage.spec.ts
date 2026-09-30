// Purpose: DesktopUsageRuntime/reduceUsagePages own authenticated usage demand,
// bounded reads and CAS. Prevent account-switch leakage, foreign reads, stale
// saves, redundant chatter fetches and in-flight invalidation loss. Injected
// hermetic transport is the narrowest orchestration layer, not live telemetry.
import test from 'node:test'
import assert from 'node:assert/strict'
import { DesktopUsageRuntime } from './desktop-usage'
import { reduceUsagePages, usageKey, type UsageInput, type UsagePages, type UsageScopeTotal, type WorkerBudgetStatus } from '../state/desktop-usage-state'
const input: UsageInput = { accountScopeId: 'acct', scope: { kind: 'worker', id: 'worker-test' } }
const usage = (revision = 1): UsageScopeTotal => ({ ...input.scope, revision, total_tokens: 10, input_tokens: 10, output_tokens: 0, cache_read_tokens: 0, cache_write_tokens: 0, thinking_tokens: 0, media_cost_usd: 0, media_receipts: 0, coverage: 'observed_receipts_only', catalog_cost_usd: 0, provider_cost_usd: 1, provider_estimate_cost_usd: 0, nominal_subscription_cost_usd: 0, unknown_receipts: 0, free_receipts: 0, subscription_receipts: 0, receipt_count: 1, history_complete: false })
function harness() {
  let pages: UsagePages = {}, account = 'acct', saves = 0
  const reads: Array<(data: { usage: UsageScopeTotal; recorded: boolean; budget?: WorkerBudgetStatus }) => void> = []
  const runtime = new DesktopUsageRuntime({ account: () => account, pages: () => pages, dispatch: a => { pages = reduceUsagePages(pages, a) }, read: () => new Promise(resolve => reads.push(resolve)), save: async () => { saves++ } })
  return { runtime, reads, pages: () => pages, saves: () => saves, account: (next: string) => { account = next } }
}
test('event burst coalesces repair on completion and unrelated chatter does not read', { timeout: 1000 }, async () => {
  const h = harness(), release = h.runtime.acquire(input)
  await Promise.resolve()
  const first = h.runtime.refresh(input)
  h.runtime.acceptFrame({ kind: 'event' })
  h.runtime.acceptFrame({ kind: 'worker.updated', account_scope_id: 'foreign', worker_id: input.scope.id })
  assert.equal(h.reads.length, 1)
  h.runtime.invalidate('acct', input.scope.id); h.runtime.invalidate('acct', input.scope.id)
  h.reads[0]({ usage: usage(), recorded: true }); await first; await Promise.resolve()
  assert.equal(h.reads.length, 2)
  assert.equal(h.pages()[usageKey(input)].usage, undefined)
  h.reads[1]({ usage: usage(2), recorded: true }); await h.runtime.refresh(input)
  assert.equal(h.pages()[usageKey(input)].usage?.revision, 2)
  release(); assert.deepEqual(h.pages(), {})
})
test('account switch and foreign scope cannot publish a result', { timeout: 1000 }, async () => {
  for (const switchAccount of [true, false]) {
    const h = harness(), release = h.runtime.acquire(input)
    await Promise.resolve()
    const flight = h.runtime.refresh(input)
    if (switchAccount) h.account('other')
    h.reads[0]({ usage: { ...usage(), id: switchAccount ? input.scope.id : 'foreign' }, recorded: true })
    await flight
    assert.equal(h.pages()[usageKey(input)].usage, undefined)
    assert.ok(h.pages()[usageKey(input)].error)
    release()
  }
})
test('read concurrency is capped at six and release evicts all pages', { timeout: 1000 }, async () => {
  const h = harness()
  const inputs = Array.from({ length: 8 }, (_, i) => ({ ...input, scope: { ...input.scope, id: `worker-${i}` } }))
  const releases = inputs.map(i => h.runtime.acquire(i))
  await Promise.resolve(); assert.equal(h.reads.length, 6)
  h.reads[0]({ usage: { ...usage(), id: inputs[0].scope.id }, recorded: true })
  await h.runtime.refresh(inputs[0]); await Promise.resolve()
  assert.equal(h.reads.length, 7)
  releases.forEach(release => release())
  assert.deepEqual(h.pages(), {})
})
test('budget writes require loaded exact revision and never occur on acquire', { timeout: 1000 }, async () => {
  const h = harness(), budgetInput = { ...input, budget: true }, release = h.runtime.acquire(budgetInput)
  await Promise.resolve()
  assert.equal(h.saves(), 0)
  await assert.rejects(h.runtime.save(budgetInput, { expected_revision: 1, daily_cost_limit_usd: 2, daily_tokens_limit: 0 }), /stale/)
  const budget = { account_scope_id: 'acct', worker_id: input.scope.id, revision: 2, usage: usage(), daily_cost_limit_usd: 0, daily_tokens_limit: 0 } as WorkerBudgetStatus
  h.reads[0]({ usage: usage(), recorded: true, budget }); await h.runtime.refresh(budgetInput)
  await assert.rejects(h.runtime.save(budgetInput, { expected_revision: 1, daily_cost_limit_usd: 2, daily_tokens_limit: 0 }), /stale/)
  assert.equal(h.saves(), 0)
  await h.runtime.save(budgetInput, { expected_revision: 2, daily_cost_limit_usd: 2, daily_tokens_limit: 0 })
  assert.equal(h.saves(), 1); assert.equal(h.pages()[usageKey(budgetInput)].stale, true)
  release()
})

test('durable replacement updates demanded worker once without lifetime reread', { timeout: 1000 }, async () => {
  const h = harness(), release = h.runtime.acquire(input)
  await Promise.resolve()
  h.reads[0]({ usage: usage(), recorded: true }); await h.runtime.refresh(input)
  const frame = { kind: 'usage.scope.updated', event: { event_type: 'usage.scope.updated', payload: { scope_totals: [usage(3)] } } } as any
  h.runtime.acceptFrame(frame); h.runtime.acceptFrame(frame)
  assert.equal(h.reads.length, 1)
  assert.equal(h.pages()[usageKey(input)].usage?.revision, 3)
  assert.equal(h.pages()[usageKey(input)].usage?.total_tokens, 10)
  release()
})
