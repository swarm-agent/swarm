import { useState } from 'react'
import { desktopUsage, useUsagePage } from '../runtime/desktop-usage'
import type { UsageInput } from '../state/desktop-usage-state'
import { usageCostSummary } from './scope-usage'

export function WorkerBudgetEditor({ accountScopeId, workerId, disabled }: { accountScopeId: string; workerId: string; disabled: boolean }) {
  const input: UsageInput = { accountScopeId, scope: { kind: 'worker', id: workerId }, budget: true }
  const page = useUsagePage(input), budget = page?.budget
  const [draft, setDraft] = useState<{ revision: number; dollars: string; tokens: string; overall: boolean }>()
  const [busy, setBusy] = useState(false), [error, setError] = useState('')
  const stale = !!page?.stale || !!page?.loading || !!page?.error || !budget || !!(draft && draft.revision !== budget.revision)
  const ceiling = budget?.account_policy.enabled && budget.account_policy.daily_cost_limit_usd > 0 ? budget.account_policy.daily_cost_limit_usd : undefined
  const value = draft || { revision: budget?.revision || 0, dollars: String(budget?.daily_cost_limit_usd || ''), tokens: String(budget?.daily_tokens_limit || 0), overall: !budget?.daily_cost_limit_usd }
  const stage = (patch: Partial<typeof value>) => { setError(''); setDraft({ ...value, ...patch }) }
  const locked = disabled || busy || stale
  const reset = budget?.hold?.reset_at || budget?.reset_at
  return <section aria-label="Worker daily budget" className="min-w-0 space-y-3 rounded-xl border border-[var(--app-border)] p-4" onClick={event => event.stopPropagation()}>
    <h4 className="text-base font-semibold">Daily limit</h4>
    {page?.error && <p role="alert">{page.error}</p>}
    {!budget ? <p>{page?.loading ? 'Loading budget…' : 'Budget unavailable until loaded.'}</p> : <>
      <p className="break-words text-sm">{page?.stale ? 'Last known · ' : ''}Overall ceiling: <strong>{ceiling ? `$${ceiling}/day` : budget.account_policy.enabled ? 'No dollar limit set' : 'Disabled'}</strong> · <a href="/usage" className="underline">Change overall limit in Usage</a></p>
      {!ceiling && value.overall && <p className="font-semibold">No dollar safety limit. Set a worker limit here or enable an overall limit in Usage.</p>}
      <form className="min-w-0 space-y-3" onSubmit={async event => {
        event.preventDefault(); if (locked || !draft) return
        const dollars = draft.overall ? 0 : Number(draft.dollars), tokens = Number(draft.tokens)
        if ((!draft.overall && (!draft.dollars.trim() || !Number.isFinite(dollars) || dollars <= 0)) || !draft.tokens.trim() || !Number.isSafeInteger(tokens) || tokens < 0) { setError('Enter a positive $/day limit and non-negative whole tokens.'); return }
        if (ceiling && dollars > ceiling) { setError(`Worker limit cannot exceed the overall $${ceiling}/day ceiling.`); return }
        if (budget.account_policy.enabled && budget.account_policy.daily_tokens_limit && tokens > budget.account_policy.daily_tokens_limit) { setError('Worker token limit cannot exceed the overall token ceiling.'); return }
        setBusy(true); setError('')
        try { await desktopUsage.save(input, { expected_revision: draft.revision, daily_cost_limit_usd: dollars, daily_tokens_limit: tokens }); setDraft(undefined) }
        catch (cause) { setError(cause instanceof Error ? cause.message : 'Budget save failed; reload current policy') }
        finally { setBusy(false) }
      }}>
        <fieldset disabled={locked} className="flex min-w-0 flex-wrap gap-3">
          <legend className="sr-only">Dollar limit source</legend>
          <label className="flex items-center gap-2"><input type="radio" name={`limit-${workerId}`} checked={value.overall} onChange={() => stage({ overall: true })} />Use overall limit</label>
          <label className="flex items-center gap-2"><input type="radio" name={`limit-${workerId}`} checked={!value.overall} onChange={() => stage({ overall: false })} />Set worker limit</label>
        </fieldset>
        {!value.overall && <label className="flex flex-wrap items-center gap-2">Worker limit <input className="w-28 min-w-0 rounded border p-2" aria-label="Daily worker USD" inputMode="decimal" value={value.dollars} disabled={locked} onChange={event => stage({ dollars: event.target.value })} /> $/day</label>}
        {ceiling && budget.daily_cost_limit_usd > ceiling && <p role="alert">Saved worker limit exceeds the new overall ceiling. The lower overall ceiling still applies; lower the worker limit before saving.</p>}
        <label className="flex flex-wrap items-center gap-2">Token limit <input className="w-28 min-w-0 rounded border p-2" aria-label="Daily worker tokens" inputMode="numeric" value={value.tokens} disabled={locked} onChange={event => stage({ tokens: event.target.value })} /> tokens/day (0 = no worker token cap)</label>
        {!!budget.account_policy.daily_tokens_limit && <p>Overall token ceiling: {budget.account_policy.enabled ? budget.account_policy.daily_tokens_limit.toLocaleString() + ' tokens/day' : 'disabled'}</p>}
        <div className="flex flex-wrap gap-3"><button type="submit" disabled={locked || !draft}>{busy ? 'Saving…' : 'Save limit'}</button>{draft && <button type="button" disabled={busy} onClick={() => { setDraft(undefined); setError('') }}>Cancel</button>}</div>
        {draft && stale && <p role="alert">Policy changed or is stale. Cancel and reload before saving.</p>}
      </form>
      <p className="break-words">Today used · {usageCostSummary(budget.usage)} · {budget.usage.total_tokens.toLocaleString()} tokens</p>
      <p className="text-lg font-semibold">Effective daily limit · {budget.effective_cost_limit_usd != null ? `$${budget.effective_cost_limit_usd}/day` : ceiling || budget.daily_cost_limit_usd ? `$${Math.min(ceiling || Infinity, budget.daily_cost_limit_usd || Infinity)}/day` : 'No dollar limit'}{budget.effective_tokens_limit != null ? ` · ${budget.effective_tokens_limit.toLocaleString()} tokens/day` : ''}</p>
      <p>Overall today used · {budget.account_coverage === 'no_records' ? 'No recorded receipts · cost unknown' : (budget.account_coverage === 'legacy_pricing_incomplete' || budget.account_usage.unknown_receipts) && !budget.account_usage.total_cost_usd ? 'Cost unknown · partial pricing' : `$${budget.account_usage.total_cost_usd.toFixed(4)} observed${budget.account_coverage === 'legacy_pricing_incomplete' || budget.account_usage.unknown_receipts ? ' · known subtotal only · partial pricing' : ''}`} · {budget.account_usage.total_tokens.toLocaleString()} tokens</p>
      <p>Resets {reset ? new Date(reset).toUTCString() : 'at 00:00 UTC each day'}.</p>
      {budget.hold ? <p role="alert" className="font-semibold">Stopped for today · {budget.hold.cap_source === 'account' ? 'Overall' : 'Worker'} {budget.hold.dimension === 'usd' ? 'dollar' : 'token'} limit reached. Jobs will not fire again before reset. Changing limits does not resume today.</p> : budget.blocked ? <p role="alert">Blocked: {budget.blocked_reason}</p> : <p>Available within daily limits.</p>}
      <p className="text-xs">Observed receipts only, not an invoice. Already-dispatched work may overshoot. Saving never activates a worker or clears usage.</p>
    </>}
    {error && <p role="alert">{error}</p>}
    <button type="button" disabled={busy} onClick={() => void desktopUsage.refresh(input)}>Reload budget</button>
  </section>
}
