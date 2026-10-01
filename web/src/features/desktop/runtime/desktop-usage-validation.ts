import type { UsageScopeTotal, WorkerBudgetStatus, UsageInput } from '../state/desktop-usage-state'
const amount = (v: unknown): v is number => typeof v === 'number' && Number.isFinite(v) && v >= 0
const integer = (v: unknown) => amount(v) && Number.isSafeInteger(v)
const tokenFields = ['total_tokens', 'input_tokens', 'output_tokens', 'cache_read_tokens', 'cache_write_tokens', 'thinking_tokens', 'media_receipts', 'receipt_count', 'unknown_receipts', 'free_receipts', 'subscription_receipts'] as const
const costFields = ['catalog_cost_usd', 'provider_cost_usd', 'provider_estimate_cost_usd', 'nominal_subscription_cost_usd', 'media_cost_usd'] as const
export function validUsageTotal(t: UsageScopeTotal): boolean {
  if (!t || !['task', 'worker', 'worker_run'].includes(t.kind) || typeof t.id !== 'string' || !t.id.trim() || t.id.length > 256 || !integer(t.revision)
    || (t.kind === 'worker' ? !!t.project_id : typeof t.project_id !== 'string' || !t.project_id.trim() || t.project_id.length > 256)
    || typeof t.history_complete !== 'boolean' || !['observed_receipts_only', 'repaired_receipts_incomplete', 'no_records', ''].includes(t.coverage)
    || !tokenFields.every(k => integer(t[k])) || !costFields.every(k => amount(t[k]))) return false
  if (!t.receipt_count) return t.revision === 0 && (t.coverage === 'no_records' || t.coverage === '') && [...tokenFields, ...costFields].every(k => t[k] === 0) && !t.history_complete
  return t.revision > 0 && t.coverage !== 'no_records' && t.coverage !== '' && t.unknown_receipts + t.free_receipts + t.subscription_receipts <= t.receipt_count && t.media_receipts <= t.receipt_count
}
function validDate(date: string) {
  const timestamp = typeof date === 'string' && /^\d{4}-\d{2}-\d{2}$/.test(date) ? Date.parse(`${date}T00:00:00Z`) : NaN
  return Number.isFinite(timestamp) && new Date(timestamp).toISOString().slice(0, 10) === date
}
export function validWorkerBudget(b: WorkerBudgetStatus, input: UsageInput): boolean {
  if (!b || input.scope.kind !== 'worker' || b.account_scope_id !== input.accountScopeId || b.worker_id !== input.scope.id || !integer(b.revision)
    || !amount(b.daily_cost_limit_usd) || !integer(b.daily_tokens_limit) || !integer(b.updated_at) || !validDate(b.date)
    || !validUsageTotal(b.usage) || b.usage.kind !== 'worker' || b.usage.id !== b.worker_id
    || typeof b.blocked !== 'boolean' || typeof b.inflight !== 'boolean' || typeof b.account_inflight !== 'boolean'
    || (b.blocked_reason !== undefined && typeof b.blocked_reason !== 'string') || (b.blocked && !b.blocked_reason)
    || typeof b.limitations !== 'string' || !['no_records', 'observed_receipts_only', 'legacy_pricing_incomplete'].includes(b.account_coverage)) return false
  if ((b.daily_cost_limit_usd === 0) !== (b.remaining_cost_usd === null) || (b.daily_tokens_limit === 0) !== (b.remaining_tokens === null)) return false
  if (![b.remaining_cost_usd, b.account_remaining_cost_usd].every(v => v === null || amount(v)) || ![b.remaining_tokens, b.account_remaining_tokens].every(v => v === null || integer(v))) return false
  if (b.reset_at !== undefined && !integer(b.reset_at)) return false
  if ([b.effective_cost_limit_usd, b.effective_tokens_limit].some(v => v !== undefined && v !== null && !amount(v))) return false
  const h = b.hold
  if (h && (h.date !== b.date || h.reason !== 'daily_budget_exhausted' || !['worker', 'account'].includes(h.cap_source) || !['usd', 'tokens'].includes(h.dimension) || !amount(h.limit) || h.limit <= 0 || !amount(h.usage) || h.usage < h.limit || !integer(h.reset_at) || !b.blocked)) return false
  const p = b.account_policy, u = b.account_usage
  if (!p || (b.account_remaining_cost_usd === null) !== (!p.enabled || p.daily_cost_limit_usd === 0) || (b.account_remaining_tokens === null) !== (!p.enabled || !p.daily_tokens_limit)) return false
  return !!p && p.account_scope_id === input.accountScopeId && typeof p.enabled === 'boolean' && amount(p.daily_cost_limit_usd) && (p.daily_tokens_limit === undefined || integer(p.daily_tokens_limit)) && integer(p.updated_at)
    && !!u && u.account_scope_id === input.accountScopeId && u.date === b.date && amount(u.total_cost_usd) && integer(u.total_tokens)
    && (u.unknown_receipts === undefined || integer(u.unknown_receipts)) && (u.pricing_coverage_version === undefined || integer(u.pricing_coverage_version)) && (u.pricing_coverage_incomplete === undefined || typeof u.pricing_coverage_incomplete === 'boolean')
}
