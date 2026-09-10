import { useEffect, useMemo, useRef, useState } from 'react'
import { desktopAutomations, useAutomationPage } from '../../runtime/desktop-automations'
import type { AutomationMutation, AutomationRead, AutomationRecord } from '../../state/desktop-automation-api'
import { automationPageKey } from '../../state/desktop-automation-state'
import { AutomationEditor, automationControl as control } from './automation-editor'
import { AutomationChat } from './automation-chat'
import { dayKey, groupUpdates, needsAttention, nextDayDelay, parseInstructions } from './automation-view'

function usePage(input: AutomationRead) {
  const key = automationPageKey(input)
  const stable = useMemo(() => input, [key]) // Canonical key includes every query field.
  const page = useAutomationPage(stable)
  useEffect(() => { const lease = desktopAutomations.acquire(stable); return lease.release }, [stable])
  return page
}
function PageStatus({ input }: { input: AutomationRead }) {
  const page = useAutomationPage(input)
  return <><span role="status">{!page || page.loading ? 'Loading…' : page.stale ? 'Updates are stale.' : ''}</span>{page?.error && <p role="alert">{page.error} <button className={control} onClick={() => void desktopAutomations.refresh(input)}>Retry</button></p>}</>
}
function useMutation() {
  const lock = useRef(false)
  const active = useRef(true)
  const [pending, setPending] = useState(false)
  const [message, setMessage] = useState('')
  useEffect(() => { active.current = true; return () => { active.current = false } }, [])
  async function mutate(input: AutomationMutation) {
    if (lock.current) throw new Error('An operation is already pending.')
    lock.current = true; setPending(true); setMessage('')
    try {
      const result = await desktopAutomations.mutate(input)
      if (active.current) setMessage(input.action === 'run' ? 'Run admitted. Execution has not necessarily started.' : 'Request recorded.')
      return result
    } catch (error) {
      if (active.current) setMessage(error instanceof Error ? error.message : 'Operation failed.')
      throw error
    } finally { lock.current = false; if (active.current) setPending(false) }
  }
  return { mutate, pending, message }
}

