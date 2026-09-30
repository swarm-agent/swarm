import { useState } from 'react'
import type { WorkerModelProfile, WorkerRecord } from '../state/desktop-workers-api'
import { desktopWorkers } from '../runtime/desktop-workers'
import { WorkerModelPicker } from './worker-model-picker'

/** Editing a worker stages a revision; only the separate acceptance control authorizes it. */
export function WorkerSettingsReview({ worker, accountScopeId, disabled }: { worker: WorkerRecord; accountScopeId: string; disabled: boolean }) {
  const candidate = worker.pending_review || worker
  const [profile, setProfile] = useState<WorkerModelProfile | null | undefined>(candidate.model_profile)
  const [mode, setMode] = useState<'auto' | 'plan'>(candidate.execution_mode || 'auto')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  return <details className="rounded-xl border border-slate-800 p-4 space-y-3">
    <summary className="cursor-pointer">Change execution and model</summary>
    <WorkerModelPicker accountScopeId={accountScopeId} profile={profile} disabled={disabled || busy} onChange={setProfile} />
    <label className="block">Execution mode <select value={mode} disabled={disabled || busy} onChange={event => setMode(event.target.value as 'auto' | 'plan')} className="bg-slate-900 border border-slate-700 rounded p-2"><option value="auto">Swarm (default)</option><option value="plan">Plan (explicit planning before execution)</option></select></label>
    <p>Changes require review before they apply. Existing approved jobs and account model settings are not changed by this proposal.</p>
    <button type="button" disabled={disabled || busy || worker.account_scope_id !== accountScopeId} onClick={async () => {
      setBusy(true); setError('')
      try { await desktopWorkers.mutate({ action: 'update', workerId: worker.id, expected_revision: worker.revision, changes: { execution_mode: mode, ...(profile ? { model_profile: profile } : {}), change_summary: 'Execution and model selection proposed' } }, accountScopeId) }
      catch (cause) { setError(cause instanceof Error ? cause.message : 'Model proposal failed') }
      finally { setBusy(false) }
    }} className="rounded border border-slate-700 px-3 py-2">{busy ? 'Saving proposal…' : 'Propose settings changes'}</button>
    {error && <p role="alert">{error}</p>}
  </details>
}
