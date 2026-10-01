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
  const budget = budgetStatus()
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

function budgetStatus(): WorkerBudgetStatus {
  return { account_scope_id: 'acct', worker_id: input.scope.id, revision: 2, updated_at: 0, usage: usage(), daily_cost_limit_usd: 0, daily_tokens_limit: 0, date: '2026-01-01', remaining_cost_usd: null, remaining_tokens: null, account_remaining_cost_usd: null, account_remaining_tokens: null, blocked: false, inflight: false, account_inflight: false, account_coverage: 'observed_receipts_only', account_policy: { account_scope_id: 'acct', enabled: false, daily_cost_limit_usd: 0, updated_at: 0 }, account_usage: { account_scope_id: 'acct', date: '2026-01-01', total_cost_usd: 1, total_tokens: 10 }, limitations: 'Observed receipts only.' }
}
// Purpose: authenticated hydration must reject incomplete/foreign/malformed cost
// and policy snapshots before UI or CAS writes. Runtime is the narrowest boundary
// that can assert rejection AND absence of published budget/usage and writes.
test('malformed usage and budget snapshots never publish or authorize saves', { timeout: 2000 }, async () => {
  const invalid = [
    { usage: { ...usage(), cache_read_tokens: undefined } },
    { usage: { ...usage(), coverage: 'free' } },
    { usage: { ...usage(), history_complete: undefined } },
    { usage: { ...usage(), receipt_count: 0 } },
    { budget: { ...budgetStatus(), date: '2026-02-30' } },
    { budget: { ...budgetStatus(), remaining_tokens: undefined } },
    { budget: { ...budgetStatus(), account_usage: { ...budgetStatus().account_usage, account_scope_id: 'foreign' } } },
    { budget: { ...budgetStatus(), account_policy: null } },
    { budget: { ...budgetStatus(), revision: -1 } },
    { budget: { ...budgetStatus(), blocked: 'false' } },
  ]
  for (const patch of invalid) {
    const h = harness(), i = { ...input, budget: true }, release = h.runtime.acquire(i)
    await Promise.resolve()
    h.reads[0]({ usage: usage(), recorded: true, budget: budgetStatus(), ...patch } as any)
    await h.runtime.refresh(i)
    assert.ok(h.pages()[usageKey(i)].error)
    assert.equal(h.pages()[usageKey(i)].usage, undefined)
    assert.equal(h.pages()[usageKey(i)].budget, undefined)
    await assert.rejects(h.runtime.save(i, { expected_revision: 2, daily_cost_limit_usd: 1, daily_tokens_limit: 0 }), /stale/)
    assert.equal(h.saves(), 0); release()
  }
})
// Purpose: durable replay is idempotent, but another worker's receipt still changes
// shared allowance. Metadata-free worker events must conservatively repair caps;
// exact cursor replays do not repeat the read. Policy bursts
// repair once after in-flight completion. Runtime proves actual read counts.
test('budget replay is deduped and other workers still repair account allowance', { timeout: 2000 }, async () => {
  const h = harness(), i = { ...input, budget: true }, release = h.runtime.acquire(i)
  await Promise.resolve()
  h.reads[0]({ usage: usage(), recorded: true, budget: budgetStatus() }); await h.runtime.refresh(i)
  h.runtime.acceptFrame({ kind: 'event', event: { payload: { revision: 8 } } } as any)
  assert.equal(h.reads.length, 1)
  const frame = { kind: 'usage.scope.updated', event: { payload: { scope_totals: [{ ...usage(3), id: 'other-worker' }] } } } as any
  h.runtime.acceptFrame(frame); h.runtime.acceptFrame(frame)
  await Promise.resolve(); assert.equal(h.reads.length, 2)
  h.reads[1]({ usage: usage(), recorded: true, budget: budgetStatus() }); await h.runtime.refresh(i)
  await Promise.resolve(); assert.equal(h.reads.length, 2)
  const policy = { kind: 'worker.updated', worker_id: input.scope.id, event: { payload: { budget_revision: 3 } } } as any
  h.runtime.acceptFrame(policy); h.runtime.acceptFrame(policy)
  await Promise.resolve(); assert.equal(h.reads.length, 3)
  h.reads[2]({ usage: usage(), recorded: true, budget: { ...budgetStatus(), revision: 3 } }); await h.runtime.refresh(i)
  await Promise.resolve(); assert.equal(h.reads.length, 3)
  const stripped = { kind: 'worker.updated', endpoint_cursor: 'opaque-policy-event' }
  h.runtime.acceptFrame(stripped); h.runtime.acceptFrame(stripped)
  await Promise.resolve(); assert.equal(h.reads.length, 4)
  h.reads[3]({ usage: usage(), recorded: true, budget: { ...budgetStatus(), revision: 4 } }); await h.runtime.refresh(i)
  await Promise.resolve(); assert.equal(h.reads.length, 4)
  release()
})