export function AutomationWorkspace({ workspaceId, workspaceName, workspaceSlug }: { workspaceId: string; workspaceName: string; workspaceSlug: string }) {
  const [selected, setSelected] = useState('')
  const [creating, setCreating] = useState(false)
  const [cursor, setCursor] = useState<string>()
  const input: AutomationRead = { workspace_id: workspaceId, action: 'list', cursor, limit: 20 }
  const page = usePage(input)
  return <div className="flex min-h-dvh flex-col bg-[var(--app-bg)] text-[var(--app-text)] lg:flex-row">
    <nav aria-label="Automations" className="space-y-3 border-b border-[var(--app-border)] p-4 lg:w-64 lg:shrink-0 lg:border-r lg:border-b-0">
      <a className={control} href={`/${encodeURIComponent(workspaceSlug)}`}>Back to workspace</a>
      <h1 className="mt-4 text-xl font-semibold">Automations</h1><p>{workspaceName}</p>
      <button className={control} onClick={() => { setCreating(true); setSelected('') }}>New automation</button>
      <button className={control} onClick={() => { setCreating(false); setSelected('') }}>All updates</button>
      <PageStatus input={input} />
      <ul>{page?.data?.records?.map(record => <li key={record.id}><button className={`${control} my-1 w-full text-left break-words`} aria-current={selected === record.id ? 'page' : undefined} onClick={() => { setSelected(record.id); setCreating(false) }}>{record.definition?.name ?? record.id}<span className="block text-xs">{record.definition?.enabled ? 'Enabled' : 'Paused'}</span></button></li>)}</ul>
      {page?.data && !page.data.records?.length && <p>No automations on this page.</p>}
      {cursor && <button className={control} onClick={() => setCursor(undefined)}>First automations</button>}
      {page?.data?.next_cursor && <button className={control} disabled={page.loading || page.stale} onClick={() => setCursor(page.data?.next_cursor)}>More automations</button>}
    </nav>
    {creating ? <CreateAutomation key={workspaceId} workspaceId={workspaceId} onCreated={id => { setSelected(id); setCreating(false) }} /> : <AutomationDetail key={`${workspaceId}:${selected}`} workspaceId={workspaceId} id={selected} />}
  </div>
}
function CreateAutomation({ workspaceId, onCreated }: { workspaceId: string; onCreated: (id: string) => void }) {
  const [id] = useState(() => crypto.randomUUID())
  const mounted = useRef(true)
  useEffect(() => { mounted.current = true; return () => { mounted.current = false } }, [])
  const operation = useMutation()
  return <main className="min-w-0 flex-1 p-6"><h2 className="mb-4 text-xl">New automation</h2><AutomationEditor disabled={operation.pending} onSave={async definition => {
    await operation.mutate({ action: 'save', workspace_id: workspaceId, id, mutation_id: crypto.randomUUID(), expected_revision: 0, definition })
    if (mounted.current) onCreated(id)
  }} /></main>
}
function AutomationDetail({ workspaceId, id }: { workspaceId: string; id: string }) {
  const [tab, setTab] = useState('Updates')
  const [session, setSession] = useState('')
  const [timezone, setTimezone] = useState(() => Intl.DateTimeFormat().resolvedOptions().timeZone)
  const [now, setNow] = useState(Date.now)
  const definitionInput: AutomationRead = { workspace_id: workspaceId, action: 'list', id: id || undefined, limit: 1 }
  const definitionPage = usePage(definitionInput)
  const record = id ? definitionPage?.data?.records?.find(row => row.automation_id === id) : undefined
  const operation = useMutation()
  useEffect(() => {
    const timer = window.setTimeout(() => setNow(Date.now()), nextDayDelay(Date.now(), timezone))
    const resume = () => { if (document.visibilityState === 'visible') setNow(Date.now()) }
    document.addEventListener('visibilitychange', resume)
    return () => { clearTimeout(timer); document.removeEventListener('visibilitychange', resume) }
  }, [now, timezone])
  const disabled = operation.pending || !record || !!definitionPage?.stale || !!definitionPage?.loading
  const act = (action: 'enable' | 'pause' | 'run') => {
    if (!record || disabled) return
    void operation.mutate({ workspace_id: workspaceId, id, mutation_id: crypto.randomUUID(), expected_revision: record.revision, ...(action === 'run' ? { action, scheduled_at: Date.now() } : { action }) }).catch(() => {})
  }
  return <div className="flex min-w-0 flex-1 flex-col lg:flex-row"><main className="min-w-0 flex-1 space-y-5 p-4 lg:p-6">
    <header><h2 className="text-2xl font-semibold break-words">{id ? record?.definition?.name ?? 'Automation' : 'Daily updates'}</h2><p className="text-[var(--app-text-muted)]">Readable outcomes, with each blocked incident kept separate.</p></header>
    {id && <><PageStatus input={definitionInput} /><div className="flex flex-wrap gap-2"><button className={control} disabled={disabled} onClick={() => act('run')}>Run now</button><button className={control} disabled={disabled} onClick={() => act(record?.definition?.enabled ? 'pause' : 'enable')}>{record?.definition?.enabled ? 'Pause' : 'Enable'}</button></div></>}
    <p role="status">{operation.message}</p>
    <nav aria-label="Automation sections" className="flex flex-wrap gap-2">{(id ? ['Updates', 'History', 'Configuration', 'Context'] : ['Updates', 'History']).map(value => <button className={control} key={value} aria-pressed={tab === value} onClick={() => setTab(value)}>{value}</button>)}</nav>
    {(tab === 'Updates' || tab === 'History') && <><label>Display timezone <select className={control} value={timezone} onChange={event => setTimezone(event.target.value)}>{[...new Set([Intl.DateTimeFormat().resolvedOptions().timeZone, 'UTC', record?.definition?.schedule.timezone].filter((zone): zone is string => !!zone))].map(zone => <option key={zone}>{zone}</option>)}</select></label><UpdateFeed key={tab} workspaceId={workspaceId} id={id} timezone={timezone} today={dayKey(now, timezone)} history={tab === 'History'} onChat={setSession} /></>}
    {tab === 'Configuration' && record?.definition && <><PolicyPanel workspaceId={workspaceId} id={id} /><RecordHistory record={record} /><AutomationEditor key={record.id} initial={record.definition} revision={record.revision} disabled={disabled} onSave={async definition => { await operation.mutate({ action: 'save', workspace_id: workspaceId, id, mutation_id: crypto.randomUUID(), expected_revision: record.revision, definition }) }} /></>}
    {tab === 'Context' && <ContextPanel workspaceId={workspaceId} id={id} />}
    {record?.definition?.plans.map(binding => <button key={binding.id} className={control} onClick={() => setSession(binding.plan.session_id)}>Manage with plan {binding.id} AI chat</button>)}
    {!session && <p className="text-sm text-[var(--app-text-muted)]">Use a linked plan conversation for management, or an occurrence conversation to inspect its deliverables. Changes in chat do not replace pinned automation plans or grant execution permission.</p>}
  </main>{session && <div className="min-w-0"><button className={control} onClick={() => setSession('')}>Close AI chat</button><AutomationChat key={session} sessionId={session} automationId={id || undefined} /></div>}</div>
}
function UpdateFeed({ workspaceId, id, timezone, today, history, onChat }: { workspaceId: string; id: string; timezone: string; today: string; history: boolean; onChat: (id: string) => void }) {
  const [draft, setDraft] = useState('')
  const [query, setQuery] = useState('')
  const [cursor, setCursor] = useState<string>()
  const [kind, setKind] = useState<'audit' | 'occurrence'>('audit')
  const [attention, setAttention] = useState(false)
  const [historyRecord, setHistoryRecord] = useState<AutomationRecord>()
  const [outcomeRecord, setOutcomeRecord] = useState<AutomationRecord>()
  const input: AutomationRead = { workspace_id: workspaceId, action: 'search', id: id || undefined, kind, query: query || undefined, cursor, limit: 20 }
  const page = usePage(input)
  const operation = useMutation()
  const rows = (page?.data?.records ?? []).filter(row => !attention || needsAttention(row))
  return <section aria-label={history ? 'Searchable history' : 'Daily outcomes'} className="space-y-4">
    <form className="flex flex-wrap gap-2" onSubmit={event => { event.preventDefault(); setQuery(draft); setCursor(undefined) }}><label>Search recorded updates <input className={control} value={draft} maxLength={256} onChange={event => setDraft(event.target.value)} /></label><button className={control}>Search</button></form>
    <label>Record type <select className={control} value={kind} onChange={event => { setKind(event.target.value as typeof kind); setCursor(undefined) }}><option value="audit">Outcomes and audit</option><option value="occurrence">Occurrences</option></select></label>{' '}
    <label><input type="checkbox" checked={attention} onChange={event => setAttention(event.target.checked)} /> Attention only (this page)</label>
    <p className="text-sm">20 records per page, grouped by date in {timezone}; not a complete daily digest.</p>
    <PageStatus input={input} /><p role="status">{operation.message}</p>
    {page?.data && !rows.length && <p>No matching recorded updates on this page.</p>}
    {groupUpdates(rows, timezone).map(([date, records]) => <section key={date} aria-label={date}><h3 className="my-3 font-semibold">{date === today ? 'Today' : date}</h3>{records.map(row => <article key={`${row.kind}:${row.id}:${row.revision}`} className="my-3 space-y-2 rounded-lg border border-[var(--app-border)] p-4 break-words">
      <h4 className="font-semibold">{needsAttention(row) ? 'Needs attention · ' : ''}{row.outcome?.kind ?? row.occurrence?.state ?? row.kind}</h4>
      <p className="whitespace-pre-wrap">{row.outcome?.summary ?? `Occurrence ${row.id}: ${row.occurrence?.state ?? 'recorded'}`}</p>
      <time dateTime={new Date(row.written_at).toISOString()}>{new Intl.DateTimeFormat(undefined, { timeZone: timezone, hour: 'numeric', minute: '2-digit' }).format(row.written_at)}</time>
      <details><summary className="cursor-pointer">Details and recorded deliverables</summary><dl className="space-y-2"><dt>Automation</dt><dd>{row.automation_id}</dd><dt>Record revision</dt><dd>{row.revision}</dd>{Object.entries(row.outcome?.facts ?? {}).map(([key, value]) => <div key={key}><dt className="font-semibold">{key}</dt><dd className="whitespace-pre-wrap">{value}</dd></div>)}</dl><p className="text-sm">Facts are recorded text, not verified artifact references.</p><button className={control} onClick={() => setHistoryRecord(row)}>Inspect revision history</button></details>
      {row.outcome?.occurrence_id && <button className={control} onClick={() => setOutcomeRecord(row)}>Inspect outcome session and artifacts</button>}
      {row.occurrence?.session_id && <button className={control} onClick={() => onChat(row.occurrence!.session_id!)}>Open occurrence chat and artifacts</button>}
      {row.occurrence && ['pending', 'running', 'blocked'].includes(row.occurrence.state) && <button className={control} disabled={operation.pending || page?.stale || page?.loading} onClick={() => { void operation.mutate({ action: 'cancel', workspace_id: workspaceId, id: row.automation_id, occurrence_id: row.id, expected_revision: row.revision, mutation_id: crypto.randomUUID() }).catch(() => {}) }}>Cancel occurrence</button>}
    </article>)}</section>)}
    {outcomeRecord && <OutcomeSession key={outcomeRecord.id} record={outcomeRecord} onChat={onChat} />}
    {historyRecord && <section aria-label="Selected record history"><button className={control} onClick={() => setHistoryRecord(undefined)}>Close revision history</button><HistoryPage key={`${historyRecord.kind}:${historyRecord.id}`} record={historyRecord} /></section>}
    <div className="flex gap-2"><button className={control} onClick={() => void desktopAutomations.refresh(input)}>Refresh updates</button>{cursor && <button className={control} onClick={() => setCursor(undefined)}>First page</button>}{page?.data?.next_cursor && <button className={control} disabled={page.loading || page.stale} onClick={() => setCursor(page.data?.next_cursor)}>Next page</button>}</div>
  </section>
}
function RecordHistory({ record }: { record: AutomationRecord }) {
  const [open, setOpen] = useState(false)
  return <><button className={control} onClick={() => setOpen(value => !value)} aria-expanded={open}>Revision history</button>{open && <HistoryPage key={`${record.kind}:${record.id}`} record={record} />}</>
}
function HistoryPage({ record }: { record: AutomationRecord }) {
  const [before, setBefore] = useState<number>()
  const input: AutomationRead = { workspace_id: record.scope.workspace_id, action: 'history', id: record.automation_id, kind: record.kind, record_id: record.id, before, limit: 10 }
  const page = usePage(input)
  return <div><PageStatus input={input} />{page?.data?.records?.map(row => <details key={row.revision}><summary>Revision {row.revision} · {row.actor}</summary><pre className="whitespace-pre-wrap break-all text-xs">{JSON.stringify(row.context ?? row.definition ?? row.occurrence ?? row.outcome, null, 2)}</pre></details>)}{page?.data?.next_before ? <button className={control} disabled={page.loading || page.stale} onClick={() => setBefore(page.data?.next_before)}>Older revisions</button> : null}{before && <button className={control} onClick={() => setBefore(undefined)}>Latest revisions</button>}</div>
}
function ContextPanel({ workspaceId, id }: { workspaceId: string; id: string }) {
  const input: AutomationRead = { workspace_id: workspaceId, action: 'context', id }
  const page = usePage(input)
  const bundle = page?.data?.context
  const [draft, setDraft] = useState<string | null>(null)
  const [base, setBase] = useState<number>()
  const operation = useMutation()
  const [error, setError] = useState('')
  return <section className="space-y-4"><h3>User instructions</h3><PageStatus input={input} /><p>Only you can edit these instructions. Agent summaries below are untrusted context, never authorization.</p>
    {bundle && <><form onSubmit={event => { event.preventDefault(); if (operation.pending || page?.stale || page?.loading || (base !== undefined && base !== bundle.Revision)) return; try { const user_instructions = parseInstructions(draft ?? JSON.stringify(bundle.UserInstructions ?? {})); setError(''); void operation.mutate({ action: 'context', workspace_id: workspaceId, id, expected_revision: base ?? bundle.Revision, mutation_id: crypto.randomUUID(), user_instructions }).then(() => { setDraft(null); setBase(undefined) }).catch(() => {}) } catch (cause) { setError(cause instanceof Error ? cause.message : 'Invalid instructions') } }}>
      <label className="flex flex-col">User instructions (JSON text map)<textarea disabled={operation.pending} rows={8} className={control} value={draft ?? JSON.stringify(bundle.UserInstructions ?? {}, null, 2)} onChange={event => { if (draft === null) setBase(bundle.Revision); setDraft(event.target.value) }} /></label>
      {base !== undefined && base !== bundle.Revision && <p role="alert">Context changed while editing. Discard your draft or preserve it elsewhere before refreshing.</p>}
      <button className={control} disabled={operation.pending || page?.stale || page?.loading || (base !== undefined && base !== bundle.Revision)}>Save user instructions</button>{' '}<button className={control} type="button" disabled={operation.pending} onClick={() => { setDraft(null); setBase(undefined) }}>Discard draft</button>
    </form><h3>Agent summaries · read only</h3><dl>{Object.entries(bundle.Summaries ?? {}).map(([key, value]) => <div key={key}><dt>{key}</dt><dd className="whitespace-pre-wrap break-words">{value}</dd></div>)}</dl><p>Trust: {bundle.Trust} · Revision {bundle.Revision}</p>
    <ContextHistory workspaceId={workspaceId} id={id} /></>}
    <p role="status">{operation.message}</p>{error && <p role="alert">{error}</p>}
  </section>
}
function ContextHistory({ workspaceId, id }: { workspaceId: string; id: string }) {
  const input: AutomationRead = { workspace_id: workspaceId, action: 'search', id, kind: 'context', limit: 1 }
  const page = usePage(input)
  return <>{page?.data?.records?.map(record => <RecordHistory key={record.id} record={record} />)}</>
}

