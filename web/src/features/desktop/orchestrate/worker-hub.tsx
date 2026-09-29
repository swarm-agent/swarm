import { useState } from 'react'
import { getDesktopSessionIdentitySnapshot } from '../../../app/api'
import { desktopWorkers, useWorkerPage } from '../runtime/desktop-workers'
import type { WorkerAutomation, WorkerMutation, WorkerRecord, WorkerRun } from '../state/desktop-workers-api'

export interface SelectedWorker { id: string; revision: number; name: string }

function failure(error: unknown): string { return error instanceof Error ? error.message : 'Worker operation failed' }
function date(value?: number): string { return value ? new Date(value).toLocaleString() : 'Not recorded' }
function schedule(automation: WorkerAutomation): string {
  const s = automation.schedule
  if (!s) return automation.activation_mode === 'manual' ? 'Manual' : `${automation.activation_mode} (schedule unavailable)`
  if (s.kind === 'interval') return `Every ${s.interval_seconds ?? '?'} seconds (${s.timezone || 'timezone not specified'})`
  if (s.kind === 'cron') return `${s.cron || 'Cron not specified'} (${s.timezone || 'timezone not specified'})`
  return `External trigger (${automation.trigger?.trigger_kind || 'configuration unavailable'})`
}
const box = 'rounded-xl border border-slate-800 bg-slate-900/50 p-4 text-xs text-slate-300 space-y-2'
const button = 'rounded-lg border border-slate-700 bg-slate-800 px-2.5 py-1.5 text-xs text-slate-200 hover:bg-slate-700 disabled:opacity-50'
const input = 'rounded-lg border border-slate-700 bg-slate-950 px-2 py-1.5 text-xs text-white w-full'
const label = 'block text-[11px] font-semibold text-slate-400 mb-1'

/** This hub reads only the account-scoped V3 worker cache; legacy automation records are not workers. */
export function WorkerHub({ onSelectWorker, workspaceSlug, initialWorkerId }: {
  onSelectWorker: (selection: SelectedWorker) => void; workspaceSlug?: string; initialWorkerId?: string
}) {
  const accountScopeId = getDesktopSessionIdentitySnapshot()?.accountScopeId
  if (!accountScopeId) return <section role="alert" className={box}>Worker account identity is unavailable. Reconnect to view workers.</section>
  return <WorkerHubAccount key={accountScopeId} accountScopeId={accountScopeId} onSelectWorker={onSelectWorker} workspaceSlug={workspaceSlug} initialWorkerId={initialWorkerId} />
}