// Purpose: no-record hydration is absence of evidence, not a free receipt.
// validUsageTotal/refresh reject nonzero no-record snapshots and preserve the
// explicit recorded=false contract; runtime assertions prevent UI invention.
test('no records accepts only an empty snapshot and keeps recorded false', { timeout: 1000 }, async () => {
  const h = harness(), release = h.runtime.acquire(input)
  await Promise.resolve()
  const empty: UsageScopeTotal = { ...usage(0), total_tokens: 0, input_tokens: 0, provider_cost_usd: 0, receipt_count: 0, coverage: 'no_records' }
  h.reads[0]({ usage: empty, recorded: false }); await h.runtime.refresh(input)
  assert.equal(h.pages()[usageKey(input)].recorded, false)
  assert.equal(h.pages()[usageKey(input)].error, undefined)
  const flight = h.runtime.refresh(input); await Promise.resolve()
  h.reads[1]({ usage: { ...empty, total_tokens: 1 }, recorded: false }); await flight
  assert.ok(h.pages()[usageKey(input)].error)
  assert.equal(h.pages()[usageKey(input)].usage?.total_tokens, 0)
  assert.equal(h.pages()[usageKey(input)].stale, true)
  release()
})

// Purpose: DesktopUsageRuntime.save must reject worker limits above the enabled
// overall ceiling without issuing a PUT. The runtime layer proves zero writes,
// including token ceilings and account-policy disabled/unset cases; backend CAS
// remains the final authority if the account limit changes after hydration.
test('overall ceiling rejects excessive dollar and token writes without mutation', { timeout: 2000 }, async () => {
  for (const patch of [{ daily_cost_limit_usd: 4, daily_tokens_limit: 0 }, { daily_cost_limit_usd: 2, daily_tokens_limit: 101 }]) {
    const h = harness(), i = { ...input, budget: true }, release = h.runtime.acquire(i)
    await Promise.resolve()
    const budget = { ...budgetStatus(), account_policy: { ...budgetStatus().account_policy, enabled: true, daily_cost_limit_usd: 3, daily_tokens_limit: 100 }, account_remaining_cost_usd: 2, account_remaining_tokens: 90 }
    h.reads[0]({ usage: usage(), recorded: true, budget }); await h.runtime.refresh(i)
    await assert.rejects(h.runtime.save(i, { expected_revision: 2, ...patch }), /cannot exceed/)
    assert.equal(h.saves(), 0)
    assert.equal(h.pages()[usageKey(i)].budget?.revision, 2)
    release()
  }
  for (const enabled of [false, true]) {
    const h = harness(), i = { ...input, budget: true }, release = h.runtime.acquire(i)
    await Promise.resolve()
    h.reads[0]({ usage: usage(), recorded: true, budget: { ...budgetStatus(), account_policy: { ...budgetStatus().account_policy, enabled } } }); await h.runtime.refresh(i)
    await h.runtime.save(i, { expected_revision: 2, daily_cost_limit_usd: 5, daily_tokens_limit: 900 })
    assert.equal(h.saves(), 1)
    release()
  }
})

// Purpose: validWorkerBudget owns structured safety-status validation. A foreign
// day, malformed source or non-exhausted hold must not publish a stopped state
// or authorize saves. Refresh is the narrowest real cache boundary for this.
test('structured holds validate before publishing stopped state', { timeout: 2000 }, async () => {
  const hold = { date: '2026-01-01', reason: 'daily_budget_exhausted' as const, cap_source: 'worker' as const, dimension: 'usd' as const, limit: 2, usage: 2, reset_at: Date.parse('2026-01-02T00:00:00Z') }
  for (const invalid of [undefined, { ...hold, date: '2025-12-31' }, { ...hold, usage: 1 }, { ...hold, cap_source: 'foreign' }, { ...hold, reset_at: -1 }]) {
    const h = harness(), i = { ...input, budget: true }, release = h.runtime.acquire(i)
    await Promise.resolve()
    const budget = { ...budgetStatus(), blocked: true, blocked_reason: 'daily hold', hold: invalid || hold }
    h.reads[0]({ usage: usage(), recorded: true, budget: budget as WorkerBudgetStatus }); await h.runtime.refresh(i)
    if (invalid) {
      assert.ok(h.pages()[usageKey(i)].error)
      assert.equal(h.pages()[usageKey(i)].budget, undefined)
      await assert.rejects(h.runtime.save(i, { expected_revision: 2, daily_cost_limit_usd: 0, daily_tokens_limit: 0 }), /stale/)
      assert.equal(h.saves(), 0)
    } else {
      assert.deepEqual(h.pages()[usageKey(i)].budget?.hold, hold)
    }
    release()
  }
})
