import { useState } from 'react'
import type { WorkerRecord } from '../state/desktop-workers-api'
import { desktopWorkers } from '../runtime/desktop-workers'
import { displayModelName } from '../chat/services/model-options'
import { WorkerModelPicker, workerSlotInherited } from './worker-model-picker'
import { createWorkerSettingsDraft, reconcileWorkerSettingsDraft, workerSettingsChanges, workerSettingsValue, type WorkerSettingsValue } from './worker-settings-draft'

const button = 'rounded-lg border border-[var(--app-border)] px-3 py-2 text-xs font-medium hover:bg-[var(--app-border)] focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[var(--app-text)] disabled:opacity-40'
function settingLabel(value: WorkerSettingsValue, field: string) {
  if (field === 'Execution') return value.mode === 'plan' ? 'Plan' : 'Swarm'
  const slot = field === 'Action model' ? 'action' : 'plan'
  if (workerSlotInherited(value.profile, slot)) return 'Account default'
  const model = value.profile?.[slot]
  return model ? `${model.provider} / ${displayModelName(model.provider, model.model, model.context_mode || '')}${model.thinking ? ` · ${model.thinking}` : ''}${model.service_tier ? ` · ${model.service_tier}` : ''}${model.context_mode ? ` · ${model.context_mode}` : ''} · Custom` : 'Unconfigured'
}

/** Editing stages a revision; only the separate acceptance control authorizes it. */
export function WorkerSettingsReview({ worker, accountScopeId, disabled }: { worker: WorkerRecord; accountScopeId: string; disabled: boolean }) {
  // Identity changes reset synchronously, before another worker's draft can render.
  return <WorkerSettingsEditor key={`${accountScopeId}:${worker.account_scope_id}:${worker.id}`} worker={worker} accountScopeId={accountScopeId} disabled={disabled} />
}
function WorkerSettingsEditor({ worker, accountScopeId, disabled }: { worker: WorkerRecord; accountScopeId: string; disabled: boolean }) {
  const [stored, setDraft] = useState(() => createWorkerSettingsDraft(worker))
  const draft = reconcileWorkerSettingsDraft(stored, worker)
  if (draft !== stored) setDraft(draft)
  const current = createWorkerSettingsDraft(worker)
  const conflict = current.key !== draft.key && !(draft.saved && worker.revision < draft.revision)
  const changes = workerSettingsChanges(draft.baseline, draft.value)
  const dirty = changes.length > 0
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const pending = !!worker.pending_review || worker.lifecycle_state === 'pending' || draft.saved
  const blocked = disabled || busy || worker.account_scope_id !== accountScopeId
  const approved: WorkerSettingsValue = { mode: worker.execution_mode || 'auto', profile: worker.model_profile }
  const savedCandidate = workerSettingsValue(worker)
  const pendingChanges = worker.pending_review ? workerSettingsChanges(approved, savedCandidate) : []
  const discard = () => { setDraft(createWorkerSettingsDraft(worker)); setError('') }
  const propose = async () => {
    if (blocked || conflict || !dirty) return
    setBusy(true); setError('')
    try {
      const result = await desktopWorkers.mutate({ action: 'update', workerId: worker.id, expected_revision: draft.revision, changes: { execution_mode: draft.value.mode, ...(draft.value.profile ? { model_profile: draft.value.profile } : {}), change_summary: `${changes.join(', ')} proposed` } }, accountScopeId)
      if ('worker' in result) setDraft({ ...createWorkerSettingsDraft(result.worker), saved: true })
      else setError('Proposal response did not include the saved worker. Refresh before making further changes.')
    } catch (cause) { setError(cause instanceof Error ? cause.message : 'Model proposal failed. Your draft has been kept.') }
    finally { setBusy(false) }
  }
  return <section aria-label="Execution and model settings" className="min-w-0 space-y-4 rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)] p-4 text-[var(--app-text)]">
    <header className="flex flex-wrap items-center justify-between gap-3">
      <div className="flex flex-wrap items-center gap-2"><h3 className="text-sm font-semibold">Execution</h3><span className="rounded-full border border-[var(--app-border)] px-2 py-0.5 text-[10px] font-medium text-[var(--app-text-muted)]">{dirty ? 'Local draft' : pending ? 'Pending approval' : 'Approved'}</span></div>
      <div role="group" aria-label="Execution mode" className="flex rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-1">
        {(['auto', 'plan'] as const).map(mode => <button key={mode} type="button" aria-pressed={draft.value.mode === mode} disabled={blocked} className={`${button} border-0 ${draft.value.mode === mode ? 'bg-[var(--app-border)] font-semibold' : 'text-[var(--app-text-muted)]'}`} onClick={() => setDraft({ ...draft, saved: false, value: { ...draft.value, mode } })}>{mode === 'auto' ? 'Swarm' : 'Plan'}</button>)}
      </div>
    </header>
    <p className="text-xs text-[var(--app-text-muted)]">{draft.value.mode === 'plan' ? 'Plan first, then carry out the approved work.' : 'Work directly with the Action model.'}</p>
    <WorkerModelPicker accountScopeId={accountScopeId} profile={draft.value.profile} mode={draft.value.mode} disabled={blocked} onChange={profile => setDraft({ ...draft, saved: false, value: { ...draft.value, profile } })} />
    {pending && <div className="space-y-2 rounded-lg border border-[var(--app-border)] px-3 py-2 text-xs" aria-label="Pending model changes">
      <p className="font-medium">{worker.lifecycle_state === 'pending' ? 'Not yet accepted · nothing runs' : 'Pending approval · approved settings remain in effect'}</p>
      {!!pendingChanges.length && <ul className="space-y-1 text-[var(--app-text-muted)]">{pendingChanges.map(field => <li key={field} className="break-words [overflow-wrap:anywhere]"><span className="font-medium">{field}</span>: {settingLabel(approved, field)} → {settingLabel(savedCandidate, field)}</li>)}</ul>}
    </div>}
    {conflict && <p role="alert" className="text-xs">This worker changed while you were editing. Your draft is kept; discard it to load the latest revision before proposing.</p>}
    {dirty && <div aria-label="Local settings changes" className="flex flex-wrap items-center justify-between gap-3 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-3">
      <div className="min-w-0 text-xs"><p className="font-medium">{changes.join(' · ')}</p><p className="mt-1 text-[var(--app-text-muted)]">Requires acceptance · existing jobs stay unchanged.</p></div>
      <div className="flex flex-wrap gap-2"><button type="button" disabled={busy} onClick={discard} className={button}>Discard</button><button type="button" disabled={blocked || conflict} onClick={() => { void propose() }} className={`${button} bg-[var(--app-border)]`}>{busy ? 'Saving proposal…' : 'Propose changes'}</button></div>
    </div>}
    {!dirty && <p className="text-[11px] text-[var(--app-text-muted)]">Default follows your account. Custom applies only to this worker.</p>}
    {error && <p role="alert" className="break-words text-xs">{error}</p>}
  </section>
}