function WorkerHubAccount({ accountScopeId, onSelectWorker, workspaceSlug, initialWorkerId }: {
  accountScopeId: string; onSelectWorker: (selection: SelectedWorker) => void; workspaceSlug?: string; initialWorkerId?: string
}) {
  const [selectedId, setSelectedId] = useState(initialWorkerId || '')
  const [cursor, setCursor] = useState<string | undefined>()
  const [name, setName] = useState('')
  const [instructions, setInstructions] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const page = useWorkerPage({ kind: 'list', accountScopeId, cursor, limit: 25 })
  const workers = page?.data && 'workers' in page.data ? page.data.workers : []
  const next = page?.data && 'workers' in page.data ? page.data.next_cursor : undefined
  const perform = async (mutation: WorkerMutation) => {
    setBusy(true); setError(''); setNotice('')
    try {
      const result = await desktopWorkers.mutate(mutation, accountScopeId)
      if ('worker' in result) { setSelectedId(result.worker.id); setNotice(`${result.worker.name} saved at revision ${result.worker.revision}.`) }
      return true
    } catch (cause) { setError(failure(cause)); return false }
    finally { setBusy(false) }
  }
  return <section className="flex-1 min-h-0 overflow-y-auto p-6 space-y-4 w-full max-w-5xl mx-auto" aria-label="Durable workers" data-testid="durable-worker-hub">
    <header className="flex items-center justify-between gap-3 border-b border-slate-800 pb-3">
      <div><h2 className="text-base font-bold text-white">Workers</h2><p className="text-xs text-slate-400">Account-owned durable workers. Select one to inspect or give the Orchestrator one-message context.</p></div>
      <button type="button" className={button} onClick={() => void desktopWorkers.refresh({ kind: 'list', accountScopeId, cursor, limit: 25 })}>Refresh</button>
    </header>
    <form className={box} onSubmit={async e => { e.preventDefault(); if (await perform({ action: 'create', name, instructions, idempotency_key: crypto.randomUUID() })) { setName(''); setInstructions('') } }}>
      <strong className="text-white">Create idle worker</strong><p>No automation is attached and creation does not run work.</p>
      <label><span className={label}>Name</span><input aria-label="New worker name" className={input} required value={name} onChange={e => setName(e.target.value)} /></label>
      <label><span className={label}>Standing instructions</span><textarea aria-label="New worker instructions" className={input} value={instructions} onChange={e => setInstructions(e.target.value)} /></label>
      <button className={button} disabled={busy} type="submit">Create worker</button>
    </form>
    {error && <p role="alert" className="text-red-300">{error}</p>}{notice && <p role="status" className="text-emerald-300">{notice}</p>}
    {page?.error && <p role="alert" className="text-red-300">Worker list: {page.error}</p>}
    {page?.mutationError && <p role="alert" className="text-red-300">Worker change: {page.mutationError}</p>}
    {page?.stale && <p className="text-amber-300 text-xs">Worker list refreshing; displayed entries may be stale.</p>}
    {page?.loading && !page.data && <p role="status" className="text-slate-400 text-xs">Loading workers…</p>}
    <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
      {workers.map(worker => <button key={worker.id} type="button" onClick={() => setSelectedId(worker.id)} className={`${box} text-left hover:border-blue-500/50 ${selectedId === worker.id ? 'border-blue-500/60' : ''}`} aria-label={`Inspect ${worker.name}`}>
        <span className="font-semibold text-white">{worker.name}</span> <span className="text-slate-400">r{worker.revision} · {worker.lifecycle_state}</span>
        <span className="block font-mono text-[10px]">{worker.id}</span><span className="block">{worker.description || 'No description'}</span>
        <span className="block">{worker.automations?.length ?? 0} attached automation(s) · Current work and daily totals in detail</span>
      </button>)}
    </div>
    {!page?.loading && !page?.error && workers.length === 0 && <p className="text-xs text-slate-400">No durable workers on this page. Legacy automations are not automatically migrated.</p>}
    {next && <button className={button} onClick={() => setCursor(next)}>Next workers page</button>}
    {cursor && <button className={button} onClick={() => setCursor(undefined)}>First page</button>}
    {selectedId && <WorkerDetail key={selectedId} accountScopeId={accountScopeId} workerId={selectedId} workspaceSlug={workspaceSlug} onSelectWorker={onSelectWorker} onClose={() => setSelectedId('')} />}
  </section>
}

