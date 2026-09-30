import { useEffect, useMemo } from 'react'
import { getDesktopSessionIdentitySnapshot, requestJson } from '../../../app/api'
import { dispatchDesktopV3Cache, getDesktopV3CacheSnapshot, useDesktopV3CacheSelector } from '../state/desktop-v3-cache-store'
import type { RealtimeMessage } from '../state/desktop-v3-cache-types'
import { sameUsageScope, usageKey, type UsageAction, type UsageInput, type UsagePages, type UsageScopeTotal, type WorkerBudgetStatus, type WorkerBudgetUpdate } from '../state/desktop-usage-state'
export interface UsageDeps { account: () => string | undefined; pages: () => UsagePages; dispatch: (a: UsageAction) => void; read: (i: UsageInput) => Promise<{ usage: UsageScopeTotal; recorded: boolean; budget?: WorkerBudgetStatus }>; save: (id: string, policy: WorkerBudgetUpdate) => Promise<unknown> }
function validUsageTotal(t: UsageScopeTotal): boolean {
  return !!t && ['task', 'worker', 'worker_run'].includes(t.kind) && typeof t.id === 'string' && !!t.id && Number.isSafeInteger(t.revision) && t.revision >= 0
    && ['total_tokens', 'input_tokens', 'output_tokens', 'catalog_cost_usd', 'provider_cost_usd', 'provider_estimate_cost_usd', 'nominal_subscription_cost_usd', 'media_cost_usd', 'receipt_count', 'unknown_receipts', 'free_receipts', 'subscription_receipts'].every(key => { const value = t[key as keyof UsageScopeTotal]; return typeof value === 'number' && Number.isFinite(value) && value >= 0 })
}
const budgetPath = (id: string) => `/v3/usage/worker-budget?${new URLSearchParams({ worker_id: id })}`
export class DesktopUsageRuntime {
  private demands = new Map<string, { input: UsageInput; count: number }>()
  private flights = new Map<string, Promise<void>>()
  private activeReads = 0
  private readWaiters: Array<() => void> = []
  private async boundedRead(input: UsageInput) {
    if (this.activeReads >= 6) await new Promise<void>(resolve => this.readWaiters.push(resolve))
    else this.activeReads++
    try { this.assertAccount(input); return await this.deps.read(input) }
    finally { const next = this.readWaiters.shift(); if (next) next(); else this.activeReads-- }
  }
  constructor(private deps: UsageDeps) {}
  private assertAccount(input: UsageInput) { if (!input.accountScopeId || this.deps.account() !== input.accountScopeId) throw new Error('Usage account changed; reopen this view') }
  acquire(input: UsageInput) {
    this.assertAccount(input)
    const key = usageKey(input), old = this.demands.get(key)
    if (!old && this.demands.size >= 320) throw new Error('Too many usage scopes open')
    this.demands.set(key, { input, count: (old?.count || 0) + 1 })
    void this.refresh(input)
    let released = false
    return () => { if (released) return; released = true; const entry = this.demands.get(key); if (entry && --entry.count === 0) { this.demands.delete(key); this.flights.delete(key); this.deps.dispatch({ type: 'usage.evict', input }) } }
  }
  refresh(input: UsageInput): Promise<void> {
    const key = usageKey(input), old = this.flights.get(key)
    if (old) return old
    const requestId = crypto.randomUUID()
    this.deps.dispatch({ type: 'usage.begin', input, requestId })
    const generation = this.deps.pages()[key].generation
    const flight: Promise<void> = Promise.resolve().then(() => { this.assertAccount(input); return this.boundedRead(input) }).then(data => {
      if (this.flights.get(key) !== flight) return
      this.assertAccount(input)
      if (!validUsageTotal(data.usage) || !sameUsageScope(data.usage, input.scope) || !Number.isSafeInteger(data.usage.revision) || data.usage.revision < 0 || typeof data.recorded !== 'boolean' || (input.budget && (!data.budget || data.budget.account_scope_id !== input.accountScopeId || data.budget.worker_id !== input.scope.id))) throw new Error('Usage response scope mismatch')
      this.deps.dispatch({ type: 'usage.finish', input, requestId, generation, ...data })
    }).catch(error => { if (this.flights.get(key) === flight) this.deps.dispatch({ type: 'usage.finish', input, requestId, generation, error: error instanceof Error ? error.message : 'Usage unavailable' }) }).finally(() => {
      if (this.flights.get(key) !== flight) return
      this.flights.delete(key)
      if (this.demands.has(key) && this.deps.account() === input.accountScopeId && this.deps.pages()[key]?.generation !== generation) void this.refresh(input)
    })
    this.flights.set(key, flight); return flight
  }
  invalidate(accountScopeId?: string, workerId?: string, budgetOnly = false) {
    this.deps.dispatch({ type: 'usage.invalidate', accountScopeId, workerId, budgetOnly })
    for (const { input } of this.demands.values()) if (this.deps.account() === input.accountScopeId && (!accountScopeId || input.accountScopeId === accountScopeId) && (!budgetOnly || input.budget) && (!workerId || input.scope.kind === 'worker' && input.scope.id === workerId || input.scope.kind === 'worker_run' && input.scope.project_id === workerId) && this.deps.pages()[usageKey(input)]?.stale) void this.refresh(input)
  }
  acceptFrame(frame: RealtimeMessage) {
    if (frame.kind === 'usage.scope.updated' && this.deps.account()) {
      const account = this.deps.account()!
      let payload: unknown = frame.event?.payload
      if (typeof payload === 'string') { try { payload = JSON.parse(payload) } catch { this.invalidate(account); return } }
      const totals = (payload as { scope_totals?: UsageScopeTotal[] } | undefined)?.scope_totals
      if (!Array.isArray(totals) || totals.length > 320 || totals.some(t => !validUsageTotal(t))) { this.invalidate(account); return }
      this.deps.dispatch({ type: 'usage.snapshot', accountScopeId: account, totals })
      // Any receipt may change the shared account allowance shown on a worker.
      this.invalidate(account, undefined, true)
    } else if (frame.kind === 'worker.updated') this.invalidate(typeof frame.account_scope_id === 'string' ? frame.account_scope_id : undefined, typeof frame.worker_id === 'string' ? frame.worker_id : undefined)
    else if (frame.kind === 'cursor.error' || frame.kind === 'rehydrate.required') this.invalidate()
  }
  async save(input: UsageInput, policy: WorkerBudgetUpdate) {
    this.assertAccount(input)
    const page = this.deps.pages()[usageKey(input)]
    if (!page?.budget || page.stale || page.loading || page.budget.revision !== policy.expected_revision) throw new Error('Worker budget revision is stale; reload before saving')
    if (!input.budget || input.scope.kind !== 'worker' || !Number.isSafeInteger(policy.expected_revision) || policy.expected_revision < 0 || !Number.isFinite(policy.daily_cost_limit_usd) || policy.daily_cost_limit_usd < 0 || !Number.isSafeInteger(policy.daily_tokens_limit) || policy.daily_tokens_limit < 0) throw new Error('Invalid worker budget')
    try { await this.deps.save(input.scope.id, policy); this.assertAccount(input) }
    finally { if (this.deps.account() === input.accountScopeId) this.invalidate(input.accountScopeId, input.scope.id) }
  }
}
export const desktopUsage = new DesktopUsageRuntime({
  account: () => getDesktopSessionIdentitySnapshot()?.accountScopeId, pages: () => getDesktopV3CacheSnapshot().usagePages, dispatch: dispatchDesktopV3Cache,
  read: async input => {
    if (input.budget) { const budget = await requestJson<WorkerBudgetStatus>(budgetPath(input.scope.id)); return { usage: budget.usage, recorded: budget.usage.receipt_count > 0, budget } }
    return requestJson(`/v3/usage/scope?${new URLSearchParams({ kind: input.scope.kind, id: input.scope.id, ...(input.scope.project_id ? { project_id: input.scope.project_id } : {}) })}`)
  },
  save: async (id, policy) => {
    const result = await requestJson<{ worker_id: string; revision: number }>(budgetPath(id), { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(policy) })
    if (result.worker_id !== id || !Number.isSafeInteger(result.revision) || result.revision <= policy.expected_revision) throw new Error('Budget save response is invalid; reload current policy')
    return result
  },
})
export function useUsagePage(input: UsageInput) {
  const key = usageKey(input), stable = useMemo(() => input, [key])
  const page = useDesktopV3CacheSelector(state => state.usagePages[key])
  useEffect(() => { if (getDesktopSessionIdentitySnapshot()?.accountScopeId === stable.accountScopeId) return desktopUsage.acquire(stable) }, [stable])
  return getDesktopSessionIdentitySnapshot()?.accountScopeId === input.accountScopeId ? page : undefined
}
