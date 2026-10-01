import { desktopUsage, useUsagePage } from '../runtime/desktop-usage'
import type { UsageInput, UsageScopeTotal } from '../state/desktop-usage-state'
import { WorkerBudgetEditor } from './worker-budget-editor'
export function usageCostParts(usage: UsageScopeTotal) {
  return [
    ['Catalog estimate', usage.catalog_cost_usd], ['Provider cost', usage.provider_cost_usd],
    ['Provider estimate', usage.provider_estimate_cost_usd], ['Subscription equivalent (not charged)', usage.nominal_subscription_cost_usd],
    ['Media cost', usage.media_cost_usd],
  ] as const
}
export function usageCostSummary(usage: UsageScopeTotal): string {
  const parts: string[] = []
  if (usage.catalog_cost_usd) parts.push(`$${usage.catalog_cost_usd.toFixed(4)} catalog estimate`)
  if (usage.provider_cost_usd) parts.push(`$${usage.provider_cost_usd.toFixed(4)} provider cost`)
  if (usage.provider_estimate_cost_usd) parts.push(`$${usage.provider_estimate_cost_usd.toFixed(4)} provider estimate`)
  if (usage.media_cost_usd) parts.push(`$${usage.media_cost_usd.toFixed(4)} media cost`)
  if (usage.subscription_receipts) parts.push(`Subscription · $${usage.nominal_subscription_cost_usd.toFixed(4)} nominal (not charged)`)
  if (usage.free_receipts) parts.push(`${usage.free_receipts} explicitly free`)
  if (usage.unknown_receipts) parts.push(parts.length ? 'known subtotal only · partial pricing' : 'Cost unknown')
  if (!parts.length) parts.push(usage.receipt_count ? 'Observed cost $0 · not an invoice' : 'No recorded receipts · cost unknown')
  if (!usage.history_complete) parts.push('history incomplete')
  return parts.join(' · ')
}
export function ScopeUsage({ input, label = 'Lifetime observed usage' }: { input: UsageInput; label?: string }) {
  const page = useUsagePage(input), usage = page?.usage
  return <details className="min-w-0 text-xs" onClick={e => e.stopPropagation()}>
    <summary className="cursor-pointer break-words">{label} · {usage && page?.recorded ? `${usage.total_tokens.toLocaleString()} tokens · ${usageCostSummary(usage)}` : page?.error ? 'Unavailable' : page?.loading ? 'Loading…' : 'No recorded receipts'}{page?.stale && usage ? ' · last known' : ''}</summary>
    {page?.error && <p role="alert">{page.error}</p>}
    <button type="button" onClick={() => void desktopUsage.refresh(input)}>Refresh usage</button>
    {usage && page?.recorded && <dl className="grid grid-cols-[minmax(0,1fr)_auto] gap-x-3 gap-y-1">
      {usageCostParts(usage).map(([name, cost]) => <div key={name} className="contents"><dt>{name}</dt><dd>${cost.toFixed(6)}</dd></div>)}
      <dt>Free / subscription / unknown receipts</dt><dd>{usage.free_receipts} / {usage.subscription_receipts} / {usage.unknown_receipts}</dd>
      <dt>Input / output tokens</dt><dd>{usage.input_tokens} / {usage.output_tokens}</dd>
      <dt>Cache read / write / thinking tokens</dt><dd>{usage.cache_read_tokens} / {usage.cache_write_tokens} / {usage.thinking_tokens}</dd>
      <dt>Coverage / missing-price receipts</dt><dd>{usage.coverage} / {usage.unknown_receipts}</dd>
    </dl>}
    <p>All recorded attempts and descendants, counted by the server once. Not context occupancy or an invoice.</p>
    <p>{!page?.recorded ? 'Missing receipts do not prove zero cost.' : usage?.history_complete ? 'Recorded history complete.' : 'History incomplete; observed receipts only.'}{!!usage?.unknown_receipts && ' Partial pricing: unknown receipts.'}</p>
  </details>
}
export function InlineUsage({ input }: { input: UsageInput }) {
  const page = useUsagePage(input)
  return <span aria-label="Lifetime observed usage" className="min-w-0 break-words text-xs">
    Observed · {page?.usage && page.recorded ? `${page.usage.total_tokens.toLocaleString()} tokens · ${usageCostSummary(page.usage)}` : page?.error ? 'Usage unavailable' : page?.loading ? 'Loading usage…' : 'No recorded receipts · cost unknown'}{page?.stale && page.usage ? ' · last known' : ''}
  </span>
}
export function WorkerBudgetMetadata({ accountScopeId, workerId }: { accountScopeId: string; workerId: string }) {
  const page = useUsagePage({ accountScopeId, scope: { kind: 'worker', id: workerId }, budget: true })
  const b = page?.budget
  return <span className="min-w-0 break-words text-xs">Worker · {workerId} · {b ? `${b.daily_cost_limit_usd > 0 ? `$${b.daily_cost_limit_usd}/day worker limit` : b.account_policy.enabled && b.account_policy.daily_cost_limit_usd > 0 ? `$${b.account_policy.daily_cost_limit_usd}/day overall limit` : 'No dollar limit'}${b.daily_tokens_limit ? ` · ${b.daily_tokens_limit.toLocaleString()} tokens/day` : ''}${b.hold ? ' · Stopped for today' : ''}` : page?.error ? 'Budget unavailable' : 'Loading budget…'}{page?.stale && b ? ' · last known' : ''}</span>
}
export function WorkerBudget({ accountScopeId, workerId, disabled = false }: { accountScopeId: string; workerId: string; disabled?: boolean }) {
  return <WorkerBudgetEditor key={`${accountScopeId}:${workerId}`} accountScopeId={accountScopeId} workerId={workerId} disabled={disabled} />
}