export function WorkerDetail({ accountScopeId, workerId, workspaceSlug, onSelectWorker, onClose }: {
  accountScopeId: string; workerId: string; workspaceSlug?: string; onSelectWorker: (selection: SelectedWorker) => void; onClose: () => void
}) {
  const [cursor, setCursor] = useState<string | undefined>()
  const [historyCursor, setHistoryCursor] = useState<string | undefined>()
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [confirm, setConfirm] = useState<'archive' | 'delete' | null>(null)
  const [editing, setEditing] = useState(false)
  const [name, setName] = useState('')
  const [instructions, setInstructions] = useState('')
  const [binding, setBinding] = useState('')
  const [prompt, setPrompt] = useState('')
  const [automationName, setAutomationName] = useState('')
  const [planText, setPlanText] = useState('')
  const [mode, setMode] = useState<'manual' | 'interval' | 'cron' | 'external_trigger'>('manual')
  const [interval, setInterval] = useState('3600')
  const [cron, setCron] = useState('')
  const [timezone, setTimezone] = useState('UTC')
  const detail = useWorkerPage({ kind: 'detail', accountScopeId, workerId })
  const runsPage = useWorkerPage({ kind: 'runs', accountScopeId, workerId, cursor, limit: 25 })
  const historyPage = useWorkerPage({ kind: 'history', accountScopeId, workerId, cursor: historyCursor, limit: 10 })
  const worker = detail?.data && 'worker' in detail.data ? detail.data.worker : undefined
  const runs = runsPage?.data && 'runs' in runsPage.data ? runsPage.data.runs : []
  const nextRuns = runsPage?.data && 'runs' in runsPage.data ? runsPage.data.next_cursor : undefined
  const nextHistory = historyPage?.data && 'revisions' in historyPage.data ? historyPage.data.next_cursor : undefined
  const revisions = historyPage?.data && 'revisions' in historyPage.data ? historyPage.data.revisions : []
  const readOnly = Boolean(worker?.provenance?.migrated_at)
  const mutate = async (make: (worker: WorkerRecord) => WorkerMutation, message?: string) => {
    if (!worker || busy || readOnly || detail?.stale || detail?.error) return false
    setBusy(true); setError(''); setNotice('')
    try {
      await desktopWorkers.mutate(make(worker), accountScopeId)
      if (message) setNotice(message)
      return true
    } catch (cause) { setError(failure(cause)); return false }
    finally { setBusy(false) }
  }
  const today = new Date().toLocaleDateString()
  const todayRuns = runs.filter(run => new Date(run.created_at).toLocaleDateString() === today)
  return <article className={box} data-testid="durable-worker-detail">
    <div className="flex justify-between"><h3 className="text-white font-bold">{worker?.name || workerId}</h3><button className={button} onClick={onClose}>Close detail</button></div>
    {detail?.error && <p role="alert" className="text-red-300">Worker detail: {detail.error}. This worker may have been removed.</p>}
    {detail?.mutationError && <p role="alert" className="text-red-300">Worker change: {detail.mutationError}</p>}
    {detail?.stale && <p className="text-amber-300">Detail refreshing; do not act on stale revisions.</p>}
    {worker && <>
      <p className="font-mono">ID: {worker.id} · revision {worker.revision} · {worker.lifecycle_state}</p>
      {readOnly && <p role="status" className="text-amber-300">Migrated legacy snapshot: read-only. Live executor cutover is not available.</p>}
      <p>{worker.description || 'No description'}</p>
      <section><h4 className="font-bold text-white">Standing instructions</h4><p className="whitespace-pre-wrap break-words">{worker.instructions || 'No instructions'}</p></section>
      <section><h4 className="font-bold text-white">Workspace roles and approved local bindings</h4>
        <ul>{(worker.workspace_requirements || []).map(req => <li key={req.role}>{req.role}: {req.description || 'No description'} ({req.required ? 'required' : 'optional'})</li>)}</ul>
        <p>{Object.entries(worker.local_bindings || {}).map(([role, id]) => `${role}: ${id}`).join(', ') || 'No approved binding'}</p>
      </section>
      <section><h4 className="font-bold text-white">Capabilities</h4><p>Requested (not a grant): {(worker.requested_capabilities || []).map(cap => `${cap.type}/${cap.name}`).join(', ') || 'None'}</p><p>Approved capability grants: not reported by this API; activation rejects unsupported grants.</p></section>
      {!readOnly && <div className="flex flex-wrap gap-2">
        <button className={button} disabled={busy || !!detail?.stale} onClick={() => { setName(worker.name); setInstructions(worker.instructions); setEditing(!editing) }}>Edit definition</button>
        {worker.lifecycle_state === 'idle' && <button className={button} disabled={busy || !!detail?.stale || !binding.trim()} onClick={() => void mutate(w => ({ action: 'activate', workerId: w.id, expected_revision: w.revision, local_bindings: { primary: binding.trim() } }), 'Local activation accepted.')}>Activate locally</button>}
        {(worker.lifecycle_state === 'paused' || worker.lifecycle_state === 'active') && <button className={button} disabled={busy || !!detail?.stale} onClick={() => void mutate(w => ({ action: w.lifecycle_state === 'active' ? 'pause' : 'resume', workerId: w.id, expected_revision: w.revision }), 'Lifecycle change confirmed.')}>{worker.lifecycle_state === 'active' ? 'Pause' : 'Resume'}</button>}
        {worker.lifecycle_state !== 'archived' && worker.lifecycle_state !== 'deleted' && <button className={button} disabled={busy || !!detail?.stale} onClick={() => setConfirm('archive')}>Archive…</button>}
        {worker.lifecycle_state !== 'deleted' && <button className={button} disabled={busy || !!detail?.stale} onClick={() => setConfirm('delete')}>Delete…</button>}
      </div>}
      {worker.lifecycle_state === 'idle' && !readOnly && <label><span className={label}>Approved primary workspace ID for local activation (not a path)</span><input className={input} aria-label="Approved primary workspace ID" value={binding} onChange={e => setBinding(e.target.value)} /></label>}
      {confirm && <div role="group" aria-label={`Confirm ${confirm} worker`} className="rounded-lg border border-amber-700 p-2 space-x-2">{confirm} {worker.name}? Active runs must acknowledge stop before this completes.
        <button className={button} disabled={busy} onClick={() => { const action = confirm; void mutate(w => ({ action, workerId: w.id, expected_revision: w.revision }), `${action} confirmed.`).then(ok => { if (ok) { setConfirm(null); if (action === 'delete') onClose() } }) }}>Confirm {confirm}</button>
        <button className={button} onClick={() => setConfirm(null)}>Keep worker</button></div>}
      {editing && <form className="space-y-2" onSubmit={e => { e.preventDefault(); void mutate(w => ({ action: 'update', workerId: w.id, expected_revision: w.revision, changes: { name, instructions } }), 'Definition saved.').then(ok => { if (ok) setEditing(false) }) }}>
        <label><span className={label}>Name</span><input required className={input} value={name} onChange={e => setName(e.target.value)} /></label><label><span className={label}>Instructions</span><textarea className={input} value={instructions} onChange={e => setInstructions(e.target.value)} /></label><button disabled={busy || !!detail?.stale} className={button}>Save revision</button></form>}
      <div className="flex flex-wrap gap-2"><button className={button} disabled={!!detail?.stale || !!detail?.error} onClick={() => onSelectWorker({ id: worker.id, revision: worker.revision, name: worker.name })}>Select for next Orchestrator message</button></div>
      <section className="space-y-2"><h4 className="font-bold text-white">Attached automations and plan templates</h4>
        {(worker.automations || []).map(auto => <div className="border border-slate-800 rounded-lg p-2 space-y-1" key={auto.id}>
          <strong>{auto.name}</strong> · r{auto.revision} · {auto.enabled ? 'enabled' : 'disabled'}<p>{auto.description}</p><p>Activation: {schedule(auto)}</p><p>Trigger: {auto.trigger?.trigger_kind || 'None'} (credential references are not displayed)</p>
          <p>Plan: {auto.plan_document?.title || 'Untitled'}</p><ul>{auto.plan_document?.checkpoints?.map(cp => <li key={cp.id}>{cp.id}: {cp.title}</li>)}</ul>
          {!readOnly && <button className={button} disabled={busy || !!detail?.stale} onClick={() => void mutate(w => ({ action: auto.enabled ? 'disableAutomation' : 'enableAutomation', workerId: w.id, automationId: auto.id, expected_revision: w.revision }), 'Automation control confirmed.')}>{auto.enabled ? 'Disable automation' : 'Enable automation'}</button>}
          {!readOnly && <button className={button} disabled={busy || !!detail?.stale} onClick={() => void mutate(w => ({ action: 'test', workerId: w.id, automationId: auto.id, idempotency_key: crypto.randomUUID() }), 'Test run admitted.')}>Test automation (starts a run)</button>}
        </div>)}
        {!worker.automations?.length && <p>No automations attached.</p>}
        {!readOnly && <form className="space-y-2" onSubmit={e => { e.preventDefault(); let plan: WorkerAutomation['plan_document']; try { plan = JSON.parse(planText); if (!plan || typeof plan.title !== 'string' || !Array.isArray(plan.checkpoints)) throw new Error('Plan needs title and checkpoints') } catch (cause) { setError(`Invalid plan JSON: ${failure(cause)}`); return }
          const scheduleValue: WorkerAutomation['schedule'] = mode === 'interval' ? { kind: 'interval', interval_seconds: Number(interval), timezone } : mode === 'cron' ? { kind: 'cron', cron, timezone } : mode === 'external_trigger' ? { kind: 'trigger' } : undefined
          void mutate(w => ({ action: 'attachAutomation', workerId: w.id, expected_revision: w.revision, automation: { name: automationName, activation_mode: mode, schedule: scheduleValue, trigger: mode === 'external_trigger' ? { trigger_kind: 'webhook' } : undefined, plan_document: plan } }), 'Automation attached.')
        }}><strong>Attach a plan template (does not run it)</strong><input required aria-label="Automation name" className={input} value={automationName} onChange={e => setAutomationName(e.target.value)} /><select aria-label="Activation mode" className={input} value={mode} onChange={e => setMode(e.target.value as typeof mode)}><option value="manual">Manual</option><option value="interval">Interval</option><option value="cron">Cron</option><option value="external_trigger">External trigger</option></select>
          {mode === 'interval' && <input aria-label="Interval seconds" type="number" min="60" className={input} value={interval} onChange={e => setInterval(e.target.value)} />}{mode === 'cron' && <input aria-label="Cron expression" className={input} value={cron} onChange={e => setCron(e.target.value)} />}
          {(mode === 'interval' || mode === 'cron') && <input aria-label="Schedule timezone" className={input} value={timezone} onChange={e => setTimezone(e.target.value)} />}
          {mode === 'external_trigger' && <p>External trigger registration needs a supported server-side trigger configuration; invalid drafts are rejected without activation.</p>}
          <textarea required aria-label="Plan document JSON" placeholder={'{"title":"Review","checkpoints":[]}'} className={input} value={planText} onChange={e => setPlanText(e.target.value)} /><button disabled={busy || !!detail?.stale} className={button}>Attach automation</button></form>}
      </section>
      {!readOnly && <div className="space-y-2"><label><span className={label}>Explicit task prompt</span><input className={input} value={prompt} onChange={e => setPrompt(e.target.value)} /></label><button className={button} disabled={busy || !prompt.trim()} onClick={() => void mutate(w => ({ action: 'direct', workerId: w.id, prompt, idempotency_key: crypto.randomUUID() }), 'Task run admitted.')}>Send task (starts a run)</button><button className={button} disabled={busy} onClick={() => void mutate(w => ({ action: 'test', workerId: w.id, idempotency_key: crypto.randomUUID() }), 'Test run admitted.')}>Test worker (starts a run)</button></div>}
    </>}
    {error && <p role="alert" className="text-red-300">{error}</p>}{notice && <p role="status" className="text-emerald-300">{notice}</p>}
    <section className="space-y-2"><h4 className="font-bold text-white">Run history</h4>
      {runsPage?.error && <p role="alert" className="text-red-300">Run history: {runsPage.error}</p>}
      <p>Today ({Intl.DateTimeFormat().resolvedOptions().timeZone}): {todayRuns.length} run(s) on this page · {todayRuns.filter(r => r.status === 'succeeded').length} succeeded · {todayRuns.filter(r => r.status === 'failed').length} failed · {todayRuns.filter(r => r.status === 'cancelled').length} cancelled. Page counts only, not full daily totals. {nextRuns ? 'More pages available.' : 'No further page reported.'}</p>
      <p>Current work: {runs.filter(r => r.status === 'running' || r.status === 'admitted').length} active/admitted run(s) on this page. Next scheduled run: not reported by the durable worker API.</p>
      {runs.map(run => <RunRow key={run.id} run={run} worker={worker} busy={busy} mutate={mutate} workspaceSlug={workspaceSlug} />)}
      {nextRuns && <button className={button} onClick={() => setCursor(nextRuns)}>Next runs page</button>}{cursor && <button className={button} onClick={() => setCursor(undefined)}>First runs page</button>}
      {runsPage?.loading && !runsPage.data && <p>Loading runs…</p>}
    </section>
    <section className="space-y-2"><h4 className="font-bold text-white">Definition revisions</h4>{historyPage?.error && <p role="alert">Revision history: {historyPage.error}</p>}
      {revisions.map(revision => <p key={revision.revision}>r{revision.revision} · {date(revision.committed_at)} · {revision.change_summary || 'No summary'}</p>)}
      {nextHistory && <button className={button} onClick={() => setHistoryCursor(nextHistory)}>Next revisions page</button>}{historyCursor && <button className={button} onClick={() => setHistoryCursor(undefined)}>First revisions page</button>}
    </section>
  </article>
}

