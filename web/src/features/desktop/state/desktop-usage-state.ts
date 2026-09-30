import type { UsageScope, UsageScopeTotal, WorkerBudgetStatus } from '../../../../../packages/sdk/src/usage'
export type { UsageScope, UsageScopeTotal, WorkerBudgetStatus, WorkerBudgetUpdate } from '../../../../../packages/sdk/src/usage'
export interface UsageInput { accountScopeId: string; scope: UsageScope; budget?: boolean }
export interface UsagePage { input: UsageInput; loading: boolean; stale: boolean; generation: number; requestId?: string; usage?: UsageScopeTotal; recorded?: boolean; budget?: WorkerBudgetStatus; error?: string }
export type UsagePages = Record<string, UsagePage>
export const usageKey = (input: UsageInput) => JSON.stringify([input.accountScopeId, input.scope.kind, input.scope.project_id || '', input.scope.id, !!input.budget])
export type UsageAction =
  | { type: 'usage.begin'; input: UsageInput; requestId: string }
  | { type: 'usage.finish'; input: UsageInput; requestId: string; generation: number; usage?: UsageScopeTotal; recorded?: boolean; budget?: WorkerBudgetStatus; error?: string }
  | { type: 'usage.invalidate'; accountScopeId?: string; workerId?: string; budgetOnly?: boolean }
  | { type: 'usage.snapshot'; accountScopeId: string; totals: UsageScopeTotal[] }
  | { type: 'usage.evict'; input: UsageInput }
export function sameUsageScope(a: UsageScope, b: UsageScope) { return a.kind === b.kind && a.id === b.id && (a.project_id || '') === (b.project_id || '') }
export function reduceUsagePages(pages: UsagePages, action: UsageAction): UsagePages {
  if (action.type === 'usage.invalidate' || action.type === 'usage.snapshot') {
    return Object.fromEntries(Object.entries(pages).map(([key, page]) => {
      if (action.accountScopeId && page.input.accountScopeId !== action.accountScopeId) return [key, page]
      if (action.type === 'usage.snapshot') {
        const total = action.totals.find(t => sameUsageScope(t, page.input.scope))
        if (!total || page.input.budget || (page.usage && total.revision <= page.usage.revision)) return [key, page]
        return [key, { ...page, usage: total, recorded: true, stale: false, error: undefined }]
      }
      if (action.budgetOnly && !page.input.budget) return [key, page]
      if (action.workerId && !(page.input.scope.kind === 'worker' && page.input.scope.id === action.workerId) && !(page.input.scope.kind === 'worker_run' && page.input.scope.project_id === action.workerId)) return [key, page]
      return [key, { ...page, stale: true, generation: page.generation + 1 }]
    }))
  }
  const key = usageKey(action.input), old = pages[key]
  if (action.type === 'usage.evict') { const next = { ...pages }; delete next[key]; return next }
  if (action.type === 'usage.begin') return { ...pages, [key]: { ...old, input: action.input, generation: old?.generation || 0, loading: true, stale: old?.stale ?? true, requestId: action.requestId, error: undefined } }
  if (!old || old.requestId !== action.requestId) return pages
  if (old.generation !== action.generation) return { ...pages, [key]: { ...old, loading: false } }
  // A delayed hydration must never overwrite a newer durable replacement snapshot.
  const newer = old.usage && action.usage && old.usage.revision > action.usage.revision
  return { ...pages, [key]: { ...old, loading: false, requestId: undefined, stale: !!action.error, error: action.error,
    usage: newer ? old.usage : action.usage ?? old.usage, recorded: newer ? old.recorded : action.recorded ?? old.recorded, budget: action.budget ?? old.budget } }
}
