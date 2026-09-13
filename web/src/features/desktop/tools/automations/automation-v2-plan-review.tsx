import { useRef, useState } from 'react'
import { Button } from '../../../../components/ui/button'
import { AutomationV2ScheduleFields } from './automation-v2-schedule-fields'
import { scheduleLabel } from './automation-v2-schedule'
import { normalizeStructuredPlanDocument, StructuredPlanReviewView } from '../../chat/components/structured-plan-document'
import { automationV2Review, validateAutomationV2, type AutomationV2Document, type AutomationV2Proposal, type AutomationV2Record } from '../../state/desktop-automation-v2-api'
import { desktopAutomationV2 } from '../../runtime/desktop-automation-v2'
import type { DesktopPermissionRecord } from '../../types/realtime'

export function automationV2PermissionProposal(permission: DesktopPermissionRecord): AutomationV2Proposal | null {
  if (permission.requirement !== 'automation_v2_acceptance') return null
  try {
    const payload = JSON.parse(permission.toolArguments)
    const review = automationV2Review(payload.automation_review)
    if (payload.review_kind !== 'automation_v2' || !payload.scope?.workspace_id || !payload.scope?.account_id || !payload.document?.automation_v2 || !payload.document?.checkpoints?.length) return null
    return { ...review, workspace_id: payload.scope.workspace_id, account_id: payload.scope.account_id, session_id: permission.sessionId, document: payload.document }
  } catch { return null }
}
const field = 'w-full min-w-0 rounded-lg border border-[var(--app-border)] bg-[var(--app-bg)] px-3 py-2 text-sm text-[var(--app-text)] focus-visible:outline-[var(--app-primary)]'
export function AutomationV2PlanReview({ proposal, onReject, disabled = false }: { proposal: AutomationV2Proposal; onReject?: () => Promise<void>; disabled?: boolean }) {
  const [reviewed, setReviewed] = useState(proposal)
  const [draft, setDraft] = useState<AutomationV2Document>(() => ({ ...proposal.document, automation_v2: { ...proposal.document.automation_v2, expiration: proposal.document.automation_v2.expiration ?? { kind: 'indefinite' } } }))
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [accepted, setAccepted] = useState<AutomationV2Record | null>(null)
  const [rejected, setRejected] = useState(false)
  const lock = useRef(false)
  const settings = draft.automation_v2
  const dirty = JSON.stringify(draft) !== JSON.stringify(reviewed.document)
  // A newer permission is not a license to accept the old draft. Preserve edits
  // visibly and require explicit refresh rather than silently rebasing them.
  const stale = proposal.revision > reviewed.revision || (proposal.revision === reviewed.revision && proposal.digest !== reviewed.digest)
  const blocked = disabled || busy || !!accepted || rejected || stale
  let invalid = ''
  try { validateAutomationV2(settings) } catch (cause) { invalid = cause instanceof Error ? cause.message : 'Invalid settings' }
  const update = (patch: Partial<typeof settings>) => setDraft({ ...draft, automation_v2: { ...settings, ...patch } })
  async function run(action: 'review' | 'accept' | 'reject') {
    if (lock.current || blocked || (action !== 'reject' && invalid)) return
    lock.current = true; setBusy(true); setError('')
    try {
      if (action === 'reject') { await onReject?.(); setRejected(true); return }
      if (action === 'review') {
        const response = await desktopAutomationV2.mutate({ action: 'propose_automation', workspace_id: proposal.workspace_id, session_id: proposal.session_id, review: automationV2Review(reviewed), document: draft })
        const next = response.proposal
        if (!next || next.session_id !== proposal.session_id || next.workspace_id !== proposal.workspace_id || next.proposal_id !== proposal.proposal_id || next.revision <= reviewed.revision) throw new Error('Revised review unavailable. Refresh before accepting.')
        automationV2Review(next)
        setReviewed(next); setDraft(next.document)
      } else {
        if (dirty) return
        const response = await desktopAutomationV2.mutate({ action: 'accept_automation', workspace_id: proposal.workspace_id, session_id: proposal.session_id, review: automationV2Review(reviewed) })
        if (!response.record || response.record.digest !== reviewed.digest || response.record.session_id !== proposal.session_id || !response.record.automation_id) throw new Error('Acceptance result unavailable. Refresh before retrying.')
        setAccepted(response.record)
      }
    } catch (cause) { setError(cause instanceof Error ? cause.message : 'Automation request failed. Refresh before retrying.') }
    finally { lock.current = false; setBusy(false) }
  }
  const display = normalizeStructuredPlanDocument(draft)
  return <section aria-label="Automation plan review" className="min-w-0 space-y-4 rounded-2xl border border-[var(--app-primary-border)] bg-[var(--app-surface)] p-4 font-sans" data-testid="automation-v2-plan-review">
    <header><p className="text-xs font-semibold uppercase tracking-wider text-[var(--app-primary)]">Automation plan</p><h2 className="mt-1 break-words text-lg font-semibold">{draft.title}</h2><p className="mt-1 text-xs text-[var(--app-text-muted)]">Choose when this runs, then accept the plan.</p></header>
    <p className="break-words text-sm leading-5 text-[var(--app-text-muted)]">{draft.info.goal}</p>
    <fieldset disabled={blocked} className="min-w-0">
      <legend className="mb-2 text-sm font-semibold">Schedule</legend>
      <AutomationV2ScheduleFields schedule={settings.schedule} onChange={schedule => update({ schedule })} />
    </fieldset>
    {display && <details className="rounded-xl border border-[var(--app-border)]/60 p-3"><summary className="cursor-pointer text-sm font-medium">What Swarm will do · {draft.checkpoints.length} {draft.checkpoints.length === 1 ? 'step' : 'steps'}</summary><div className="mt-3"><StructuredPlanReviewView document={display} /></div></details>}
    <details className="rounded-xl border border-[var(--app-border)] p-3"><summary className="cursor-pointer text-sm font-medium">Edit instructions</summary><fieldset disabled={blocked} className="mt-3 space-y-3"><label className="block text-xs">Goal<textarea className={field} value={draft.info.goal} onChange={e => setDraft({ ...draft, info: { ...draft.info, goal: e.target.value } })} /></label>{draft.checkpoints.map((checkpoint, index) => <label key={checkpoint.id} className="block text-xs">{checkpoint.title} · tasks (one per line)<textarea className={field} rows={3} value={(checkpoint.tasks ?? [checkpoint.objective ?? '']).join('\n')} onChange={e => setDraft({ ...draft, checkpoints: draft.checkpoints.map((c, i) => i === index ? { ...c, tasks: e.target.value.split('\n') } : c) })} /></label>)}</fieldset></details>
    <fieldset disabled={blocked} className="grid min-w-0 gap-4 sm:grid-cols-2">

      <label className="space-y-1 text-xs">Expiration<select aria-label="Expiration" className={field} value={settings.expiration.kind} onChange={e => update({ expiration: e.target.value === 'indefinite' ? { kind: 'indefinite' } : { kind: 'at', expires_at: undefined } })}><option value="indefinite">Until I stop it</option><option value="at">On a specific date</option></select></label>
      {settings.expiration.kind === 'at' && <label className="space-y-1 text-xs">Expiration (UTC)<input className={field} type="datetime-local" step="0.001" value={settings.expiration.expires_at ? new Date(settings.expiration.expires_at).toISOString().slice(0, -1) : ''} onChange={e => update({ expiration: { kind: 'at', expires_at: e.target.value ? Date.parse(e.target.value + 'Z') : undefined } })} /></label>}
    </fieldset>
    <details className="text-xs text-[var(--app-text-muted)]"><summary className="cursor-pointer">Run behavior</summary><p className="mt-2 leading-5">{settings.missed === 'skip' ? 'Missed runs are skipped.' : 'Missed runs are combined into one catch-up run.'} {settings.overlap === 'serialize' ? 'Runs wait for earlier work to finish.' : 'Runs may execute independently.'} Changes apply to future work; already admitted runs keep their original instructions.</p></details>
    <p className="rounded-xl bg-[var(--app-bg-alt)] p-3 text-xs leading-5">{scheduleLabel(settings.schedule)}. Accepting activates this schedule with the instructions above. It does not start a run immediately.</p>
    {dirty && <p role="status" className="text-sm">Your changes aren’t active yet. Save the schedule, then accept it.</p>}
    {stale && <p role="alert">A newer proposal arrived. Your draft was not applied. <Button size="sm" onClick={() => { setReviewed(proposal); setDraft(proposal.document); setError('') }}>Discard draft and load current review</Button></p>}
    {(error || invalid) && <p role="alert" className="break-words text-sm text-[var(--app-danger)]">{error || invalid}</p>}
    {accepted && <section aria-label="Automation handoff" role="status" className="rounded-xl border border-[var(--app-primary-border)] p-4"><h3 className="font-semibold">Automation accepted · scheduled</h3><p>Activated revision {accepted.revision}. Acceptance did not start a run.</p><p>{accepted.next_due_at ? `Next scheduled time: ${new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'long', timeZone: accepted.document.automation_v2.schedule.timezone || 'UTC' }).format(accepted.next_due_at)}` : 'No next scheduled time is available.'}</p><p className="text-xs text-[var(--app-text-muted)]">{accepted.document.automation_v2.schedule.kind === 'interval' ? 'Elapsed timer anchored to this acceptance; displayed in UTC.' : `Wall-clock schedule in ${accepted.document.automation_v2.schedule.timezone}.`} Scheduled is not admitted or running. Open Automation details for observed work.</p></section>}
    <footer className="flex flex-wrap justify-end gap-2 border-t border-[var(--app-border)] pt-4">
      {onReject && <Button variant="outline" disabled={blocked} onClick={() => void run('reject')}>Reject</Button>}
      {dirty ? <Button disabled={blocked || !!invalid} onClick={() => void run('review')}>{busy ? 'Saving changes…' : 'Save schedule changes'}</Button> : <Button disabled={blocked || !!invalid} onClick={() => void run('accept')}>{accepted ? 'Automation accepted' : rejected ? 'Proposal rejected' : busy ? 'Accepting…' : 'Accept automation'}</Button>}
    </footer>
  </section>
}