function RunRow({ run, worker, busy, mutate, workspaceSlug }: { run: WorkerRun; worker?: WorkerRecord; busy: boolean; mutate: (make: (worker: WorkerRecord) => WorkerMutation, message?: string) => Promise<boolean>; workspaceSlug?: string }) {
  return <div className="border border-slate-800 rounded-lg p-2 space-y-1" data-testid="durable-worker-run">
    <p className="font-mono">{run.id} · {run.status}{run.cancel_requested ? ' · cancellation requested' : ''} · {date(run.created_at)}</p>
    <p>Source: {run.request_source} · worker r{run.worker_revision} · automation {run.automation_id || 'none'}</p>
    {run.error && <p role="alert" className="text-red-300">{run.error}</p>}
    {run.session_id && <a className="text-blue-300 underline" href={workspaceSlug ? `/${encodeURIComponent(workspaceSlug)}/${encodeURIComponent(run.session_id)}` : `/${encodeURIComponent(run.session_id)}`}>Open execution session {run.session_id}</a>}
    {(run.deliverables || []).map((ref, i) => <p key={i}>Deliverable reference: <code className="break-all">{JSON.stringify(ref)}</code></p>)}
    {worker && (run.status === 'admitted' || run.status === 'running') && <button className={button} disabled={busy} onClick={() => void mutate(w => ({ action: 'cancelRun', workerId: w.id, runId: run.id }), 'Cancellation requested; awaiting run confirmation.')}>Request run cancellation</button>}
  </div>
}
