import { useEffect, useRef, useState } from 'react'
import { getDesktopSessionIdentitySnapshot } from '../../../app/api'
import { desktopWorkers, useWorkerPage } from '../runtime/desktop-workers'
import type { WorkerAutomation, WorkerAutomationInput, WorkerMutation, WorkerMutationResult, WorkerRecord, WorkerRun, WorkerSummary } from '../state/desktop-workers-api'

export interface SelectedWorker { id: string; revision: number; name: string }
const box = 'rounded-xl border border-slate-800 bg-slate-900/50 p-4 text-xs text-slate-300 space-y-2'
const button = 'rounded-lg border border-slate-700 bg-slate-800 px-2.5 py-1.5 text-xs text-slate-200 hover:bg-slate-700 disabled:opacity-50'
const input = 'rounded-lg border border-slate-700 bg-slate-950 px-2 py-1.5 text-xs text-white w-full'
const label = 'block text-[11px] font-semibold text-slate-400 mb-1'
const failure = (error: unknown) => error instanceof Error ? error.message : 'Worker operation failed'
const date = (value?: number) => value ? new Date(value).toLocaleString() : 'Not recorded'
const zone = 'UTC' // Explicit, stable across client timezones; the server computes the day boundary.
const today = () => new Date().toISOString().slice(0, 10)
function summaryText(summary?: WorkerSummary): string {
  if (!summary) return 'Summary unavailable.'
  const r = summary.runs
  return `${r.date} (${r.timezone}): ${r.truncated ? 'Partial counts' : 'Total'} ${r.daily_runs} runs · ${r.daily_success} succeeded · ${r.daily_failed} failed · ${r.daily_cancelled} cancelled · ${r.active_runs} active/admitted${r.active_truncated ? ' (active list truncated)' : ''}. Next eligible schedule: ${summary.next_scheduled_at ? date(summary.next_scheduled_at) : 'none reported'} (not a dispatch promise).`
}
export function formatWorkerSchedule(auto: WorkerAutomation): string {
  const s = auto.schedule
  if (!s) return auto.activation_mode === 'manual' ? 'Manual' : `${auto.activation_mode} (schedule unavailable)`
  if (s.kind === 'interval') return `Every ${s.interval_seconds ?? '?'} seconds (${s.timezone || 'timezone not specified'})`
  if (s.kind === 'cron') return `${s.cron || 'Cron not specified'} (${s.timezone || 'timezone not specified'})`
  return `External trigger (${auto.trigger?.trigger_kind || 'configuration unavailable'})`
}
/** Never regenerate an idempotency key on a transport failure. A changed draft starts a new intent. */
export function useWorkerIntentKeys() {
  const keys = useRef(new Map<string, string>())
  return {
    key: (intent: string) => { if (!keys.current.has(intent)) keys.current.set(intent, crypto.randomUUID()); return keys.current.get(intent)! },
    clear: (intent: string) => { keys.current.delete(intent) },
  }
}
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
  useEffect(() => { setSelectedId(initialWorkerId || '') }, [initialWorkerId])
  const [cursor, setCursor] = useState<string | undefined>()
  const [name, setName] = useState('')
  const [instructions, setInstructions] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const intents = useWorkerIntentKeys()
  const page = useWorkerPage({ kind: 'list', accountScopeId, cursor, limit: 25 })
  const workers = page?.data && 'workers' in page.data ? page.data.workers : []
  const next = page?.data && 'workers' in page.data ? page.data.next_cursor : undefined
  return <section className="flex-1 min-h-0 overflow-y-auto p-6 space-y-4 w-full max-w-5xl mx-auto" aria-label="Durable workers" data-testid="durable-worker-hub">
    <header className="flex items-center justify-between gap-3 border-b border-slate-800 pb-3"><div><h2 className="text-base font-bold text-white">Workers</h2><p>Account-owned durable workers. Select one to inspect or give the Orchestrator one-message context.</p></div><button type="button" className={button} onClick={() => desktopWorkers.invalidate(undefined, accountScopeId)}>Refresh</button></header>
    <form className={box} onSubmit={async e => {
      e.preventDefault(); if (busy) return
      setBusy(true); setError(''); setNotice('')
      try {
        const result = await desktopWorkers.mutate({ action: 'create', name, instructions, workspace_requirements: [{ role: 'primary', description: 'Primary workspace', required: true }], idempotency_key: intents.key('create') }, accountScopeId)
        if ('worker' in result) { intents.clear('create'); setSelectedId(result.worker.id); setNotice(`${result.worker.name} saved at revision ${result.worker.revision}.`); setName(''); setInstructions('') }
      } catch (cause) { setError(failure(cause)) } finally { setBusy(false) }
    }}><strong className="text-white">Create idle worker</strong><p>Requires a primary workspace role. Bind an approved local workspace ID before testing; creation does not run work.</p>
      <label><span className={label}>Name</span><input aria-label="New worker name" className={input} required value={name} onChange={e => { intents.clear('create'); setName(e.target.value) }} /></label>
      <label><span className={label}>Standing instructions</span><textarea aria-label="New worker instructions" className={input} value={instructions} onChange={e => { intents.clear('create'); setInstructions(e.target.value) }} /></label>
      <button className={button} disabled={busy} type="submit">Create worker</button>
    </form>
    {error && <p role="alert" className="text-red-300">{error}</p>}{notice && <p role="status" className="text-emerald-300">{notice}</p>}
    {page?.error && <p role="alert">Worker list: {page.error}</p>}{page?.mutationError && <p role="alert">Worker change: {page.mutationError}</p>}
    {page?.stale && <p className="text-amber-300">Worker list refreshing; displayed entries may be stale.</p>}
    {page?.loading && !page.data && <p role="status">Loading workers…</p>}
    <div className="grid grid-cols-1 md:grid-cols-2 gap-3">{workers.map(worker => <WorkerCard key={worker.id} worker={worker} accountScopeId={accountScopeId} selected={selectedId === worker.id} onClick={() => setSelectedId(worker.id)} />)}</div>
    {!page?.loading && !page?.error && !workers.length && <p>No durable workers on this page. Legacy automations are not automatically migrated.</p>}
    {next && <button className={button} onClick={() => setCursor(next)}>Next workers page</button>}{cursor && <button className={button} onClick={() => setCursor(undefined)}>First page</button>}
    {selectedId && <WorkerDetail key={selectedId} accountScopeId={accountScopeId} workerId={selectedId} workspaceSlug={workspaceSlug} onSelectWorker={onSelectWorker} onClose={() => setSelectedId('')} />}
  </section>
}
function WorkerCard({ worker, accountScopeId, selected, onClick }: { worker: WorkerRecord; accountScopeId: string; selected: boolean; onClick: () => void }) {
  const summaryPage = useWorkerPage({ kind: 'summary', accountScopeId, workerId: worker.id, timezone: zone, date: today() })
  const summary = summaryPage?.data && 'worker_id' in summaryPage.data ? summaryPage.data : undefined
  return <button type="button" onClick={onClick} className={`${box} text-left hover:border-blue-500/50 ${selected ? 'border-blue-500/60' : ''}`} aria-label={`Inspect ${worker.name}`}>
    <span className="font-semibold text-white">{worker.name}</span> <span>r{worker.revision}</span> {worker.lifecycle_state === 'pending' ? <span className="rounded bg-amber-500/20 text-amber-300 border border-amber-500/30 px-1.5 py-0.5 text-[10px] font-semibold">Pending acceptance</span> : <span>· {worker.lifecycle_state}</span>}
    <span className="block font-mono text-[10px]">{worker.id}</span><span className="block">{worker.description || 'No description'}</span>
    <span className="block">{worker.automations?.length ?? 0} attached automation(s)</span>
    <span className="block">{summaryPage?.error ? `Summary error: ${summaryPage.error}` : summaryPage?.stale ? 'Summary refreshing; values may be stale.' : summaryText(summary)}</span>
  </button>
}
export function WorkerDetail({ accountScopeId, workerId, workspaceSlug, onSelectWorker, onClose }: {
  accountScopeId: string; workerId: string; workspaceSlug?: string; onSelectWorker: (selection: SelectedWorker) => void; onClose: () => void
}) {
  const [cursor, setCursor] = useState<string | undefined>()
  const [historyCursor, setHistoryCursor] = useState<string | undefined>()
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [confirm, setConfirm] = useState<string | null>(null)
  const [editing, setEditing] = useState(false)
  const [name, setName] = useState('')
  const [instructions, setInstructions] = useState('')
  const [binding, setBinding] = useState('')
  const [prompt, setPrompt] = useState('')
  const [runInput, setRunInput] = useState('')
  const [automationName, setAutomationName] = useState('')
  const [planText, setPlanText] = useState('')
  const [mode, setMode] = useState<WorkerAutomation['activation_mode']>('manual')
  const [interval, setInterval] = useState('3600')
  const [cron, setCron] = useState('')
  const [timezone, setTimezone] = useState('UTC')
  const [triggerKind, setTriggerKind] = useState<'event' | 'webhook'>('event')
  const [triggerFormat, setTriggerFormat] = useState('generic')
  const [editAutomation, setEditAutomation] = useState<string | null>(null)
  const intents = useWorkerIntentKeys()
  const detail = useWorkerPage({ kind: 'detail', accountScopeId, workerId })
  const runsPage = useWorkerPage({ kind: 'runs', accountScopeId, workerId, cursor, limit: 25 })
  const historyPage = useWorkerPage({ kind: 'history', accountScopeId, workerId, cursor: historyCursor, limit: 10 })
  const summaryPage = useWorkerPage({ kind: 'summary', accountScopeId, workerId, timezone: zone, date: today() })
  const summary = summaryPage?.data && 'worker_id' in summaryPage.data ? summaryPage.data : undefined
  const worker = detail?.data && 'worker' in detail.data ? detail.data.worker : undefined
  const runs = runsPage?.data && 'runs' in runsPage.data && Array.isArray(runsPage.data.runs) ? runsPage.data.runs : []
  const nextRuns = runsPage?.data && 'next_cursor' in runsPage.data ? runsPage.data.next_cursor : undefined
  const revisions = historyPage?.data && 'revisions' in historyPage.data ? historyPage.data.revisions : []
  const nextHistory = historyPage?.data && 'revisions' in historyPage.data ? historyPage.data.next_cursor : undefined
  const readOnly = Boolean(worker?.provenance?.migrated_at)
  const mutate = async (make: (w: WorkerRecord) => WorkerMutation, message?: string): Promise<WorkerMutationResult | undefined> => {
    if (!worker || busy || readOnly || detail?.stale || detail?.error) return
    setBusy(true); setError(''); setNotice('')
    try {
      const result = await desktopWorkers.mutate(make(worker), accountScopeId)
      if ('worker' in result && result.worker.lifecycle_state === 'stopping') setNotice('Stopping: cancellation is awaiting acknowledgement; the requested lifecycle change is not complete.')
      else if (message) setNotice(message)
      return result
    } catch (cause) { setError(failure(cause)) } finally { setBusy(false) }
  }
  const launch = async (kind: 'direct' | 'test', automationId?: string) => {
    let parsed: Record<string, unknown> | undefined
    try { if (runInput.trim()) { const value: unknown = JSON.parse(runInput); if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error('Input must be a JSON object'); parsed = value as Record<string, unknown> } }
    catch (cause) { setError(`Invalid input JSON: ${failure(cause)}`); return }
    const intent = `${kind}:${automationId || ''}:${prompt}:${runInput}`
    const result = await mutate(w => kind === 'direct'
      ? { action: 'direct', workerId: w.id, prompt: prompt || undefined, input: parsed, idempotency_key: intents.key(intent) }
      : { action: 'test', workerId: w.id, automationId, prompt: prompt || undefined, input: parsed, idempotency_key: intents.key(intent) }, 'Run admitted.')
    if (result) intents.clear(intent)
  }
  const changeRunDraft = (field: 'prompt' | 'input', value: string) => {
    // A new draft must not accidentally reuse a prior failed admission key.
    for (const kind of ['direct', 'test'] as const) {
      intents.clear(`${kind}::${prompt}:${runInput}`)
      for (const auto of worker?.automations || []) intents.clear(`${kind}:${auto.id}:${prompt}:${runInput}`)
    }
    if (field === 'prompt') setPrompt(value); else setRunInput(value)
  }
  const editAuto = (auto: WorkerAutomation) => { setEditAutomation(auto.id); setAutomationName(auto.name); setPlanText(JSON.stringify(auto.plan_document, null, 2)); setMode(auto.activation_mode); setInterval(String(auto.schedule?.interval_seconds ?? 3600)); setCron(auto.schedule?.cron ?? ''); setTimezone(auto.schedule?.timezone ?? 'UTC'); setTriggerKind(auto.trigger?.trigger_kind === 'webhook' ? 'webhook' : 'event'); setTriggerFormat(auto.trigger?.format || 'generic') }
  return <article className={box} data-testid="durable-worker-detail">
    <div className="flex justify-between"><h3 className="text-white font-bold">{worker?.name || workerId}</h3><button className={button} onClick={onClose}>Close detail</button></div>
    {detail?.error && <p role="alert">Worker detail: {detail.error}. This worker may have been removed.</p>}{detail?.mutationError && <p role="alert">Worker change: {detail.mutationError}</p>}
    {detail?.stale && <p className="text-amber-300">Detail refreshing; do not act on stale revisions.</p>}
    {worker && <>
      <p className="font-mono">ID: {worker.id} · revision {worker.revision} · {worker.lifecycle_state}</p>
      {worker.lifecycle_state === 'stopping' && <p role="status">Stopping: awaiting active run acknowledgement. Refresh for the final lifecycle state.</p>}
      {readOnly && <p role="status">Migrated legacy snapshot: read-only. Live executor cutover is not available.</p>}
      <p>{worker.description || 'No description'}</p>
      <section><h4 className="font-bold text-white">Standing instructions</h4><p className="whitespace-pre-wrap break-words">{worker.instructions || 'No instructions'}</p></section>
      <section><h4 className="font-bold text-white">Workspace roles and bindings</h4><ul>{(worker.workspace_requirements || []).map(req => <li key={req.role}>{req.role}: {req.description || 'No description'} ({req.required ? 'required' : 'optional'})</li>)}</ul><p><span className="text-slate-400">Proposed workspaces: </span>{Object.entries(worker.proposed_bindings || {}).map(([role, id]) => `${role}: ${id}`).join(', ') || 'None'}</p><p><span className="text-slate-400">Approved local bindings: </span>{Object.entries(worker.local_bindings || {}).map(([role, id]) => `${role}: ${id}`).join(', ') || 'No approved binding'}</p></section>
      <section><h4 className="font-bold text-white">Capabilities</h4><p>Requested (not a grant): {(worker.requested_capabilities || []).map(cap => `${cap.type}/${cap.name}`).join(', ') || 'None'}</p><p>Approved grants are not reported by this API; activation rejects unsupported grants.</p></section>
      {!readOnly && <div className="flex flex-wrap gap-2">
        <button className={button} disabled={busy || !!detail?.stale} onClick={() => { setName(worker.name); setInstructions(worker.instructions); setEditing(!editing) }}>Edit definition</button>
        {worker.lifecycle_state === 'pending' && <button className="rounded-lg border border-emerald-600 bg-emerald-600/90 px-3 py-1.5 text-xs font-bold text-white hover:bg-emerald-500 disabled:opacity-50" disabled={busy || !!detail?.stale} onClick={() => void mutate(w => ({ action: 'accept', workerId: w.id, expected_revision: w.revision }), 'Worker accepted and activated.')} data-testid="accept-worker-hub-button">Accept worker</button>}
        {worker.lifecycle_state === 'idle' && <><button className={button} disabled={busy || !!detail?.stale || !binding.trim()} onClick={() => void mutate(w => ({ action: 'activate', workerId: w.id, expected_revision: w.revision, local_bindings: { primary: binding.trim() }, activate: false }), 'Binding approved; worker remains idle. Test before activation.')}>Approve binding (remain idle)</button><button className={button} disabled={busy || !!detail?.stale || !(binding.trim() || worker.local_bindings?.primary)} onClick={() => void mutate(w => ({ action: 'activate', workerId: w.id, expected_revision: w.revision, local_bindings: { primary: binding.trim() || worker.local_bindings!.primary }, activate: true }), 'Local activation accepted.')}>Activate locally</button></>}
        {(worker.lifecycle_state === 'paused' || worker.lifecycle_state === 'active') && <button className={button} disabled={busy || !!detail?.stale} onClick={() => void mutate(w => ({ action: w.lifecycle_state === 'active' ? 'pause' : 'resume', workerId: w.id, expected_revision: w.revision }), 'Lifecycle change confirmed.')}>{worker.lifecycle_state === 'active' ? 'Pause' : 'Resume'}</button>}
        {worker.lifecycle_state !== 'archived' && worker.lifecycle_state !== 'deleted' && worker.lifecycle_state !== 'stopping' && <button className={button} disabled={busy || !!detail?.stale} onClick={() => setConfirm('archive')}>Archive…</button>}
        {worker.lifecycle_state !== 'deleted' && worker.lifecycle_state !== 'stopping' && <button className={button} disabled={busy || !!detail?.stale} onClick={() => setConfirm('delete')}>Delete…</button>}
      </div>}
      {worker.lifecycle_state === 'idle' && !readOnly && <label><span className={label}>Approved primary workspace ID (not a path)</span><input className={input} aria-label="Approved primary workspace ID" value={binding} onChange={e => setBinding(e.target.value)} /></label>}
      {(confirm === 'archive' || confirm === 'delete') && <div role="group" aria-label={`Confirm ${confirm} worker`} className="rounded-lg border border-amber-700 p-2 space-x-2">{confirm} {worker.name}? Active runs must acknowledge stop before this completes.
        <button className={button} disabled={busy} onClick={() => { const action = confirm as 'archive' | 'delete'; void mutate(w => ({ action, workerId: w.id, expected_revision: w.revision })).then(result => { if (!result) return; setConfirm(null); if ('worker' in result && result.worker.lifecycle_state === 'stopping') setNotice('Stopping: awaiting active run acknowledgement.'); else if (action === 'delete' && 'deleted' in result) { setNotice('Delete confirmed.'); onClose() } else if ('worker' in result && result.worker.lifecycle_state === 'archived') setNotice('Archive confirmed.'); else setNotice('Lifecycle response pending; refresh to confirm.') }) }}>Confirm {confirm}</button>
        <button className={button} onClick={() => setConfirm(null)}>Keep worker</button></div>}
      {editing && <form className="space-y-2" onSubmit={e => { e.preventDefault(); void mutate(w => ({ action: 'update', workerId: w.id, expected_revision: w.revision, changes: { name, instructions } }), 'Definition saved.').then(result => { if (result) setEditing(false) }) }}><label><span className={label}>Name</span><input required className={input} value={name} onChange={e => setName(e.target.value)} /></label><label><span className={label}>Instructions</span><textarea className={input} value={instructions} onChange={e => setInstructions(e.target.value)} /></label><button disabled={busy || !!detail?.stale} className={button}>Save revision</button></form>}
      <button className={button} disabled={!!detail?.stale || !!detail?.error} onClick={() => onSelectWorker({ id: worker.id, revision: worker.revision, name: worker.name })}>Select for next Orchestrator message</button>
      <section className="space-y-2"><h4 className="font-bold text-white">Attached automations and plan templates</h4>
        {(worker.automations || []).map(auto => <div className="border border-slate-800 rounded-lg p-2 space-y-1" key={auto.id}><strong>{auto.name}</strong> · r{auto.revision} · {auto.enabled ? 'enabled' : 'disabled'}<p>{auto.description}</p><p>Activation: {formatWorkerSchedule(auto)}</p><p>Trigger: {auto.trigger?.trigger_kind || 'None'} (credential references are not displayed)</p><p>Plan: {auto.plan_document?.title || 'Untitled'}</p><ul>{auto.plan_document?.checkpoints?.map(cp => <li key={cp.id}>{cp.id}: {cp.title}</li>)}</ul><p>Input: {(auto.input_requirements || []).map(req => `${req.name} (${req.kind}${req.required ? ', required' : ''})`).join(', ') || 'No declared input'}</p><p>Deliverables: {(auto.deliverable_requirements || []).map(req => `${req.name} (${req.kind}${req.required ? ', required' : ''})`).join(', ') || 'No declared deliverables'}</p>
          {!readOnly && <><button className={button} disabled={busy || !!detail?.stale} onClick={() => void mutate(w => ({ action: auto.enabled ? 'disableAutomation' : 'enableAutomation', workerId: w.id, automationId: auto.id, expected_revision: w.revision }), 'Automation control confirmed.')}>{auto.enabled ? 'Disable automation' : 'Enable automation'}</button><button className={button} disabled={busy || !!detail?.stale} onClick={() => editAuto(auto)}>Edit automation</button><button className={button} disabled={busy || !!detail?.stale} onClick={() => setConfirm(`remove:${auto.id}`)}>Remove automation…</button><button className={button} disabled={busy || !!detail?.stale || !worker.local_bindings?.primary || worker.lifecycle_state === 'pending'} onClick={() => void launch('test', auto.id)}>Test automation (starts a run)</button></>}
          {confirm === `remove:${auto.id}` && <div role="group" aria-label={`Confirm remove ${auto.name}`}><button className={button} disabled={busy} onClick={() => void mutate(w => ({ action: 'removeAutomation', workerId: w.id, automationId: auto.id, expected_revision: w.revision }), 'Automation removed.').then(result => { if (result) setConfirm(null) })}>Confirm remove {auto.name}</button><button className={button} onClick={() => setConfirm(null)}>Keep automation</button></div>}
        </div>)}
        {!worker.automations?.length && <p>No automations attached.</p>}
        {!readOnly && <form className="space-y-2" onSubmit={e => { e.preventDefault(); let plan: WorkerAutomation['plan_document']; try { plan = JSON.parse(planText); if (!plan || typeof plan.title !== 'string' || !Array.isArray(plan.checkpoints)) throw new Error('Plan needs title and checkpoints') } catch (cause) { setError(`Invalid plan JSON: ${failure(cause)}`); return }
          const scheduleValue: WorkerAutomationInput['schedule'] = mode === 'interval' ? { kind: 'interval', interval_seconds: Number(interval), timezone } : mode === 'cron' ? { kind: 'cron', cron, timezone } : mode === 'external_trigger' ? { kind: 'trigger' } : undefined
          const old = worker.automations?.find(auto => auto.id === editAutomation)
          const automation: WorkerAutomationInput = { name: automationName, activation_mode: mode, schedule: scheduleValue, trigger: mode === 'external_trigger' ? { ...old?.trigger, trigger_kind: triggerKind, format: triggerFormat } : undefined, plan_document: plan, input_requirements: old?.input_requirements, deliverable_requirements: old?.deliverable_requirements, description: old?.description }
          void mutate(w => editAutomation ? { action: 'updateAutomation', workerId: w.id, automationId: editAutomation, expected_revision: w.revision, automation } : { action: 'attachAutomation', workerId: w.id, expected_revision: w.revision, automation }, editAutomation ? 'Automation updated.' : 'Automation attached.').then(result => { if (result) { setEditAutomation(null); setAutomationName(''); setPlanText('') } })
        }}><strong>{editAutomation ? 'Edit automation template' : 'Attach a plan template (does not run it)'}</strong><input required aria-label="Automation name" className={input} value={automationName} onChange={e => setAutomationName(e.target.value)} /><select aria-label="Activation mode" className={input} value={mode} onChange={e => setMode(e.target.value as typeof mode)}><option value="manual">Manual</option><option value="interval">Interval</option><option value="cron">Cron</option><option value="external_trigger">External trigger</option></select>
          {mode === 'interval' && <input aria-label="Interval seconds" type="number" min="60" className={input} value={interval} onChange={e => setInterval(e.target.value)} />}{mode === 'cron' && <input aria-label="Cron expression" className={input} value={cron} onChange={e => setCron(e.target.value)} />}{(mode === 'interval' || mode === 'cron') && <input aria-label="Schedule timezone" className={input} value={timezone} onChange={e => setTimezone(e.target.value)} />}
          {mode === 'external_trigger' && <><select className={input} aria-label="Trigger kind" value={triggerKind} onChange={e => setTriggerKind(e.target.value as typeof triggerKind)}><option value="event">Event</option><option value="webhook">Webhook</option></select><select className={input} aria-label="Trigger format" value={triggerFormat} onChange={e => setTriggerFormat(e.target.value)}><option value="generic">Generic</option><option value="slack">Slack</option><option value="discord">Discord</option><option value="github">GitHub</option></select><p>External trigger admission requires server authorization; this form does not provision a public endpoint or credential.</p></>}
          <textarea required aria-label="Plan document JSON" placeholder={'{"title":"Review","checkpoints":[]}'} className={input} value={planText} onChange={e => setPlanText(e.target.value)} /><button disabled={busy || !!detail?.stale} className={button}>{editAutomation ? 'Update automation' : 'Attach automation'}</button>{editAutomation && <button type="button" className={button} onClick={() => setEditAutomation(null)}>Cancel edit</button>}</form>}
      </section>
      {!readOnly && (
        worker.lifecycle_state === 'pending' ? (
          <p className="text-xs text-amber-300 italic" data-testid="detail-execution-blocked">
            Execution controls are blocked while worker is pending human acceptance.
          </p>
        ) : (
          <div className="space-y-2">
            <label><span className={label}>Explicit task prompt</span><input className={input} value={prompt} onChange={e => changeRunDraft('prompt', e.target.value)} /></label>
            <label><span className={label}>Run input JSON object (optional; see automation requirements above)</span><textarea className={input} aria-label="Run input JSON" value={runInput} onChange={e => changeRunDraft('input', e.target.value)} /></label>
            <button className={button} disabled={busy || (!prompt.trim() && !runInput.trim()) || !worker.local_bindings?.primary} onClick={() => void launch('direct')}>Send task (starts a run)</button>
            <button className={button} disabled={busy || !worker.local_bindings?.primary} onClick={() => void launch('test')}>Test worker (starts a run)</button>
            {!worker.local_bindings?.primary && <p>Approve a primary binding before starting runs.</p>}
          </div>
        )
      )}
    </>}
    {error && <p role="alert" className="text-red-300">{error}</p>}{notice && <p role="status" className="text-emerald-300">{notice}</p>}
    <section className="space-y-2"><h4 className="font-bold text-white">Current work and daily summary</h4>{summaryPage?.error && <p role="alert">Summary: {summaryPage.error}</p>}{summaryPage?.stale && <p>Summary refreshing; values may be stale.</p>}<p>{summaryText(summary)}</p>{summary?.runs.active.map(run => <p key={run.id}>{run.id} · {run.status} {run.session_id && <a className="text-blue-300 underline" href={workspaceSlug ? `/${encodeURIComponent(workspaceSlug)}/${encodeURIComponent(run.session_id)}` : `/${encodeURIComponent(run.session_id)}`}>Open session</a>}</p>)}</section>
    <section className="space-y-2"><h4 className="font-bold text-white">Run history (paged)</h4>{runsPage?.error && <p role="alert">Run history: {runsPage.error}</p>}{runs.map(run => <RunRow key={run.id} run={run} worker={worker} busy={busy} mutate={mutate} workspaceSlug={workspaceSlug} />)}{nextRuns && <button className={button} onClick={() => setCursor(nextRuns)}>Next runs page</button>}{cursor && <button className={button} onClick={() => setCursor(undefined)}>First runs page</button>}{runsPage?.loading && !runsPage.data && <p>Loading runs…</p>}</section>
    <section className="space-y-2"><h4 className="font-bold text-white">Definition revisions</h4>{historyPage?.error && <p role="alert">Revision history: {historyPage.error}</p>}{revisions.map(revision => <p key={revision.revision}>r{revision.revision} · {date(revision.committed_at)} · {revision.change_summary || 'No summary'}</p>)}{nextHistory && <button className={button} onClick={() => setHistoryCursor(nextHistory)}>Next revisions page</button>}{historyCursor && <button className={button} onClick={() => setHistoryCursor(undefined)}>First revisions page</button>}</section>
  </article>
}
function RunRow({ run, worker, busy, mutate, workspaceSlug }: { run: WorkerRun; worker?: WorkerRecord; busy: boolean; mutate: (make: (worker: WorkerRecord) => WorkerMutation, message?: string) => Promise<WorkerMutationResult | undefined>; workspaceSlug?: string }) {
  return <div className="border border-slate-800 rounded-lg p-2 space-y-1" data-testid="durable-worker-run"><p className="font-mono">{run.id} · {run.status}{run.cancel_requested ? ' · cancellation requested' : ''} · {date(run.created_at)}</p><p>Source: {run.request_source} · worker r{run.worker_revision} · automation {run.automation_id || 'none'}</p><p>Accepted input: {run.input ? JSON.stringify(run.input) : 'none recorded'}</p>{run.error && <p role="alert" className="text-red-300">{run.error}</p>}{run.session_id && <a className="text-blue-300 underline" href={workspaceSlug ? `/${encodeURIComponent(workspaceSlug)}/${encodeURIComponent(run.session_id)}` : `/${encodeURIComponent(run.session_id)}`}>Open execution session {run.session_id}</a>}
    {(run.deliverables || []).map((ref, i) => {
      const session = typeof ref.session_id === 'string' ? ref.session_id : run.session_id
      return <p key={i}>Deliverable reference: <code className="break-all">{JSON.stringify(ref)}</code>{session && <a className="text-blue-300 underline ml-2" href={workspaceSlug ? `/${encodeURIComponent(workspaceSlug)}/${encodeURIComponent(session)}` : `/${encodeURIComponent(session)}`}>Open source session</a>}</p>
    })}{worker && (run.status === 'admitted' || run.status === 'running') && <button className={button} disabled={busy} onClick={() => void mutate(w => ({ action: 'cancelRun', workerId: w.id, runId: run.id }), 'Cancellation requested; awaiting run confirmation.')}>Request run cancellation</button>}</div>
}
