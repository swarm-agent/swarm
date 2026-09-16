import { createContext, useContext, useEffect, useRef, useState, type ContextType } from 'react'
import { requestJson } from '../../../../app/api'
import { readAutomations, type AutomationRecord } from '../../state/desktop-automation-api'
import { desktopAutomations } from '../../runtime/desktop-automations'

// Set only by the authenticated automation sidecar, never by transcript bytes.
export const AutomationInstructionContext = createContext<null | { parentSessionId: string; automation_id: string; automation_revision: number; workspace_id: string }>(null)
type Document = Record<string, unknown>
type Plan = { id: string; session_id: string; version: number; document: Document; approval_state: string }
type Saved = { plan: Plan; document_sha256: string }
const canonical = (value: unknown): string => JSON.stringify(value, (_key, item) => item && typeof item === 'object' && !Array.isArray(item) ? Object.fromEntries(Object.keys(item).sort().map(key => [key, item[key]])) : item)
const object = (v: unknown): v is Record<string, any> => !!v && typeof v === 'object' && !Array.isArray(v)
export function parseInstructionProposal(payload: unknown, parent: string) {
  if (!object(payload)) return null
  const result = object(payload.result) ? payload.result : payload
  const p = result.proposal
  if (result.status !== 'requires_user_approval' || result.applied !== false || !object(p) || p.method !== 'POST' || p.path !== `/v3/sessions/${encodeURIComponent(parent)}/plans` || Object.keys(p).some(k => !['method', 'path', 'body'].includes(k))) return null
  const b = p.body
  if (!object(b) || Object.keys(b).some(k => !['document', 'title', 'activate', 'status', 'approval_state'].includes(k)) || b.activate !== false || b.status !== 'draft' || b.approval_state !== 'pending' || typeof b.title !== 'string' || !object(b.document) || b.document.automation != null || !Array.isArray(b.document.checkpoints) || !b.document.checkpoints.length) return null
  return { title: b.title, document: b.document as Document }
}
export function AutomationInstructionProposal({ payload }: { payload: unknown }) {
  const context = useContext(AutomationInstructionContext)
  const proposal = context && parseInstructionProposal(payload, context.parentSessionId)
  if (!context || !proposal) return null
  return <InstructionRequest key={JSON.stringify([context, proposal])} context={context} proposal={proposal} />
}
function InstructionRequest({ context, proposal }: { context: NonNullable<ContextType<typeof AutomationInstructionContext>>; proposal: NonNullable<ReturnType<typeof parseInstructionProposal>> }) {
  const lock = useRef(false)
  const mounted = useRef(true)
  useEffect(() => { mounted.current = true; return () => { mounted.current = false } }, [])
  const [busy, setBusy] = useState(false)
  const [saved, setSaved] = useState<Saved | null>(null)
  const [approved, setApproved] = useState<Saved | null>(null)
  const [record, setRecord] = useState<AutomationRecord | null>(null)
  const [binding, setBinding] = useState('')
  const [done, setDone] = useState(false)
  const [error, setError] = useState('')
  const base = `/v3/sessions/${encodeURIComponent(context.parentSessionId)}/plans`
  async function current() {
    const r = (await readAutomations({ action: 'get', workspace_id: context.workspace_id, id: context.automation_id })).record
    if (!mounted.current || !r || r.automation_id !== context.automation_id || r.scope.workspace_id !== context.workspace_id || r.revision !== context.automation_revision || r.definition?.session_id !== context.parentSessionId) throw Error('Automation changed or belongs to another context. Refresh the review.')
    return r
  }
  function check(result: Saved, expectedDocument: Document | null, id?: string) {
    if (!mounted.current || !result.plan || result.plan.session_id !== context.parentSessionId || !result.plan.id || (id && result.plan.id !== id) || !Number.isSafeInteger(result.plan.version) || result.plan.version < 1 || !/^[a-f0-9]{64}$/.test(result.document_sha256) || !object(result.plan.document) || (expectedDocument && canonical({ ...result.plan.document, revision_id: '' }) !== canonical({ ...expectedDocument, revision_id: '' }))) throw Error('Canonical instruction response mismatch. Refresh before continuing.')
    return result
  }
  async function act(step: 'draft' | 'approve' | 'save') {
    if (!mounted.current || lock.current || done) return
    lock.current = true; setBusy(true); setError('')
    try {
      const r = await current()
      if (step === 'draft') {
        // Fresh ID prevents untrusted document identity from overwriting a plan.
        const document = { ...proposal.document, id: crypto.randomUUID() }
        const result = check(await requestJson<Saved>(base, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ plan_id: document.id, title: proposal.title, document, activate: false, status: 'draft', approval_state: 'pending' }) }), null, document.id)
        if (result.plan.approval_state !== 'pending') throw Error('Draft was not pending approval')
        setSaved(result); setRecord(r); setBinding(r.definition!.plans[0]?.id ?? '')
      } else if (step === 'approve' && saved) {
        const latest = check(await requestJson<Saved>(`${base}/${encodeURIComponent(saved.plan.id)}`), saved.plan.document, saved.plan.id)
        if (latest.plan.version !== saved.plan.version || latest.document_sha256 !== saved.document_sha256 || latest.plan.approval_state !== 'pending') throw Error('Instruction draft changed. Save a fresh proposal.')
        // Submit the reviewed document itself, not approval of mutable current bytes.
        const result = check(await requestJson<Saved>(base, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ plan_id: saved.plan.id, title: proposal.title, document: saved.plan.document, activate: false, status: 'approved', approval_state: 'approved' }) }), saved.plan.document, saved.plan.id)
        if (result.plan.approval_state !== 'approved') throw Error('Instructions were not approved')
        setApproved(result)
      } else if (step === 'save' && approved) {
        if (!r.definition!.plans.some(p => p.id === binding)) throw Error('Select an existing instruction binding')
        const definition = { ...r.definition!, enabled: false, authorization: { ...r.definition!.authorization, mode: 'approval_required' as const, approval_reference: '' }, plans: r.definition!.plans.map(p => p.id !== binding ? p : { ...p, plan: { session_id: context.parentSessionId, plan_id: approved.plan.id, revision: approved.plan.version, document_sha256: approved.document_sha256 } }) }
        await desktopAutomations.mutate({ action: 'save', workspace_id: context.workspace_id, id: context.automation_id, expected_revision: context.automation_revision, mutation_id: crypto.randomUUID(), definition })
        setDone(true)
      }
    } catch (e) { setError(e instanceof Error ? e.message : 'Instruction request failed') }
    finally { lock.current = false; setBusy(false) }
  }
  return <section aria-label="Automation instruction proposal">
    <h3>{proposal.title || 'Proposed automation instructions'}</h3>
    <p>Save a draft, separately approve its exact instructions, then save replacement configuration. Saving configuration pauses this automation and requires policy reapproval and separate enabling. Existing artifacts and other plans are retained.</p>
    <pre className="max-h-64 overflow-auto whitespace-pre-wrap">{JSON.stringify(saved?.plan.document ?? proposal.document, null, 2)}</pre>
    {saved && <p>Review the canonical saved draft above before approving.</p>}
    <button disabled={busy || !!saved} onClick={() => void act('draft')}>Save instruction draft</button>
    {saved && <button disabled={busy || !!approved} onClick={() => void act('approve')}>Approve exact instruction revision</button>}
    {approved && <><p>Approved revision {approved.plan.version}: {approved.document_sha256}</p><label>Replace instruction binding<select value={binding} onChange={e => setBinding(e.target.value)}>{record?.definition?.plans.map(p => <option key={p.id} value={p.id}>{p.id}</option>)}</select></label><button disabled={busy || done || !binding} onClick={() => void act('save')}>Save paused automation configuration</button></>}
    {done && <p role="status">Configuration saved paused. Refresh Automation to review policy and separately approve and enable.</p>}
    {error && <p role="alert">{error}</p>}
  </section>
}