function PolicyPanel({ workspaceId, id }: { workspaceId: string; id: string }) {
  const input: AutomationRead = { workspace_id: workspaceId, action: 'policy', id }
  const page = usePage(input)
  const record = page?.data?.record
  const grant = page?.data?.approval
  const operation = useMutation()
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const disabled = busy || operation.pending || !record?.definition || page?.stale || page?.loading
  async function approve() {
    if (disabled || !record?.definition || !page?.data?.policy_sha256) return
    setBusy(true); setError('')
    try {
      const result = await operation.mutate({ action: 'approve', workspace_id: workspaceId, id, mutation_id: crypto.randomUUID(), expected_revision: record.revision, policy_sha256: page.data.policy_sha256 })
      if (!result.approval) throw new Error('Approval response missing; refresh policy before retrying.')
      try {
        await operation.mutate({ action: 'save', workspace_id: workspaceId, id, mutation_id: crypto.randomUUID(), expected_revision: record.revision, definition: { ...record.definition, authorization: { ...record.definition.authorization, mode: 'approved_policy', approval_reference: result.approval.id } } })
      } catch {
        throw new Error('Policy grant was created but configuration was not linked. Refresh and review before approving again; execution was not enabled by this request.')
      }
    } catch (cause) { setError(cause instanceof Error ? cause.message : 'Approval failed') }
    finally { setBusy(false) }
  }
  return <section aria-label="Execution policy" className="space-y-3 rounded border border-[var(--app-border)] p-3">
    <h3 className="font-semibold">Execution policy approval</h3><PageStatus input={input} />
    <p>Review the exact saved plans, tools, targets, schedule and expiry below. Approval links a grant; enabling remains a separate user action.</p>
    {record && <details><summary>Review saved policy · revision {record.revision}</summary><pre className="whitespace-pre-wrap break-all text-xs">{JSON.stringify(record.definition, null, 2)}</pre><p className="break-all">Digest: {page?.data?.policy_sha256}</p></details>}
    <button className={control} disabled={disabled || !page?.data?.policy_sha256} onClick={() => void approve()}>Approve and link reviewed policy</button>{' '}
    {grant && <><p>Grant revision {grant.revision} · {grant.revoked_at ? 'Revoked' : 'Recorded (server rechecks expiry and ownership)'}</p><button className={control} disabled={disabled || !!grant.revoked_at} onClick={() => { void operation.mutate({ action: 'revoke', workspace_id: workspaceId, id, mutation_id: crypto.randomUUID(), expected_revision: grant.revision, approval_reference: grant.id }).catch(() => {}) }}>Revoke execution approval</button></>}
    <p role="status">{operation.message}</p>{error && <p role="alert">{error}</p>}
  </section>
}

// Resolve the typed occurrence reference, never the untrusted facts.session_id.
function OutcomeSession({ record, onChat }: { record: AutomationRecord; onChat: (id: string) => void }) {
  const input: AutomationRead = { workspace_id: record.scope.workspace_id, action: 'get', id: record.automation_id, kind: 'occurrence', record_id: record.outcome!.occurrence_id, limit: 1 }
  const page = usePage(input)
  const occurrence = page?.data?.records?.find(row => row.id === record.outcome?.occurrence_id && row.kind === 'occurrence')
  return <section aria-label="Outcome deliverables"><PageStatus input={input} />{occurrence?.occurrence?.session_id ? <button className={control} disabled={page?.loading || page?.stale} onClick={() => onChat(occurrence.occurrence!.session_id!)}>Open canonical conversation and artifact gallery</button> : <p>No execution session available for this outcome.</p>}</section>
}
