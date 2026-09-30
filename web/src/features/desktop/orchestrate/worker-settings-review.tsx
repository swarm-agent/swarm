import { useState } from 'react'
import type { WorkerModelProfile, WorkerRecord } from '../state/desktop-workers-api'
import { desktopWorkers } from '../runtime/desktop-workers'
import { WorkerModelPicker, workerSlotInherited } from './worker-model-picker'

/** Editing a worker stages a revision; only the separate acceptance control authorizes it. */
export function WorkerSettingsReview({ worker, accountScopeId, disabled }: { worker: WorkerRecord; accountScopeId: string; disabled: boolean }) {
  const candidate = worker.pending_review || worker
  const [profile, setProfile] = useState<WorkerModelProfile | null | undefined>(candidate.model_profile)
  const [mode, setMode] = useState<'auto' | 'plan'>(candidate.execution_mode || 'auto')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  return <section aria-label="Execution and model settings" className="rounded-xl border border-slate-800 p-4 space-y-3">
    <h4>Execution and models</h4>
    <p>Approved settings: {worker.lifecycle_state === 'pending' ? 'Not yet accepted' : `${worker.execution_mode === 'plan' ? 'Plan' : 'Swarm'} · ${worker.model_profile?.use_account_default ? 'Account defaults' : 'Worker model policy'}`}. {worker.pending_review ? 'Pending settings are shown below; approved settings remain in effect.' : 'Edits below are a local draft until proposed and accepted.'}</p>
    {worker.lifecycle_state !== 'pending' && <section aria-label="Approved model policy">{(['action', 'plan'] as const).map(slot => <p key={slot}>Approved {slot === 'action' ? 'Action' : 'Plan'}: {workerSlotInherited(worker.model_profile, slot) ? 'Account default (follows future changes)' : `${worker.model_profile?.[slot]?.provider}/${worker.model_profile?.[slot]?.model} · worker override`}</p>)}</section>}
    <label className="block">Execution mode <select value={mode} disabled={disabled || busy || worker.account_scope_id !== accountScopeId} onChange={event => setMode(event.target.value as 'auto' | 'plan')} className="bg-slate-900 border border-slate-700 rounded p-2"><option value="auto">Swarm (default)</option><option value="plan">Plan (explicit planning before execution)</option></select></label>
    <WorkerModelPicker accountScopeId={accountScopeId} profile={profile} disabled={disabled || busy || worker.account_scope_id !== accountScopeId} onChange={setProfile} />
    <p>Changes require review before they apply. Existing approved jobs and account model settings are not changed by this proposal.</p>
    <button type="button" disabled={disabled || busy || worker.account_scope_id !== accountScopeId} onClick={async () => {
      setBusy(true); setError('')
      try { await desktopWorkers.mutate({ action: 'update', workerId: worker.id, expected_revision: worker.revision, changes: { execution_mode: mode, ...(profile ? { model_profile: profile } : {}), change_summary: 'Execution and model selection proposed' } }, accountScopeId) }
      catch (cause) { setError(cause instanceof Error ? cause.message : 'Model proposal failed') }
      finally { setBusy(false) }
    }} className="rounded border border-slate-700 px-3 py-2">{busy ? 'Saving proposal…' : 'Propose settings changes'}</button>
    {error && <p role="alert">{error}</p>}
  </section>
}
