import { useState } from 'react'
import { desktopUsage, useUsagePage } from '../runtime/desktop-usage'
import type { UsageInput, UsageScopeTotal } from '../state/desktop-usage-state'
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
export function WorkerBudget({ accountScopeId, workerId, disabled = false }: { accountScopeId: string; workerId: string; disabled?: boolean }) {
  return <WorkerBudgetEditor key={`${accountScopeId}:${workerId}`} accountScopeId={accountScopeId} workerId={workerId} disabled={disabled} />
}
function WorkerBudgetEditor({ accountScopeId, workerId, disabled }: { accountScopeId: string; workerId: string; disabled: boolean }) {
  const input: UsageInput = { accountScopeId, scope: { kind: 'worker', id: workerId }, budget: true }
  const page = useUsagePage(input), budget = page?.budget
  const [draft, setDraft] = useState<{ revision: number; dollars: string; tokens: string }>()
  const [busy, setBusy] = useState(false), [error, setError] = useState('')
  const stale = !!page?.stale || !!page?.error || !budget || (draft && draft.revision !== budget.revision)
  return <section aria-label="Worker daily budget" className="space-y-2 rounded-lg border border-[var(--app-border)] p-3">
    <h4>Daily UTC budget {budget ? `· ${budget.date}` : ''}</h4>
    {page?.error && <p role="alert">{page.error}</p>}
    {!budget ? <p>{page?.loading ? 'Loading budget…' : 'Budget unavailable until loaded.'}</p> : <>
      <p className="break-words text-xs">{page?.stale ? 'Last known · ' : ''}{budget.blocked ? `Blocked: ${budget.blocked_reason}` : 'Not blocked'} · remaining worker: {budget.remaining_cost_usd === null ? 'USD unset' : `$${budget.remaining_cost_usd.toFixed(4)}`} / {budget.remaining_tokens === null ? 'tokens unset' : `${budget.remaining_tokens} tokens`}</p>
      <details className="min-w-0 text-xs"><summary className="cursor-pointer break-words">Today · {budget.usage.receipt_count ? `${budget.usage.total_tokens.toLocaleString()} tokens · ${usageCostSummary(budget.usage)}` : 'No recorded receipts · cost unknown'} · details and caps</summary>
      <p>Today is daily UTC usage, separate from lifetime totals.</p>
      <p>Account remaining: {budget.account_remaining_cost_usd === null ? 'USD unset' : `$${budget.account_remaining_cost_usd.toFixed(6)}`} / {budget.account_remaining_tokens === null ? 'tokens unset' : `${budget.account_remaining_tokens} tokens`}. Account policy is read-only here.</p>
      <p>Daily pricing: {budget.usage.coverage || 'unavailable'} · account: {budget.account_coverage}{budget.usage.unknown_receipts ? ' · unknown worker receipts' : ''}</p>
      {budget.blocked && <p role="alert">Blocked: {budget.blocked_reason || 'Accounting review required'}</p>}
      <p>{budget.inflight || budget.account_inflight ? 'Provider work in flight. ' : ''}Already-dispatched work may overshoot; stop-before-next-call is not an invoice-hard cap.</p><p>{budget.limitations}</p>
      {!draft ? <button type="button" disabled={disabled || !!stale || busy} onClick={() => { setError(''); setDraft({ revision: budget.revision, dollars: String(budget.daily_cost_limit_usd), tokens: String(budget.daily_tokens_limit) }) }}>Edit worker caps</button> : <form onSubmit={async event => {
        event.preventDefault(); if (busy || stale || disabled) return
        const dollars = Number(draft.dollars), tokens = Number(draft.tokens)
        if (!draft.dollars.trim() || !draft.tokens.trim() || !Number.isFinite(dollars) || dollars < 0 || !Number.isSafeInteger(tokens) || tokens < 0) { setError('Enter non-negative USD and whole tokens; zero unsets a cap.'); return }
        setBusy(true); setError('')
        try { await desktopUsage.save(input, { expected_revision: draft.revision, daily_cost_limit_usd: dollars, daily_tokens_limit: tokens }); setDraft(undefined) }
        catch (cause) { setError(cause instanceof Error ? cause.message : 'Budget save failed; reload current policy') }
        finally { setBusy(false) }
      }} className="flex flex-wrap gap-2">
        <label>Daily USD <input className="w-24" aria-label="Daily worker USD" inputMode="decimal" value={draft.dollars} disabled={busy || disabled} onChange={e => setDraft({ ...draft, dollars: e.target.value })} /></label>
        <label>Daily tokens <input className="w-28" aria-label="Daily worker tokens" inputMode="numeric" value={draft.tokens} disabled={busy || disabled} onChange={e => setDraft({ ...draft, tokens: e.target.value })} /></label>
        <button disabled={busy || disabled || !!stale}>Save caps</button><button type="button" disabled={busy} onClick={() => { setDraft(undefined); setError('') }}>Cancel</button>
        {!!stale && <p role="alert">Policy changed or is stale. Cancel and reload before saving.</p>}
      </form>}
      <p>Zero leaves a cap unset. Editing caps never resets receipts or changes account limits.</p>
      </details>
    </>}
    {error && <p role="alert">{error}</p>}
    <button type="button" disabled={busy} onClick={() => void desktopUsage.refresh(input)}>Reload budget</button>
  </section>
}
