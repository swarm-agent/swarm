import { useState } from 'react'
import type { AutomationDefinition } from '../../state/desktop-automation-api'
import { validatePlans } from './automation-view'

export const automationControl = 'min-h-10 rounded border border-[var(--app-border)] bg-[var(--app-bg)] px-3 py-2 text-[var(--app-text)] focus-visible:outline-2 focus-visible:outline-[var(--app-primary)] disabled:opacity-50'
export function AutomationEditor({ initial, revision = 0, disabled, onSave }: { initial?: AutomationDefinition; revision?: number; disabled: boolean; onSave: (definition: AutomationDefinition) => Promise<void> }) {
  const [draft, setDraft] = useState<AutomationDefinition>(() => initial ? structuredClone(initial) : { name: '', enabled: false, plans: [], schedule: { kind: 'manual', timezone: Intl.DateTimeFormat().resolvedOptions().timeZone, missed_policy: 'skip', overlap_policy: 'independent' }, authorization: { mode: 'approval_required' } })
  const [error, setError] = useState('')
  const [baseRevision] = useState(revision)
  const changed = baseRevision !== revision
  const field = (label: string, value: string, update: (value: string) => void, required = false) => <label className="flex flex-col gap-1">{label}<input className={automationControl} value={value} required={required} onChange={event => update(event.target.value)} /></label>
  return <form className="space-y-4" onSubmit={event => {
    event.preventDefault()
    if (disabled || changed) return
    try {
      validatePlans(draft.plans)
      if (draft.schedule.timezone) new Intl.DateTimeFormat('en', { timeZone: draft.schedule.timezone })
      setError('')
      // A changed configuration is a new policy draft, never reuse its old grant.
      const { approval_reference: _grant, ...authorization } = draft.authorization
      void onSave({ ...draft, enabled: false, authorization: { ...authorization, mode: 'approval_required' } }).catch(cause => setError(cause instanceof Error ? cause.message : 'Save failed'))
    } catch (cause) { setError(cause instanceof Error ? cause.message : 'Invalid configuration') }
  }}>
    {changed && <p role="alert">Configuration changed. Reopen Configuration to load the latest revision before editing again.</p>}
    <fieldset disabled={disabled || changed} className="space-y-4">
      <legend className="font-semibold">Configuration</legend>
      {field('Name', draft.name, name => setDraft({ ...draft, name }), true)}
      <p>Configuration edits save paused and require a new execution-policy approval before enabling. Existing plan pins and the canonical conversation are preserved.</p>
      <label className="flex flex-col gap-1">Trigger<select className={automationControl} value={draft.schedule.kind} onChange={event => setDraft({ ...draft, schedule: { kind: event.target.value as AutomationDefinition['schedule']['kind'], timezone: draft.schedule.timezone, missed_policy: draft.schedule.missed_policy, overlap_policy: draft.schedule.overlap_policy } })}>{['manual', 'interval', 'cron', 'event'].map(value => <option key={value}>{value}</option>)}</select></label>
      {field('Timezone (IANA)', draft.schedule.timezone ?? '', timezone => setDraft({ ...draft, schedule: { ...draft.schedule, timezone } }), true)}
      {draft.schedule.kind === 'cron' && field('Cron expression', draft.schedule.expression ?? '', expression => setDraft({ ...draft, schedule: { ...draft.schedule, expression } }), true)}
      {draft.schedule.kind === 'event' && field('Authenticated trigger source', draft.schedule.trigger_source ?? '', trigger_source => setDraft({ ...draft, schedule: { ...draft.schedule, trigger_source } }), true)}
      {draft.schedule.kind === 'interval' && <label className="flex flex-col">Interval seconds<input className={automationControl} type="number" min="1" required value={draft.schedule.interval_seconds ?? ''} onChange={event => setDraft({ ...draft, schedule: { ...draft.schedule, interval_seconds: Number(event.target.value) } })} /></label>}
      <label>Missed runs <select className={automationControl} value={draft.schedule.missed_policy} onChange={event => setDraft({ ...draft, schedule: { ...draft.schedule, missed_policy: event.target.value as 'skip' | 'coalesce' } })}><option>skip</option><option>coalesce</option></select></label>
      <label>Overlap <select className={automationControl} value={draft.schedule.overlap_policy} onChange={event => setDraft({ ...draft, schedule: { ...draft.schedule, overlap_policy: event.target.value as 'independent' | 'serialize' } })}><option>independent</option><option>serialize</option></select></label>
      <h3 className="font-semibold">Ordered plan associations</h3>
      <p>Exact canonical plan references; dependencies must precede their consumers.</p>
      {draft.plans.map((binding, index) => {
        const update = (next: typeof binding) => setDraft({ ...draft, plans: draft.plans.map((item, position) => position === index ? next : item) })
        return <fieldset key={index} className="space-y-2 rounded border border-[var(--app-border)] p-3"><legend>Plan {index + 1}</legend>
          {field('Binding ID', binding.id, id => update({ ...binding, id }), true)}
          {field('Session ID', binding.plan.session_id, session_id => update({ ...binding, plan: { ...binding.plan, session_id } }), true)}
          {field('Plan ID', binding.plan.plan_id, plan_id => update({ ...binding, plan: { ...binding.plan, plan_id } }), true)}
          <label>Revision<input className={automationControl} type="number" min="1" required value={binding.plan.revision} onChange={event => update({ ...binding, plan: { ...binding.plan, revision: Number(event.target.value) } })} /></label>
          {field('Depends on (comma-separated binding IDs)', (binding.depends_on ?? []).join(', '), value => update({ ...binding, depends_on: value.split(',').map(id => id.trim()).filter(Boolean) }))}
          <button className={automationControl} type="button" disabled={index === 0} onClick={() => { const plans = [...draft.plans]; [plans[index - 1], plans[index]] = [plans[index], plans[index - 1]]; setDraft({ ...draft, plans }) }}>Move plan {index + 1} up</button>{' '}
          <button className={automationControl} type="button" onClick={() => setDraft({ ...draft, plans: draft.plans.filter((_, position) => position !== index) })}>Remove plan {index + 1}</button>
        </fieldset>
      })}
      <button className={automationControl} type="button" disabled={draft.plans.length >= 16} onClick={() => setDraft({ ...draft, plans: [...draft.plans, { id: crypto.randomUUID(), plan: { session_id: '', plan_id: '', revision: 1 } }] })}>Add plan</button>
      {field('Allowed tools (comma-separated)', (draft.authorization.allowed_tools ?? []).join(', '), value => setDraft({ ...draft, authorization: { ...draft.authorization, allowed_tools: value.split(',').map(item => item.trim()).filter(Boolean) } }))}
      {field('Target IDs (comma-separated)', (draft.authorization.target_ids ?? []).join(', '), value => setDraft({ ...draft, authorization: { ...draft.authorization, target_ids: value.split(',').map(item => item.trim()).filter(Boolean) } }))}
      <label className="flex flex-col">Policy expiry (UTC epoch milliseconds)<input className={automationControl} type="number" min="1" step="1" value={draft.authorization.expires_at ?? ''} onChange={event => setDraft({ ...draft, authorization: { ...draft.authorization, expires_at: event.target.value ? Number(event.target.value) : undefined } })} /></label>
      <p>Authorization mode: {draft.authorization.mode}. Editing does not grant permission.</p>
      <button className={automationControl} type="submit">Save configuration</button>
    </fieldset>
    {error && <p role="alert">{error}</p>}
  </form>
}
