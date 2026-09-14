import { useEffect, useRef, useState } from 'react'
import { Button } from '../../../../components/ui/button'
import { AutomationV2ScheduleFields } from './automation-v2-schedule-fields'
import { scheduleLabel } from './automation-v2-schedule'
import { normalizeStructuredPlanDocument, StructuredPlanReviewView } from '../../chat/components/structured-plan-document'
import { automationV2Review, validateAutomationV2, type AutomationV2Document, type AutomationV2Proposal, type AutomationV2Record } from '../../state/desktop-automation-v2-api'
import { desktopAutomationV2 } from '../../runtime/desktop-automation-v2'
import type { DesktopPermissionRecord } from '../../types/realtime'

export type AutomationIntentPresetId = 'silent_maintenance' | 'summary_report' | 'deliverable_output'

export interface AutomationIntentPreset {
  id: AutomationIntentPresetId
  title: string
  tag: string
  description: string
  stateLabel: string
  recommendedClosingState: 'routine_clean' | 'deliverable_ready' | 'attention_alert'
  recommendedAlertCondition: string
  recommendedMissed: 'skip' | 'coalesce'
  recommendedOverlap: 'serialize' | 'independent'
}

export const AUTOMATION_INTENT_PRESETS: AutomationIntentPreset[] = [
  {
    id: 'silent_maintenance',
    title: 'Silent maintenance',
    tag: 'Calm background',
    description: 'Runs quietly in the background without creating alerts or chat clutter unless an issue or drift is detected.',
    stateLabel: 'Routine clean (calm minimal)',
    recommendedClosingState: 'routine_clean',
    recommendedAlertCondition: 'Alert if health checks fail, error rates exceed thresholds, or unexpected drift is detected',
    recommendedMissed: 'skip',
    recommendedOverlap: 'serialize',
  },
  {
    id: 'summary_report',
    title: 'Summary report',
    tag: 'Periodic status',
    description: 'Produces a concise summary report after each run, giving you regular auditability without chat sprawl.',
    stateLabel: 'Routine clean (summary)',
    recommendedClosingState: 'routine_clean',
    recommendedAlertCondition: 'Alert if inspection finds warnings, degradation, or failed checks',
    recommendedMissed: 'coalesce',
    recommendedOverlap: 'serialize',
  },
  {
    id: 'deliverable_output',
    title: 'Deliverable output',
    tag: 'Deliverables & files',
    description: 'Generates or updates project files, reports, or artifacts each cycle and highlights them for review.',
    stateLabel: 'Deliverable ready (review items)',
    recommendedClosingState: 'deliverable_ready',
    recommendedAlertCondition: 'Alert if deliverable generation fails or required dependencies are missing',
    recommendedMissed: 'coalesce',
    recommendedOverlap: 'serialize',
  },
]

export function getCheckpointClosingState(checkpoint?: {
  closing_state?: string
  acceptance_criteria?: string[]
  title?: string
  tasks?: string[]
  objective?: string
}): 'routine_clean' | 'deliverable_ready' | 'attention_alert' | 'blocked' {
  if (!checkpoint) return 'routine_clean'
  if (checkpoint.closing_state && ['routine_clean', 'deliverable_ready', 'attention_alert', 'blocked'].includes(checkpoint.closing_state)) {
    return checkpoint.closing_state as 'routine_clean' | 'deliverable_ready' | 'attention_alert' | 'blocked'
  }
  const text = [
    checkpoint.title || '',
    checkpoint.objective || '',
    ...(checkpoint.tasks || []),
    ...(checkpoint.acceptance_criteria || []),
  ].join(' ').toLowerCase()
  if (text.includes('deliverable_ready') || text.includes('deliverable') || text.includes('artifact')) {
    return 'deliverable_ready'
  }
  if (text.includes('attention_alert') || text.includes('alert')) {
    return 'attention_alert'
  }
  if (text.includes('blocked')) {
    return 'blocked'
  }
  return 'routine_clean'
}

export function getCheckpointAlertConditions(checkpoint?: {
  alert_conditions?: string
  acceptance_criteria?: string[]
}): string {
  if (checkpoint?.alert_conditions?.trim()) {
    return checkpoint.alert_conditions.trim()
  }
  const alertCriterion = checkpoint?.acceptance_criteria?.find(c =>
    /alert|warning|drift|threshold|exceed|fail|error|degrad/i.test(c)
  )
  if (alertCriterion) {
    return alertCriterion.trim()
  }
  return 'Alert if health checks fail, error rates exceed thresholds, or unexpected drift occurs'
}

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

export function AutomationV2PlanReview({ proposal, onReject, disabled = false, modalMode = false }: { proposal: AutomationV2Proposal; onReject?: () => Promise<void>; disabled?: boolean; modalMode?: boolean }) {
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

  useEffect(() => {
    if (!dirty && (proposal.revision !== reviewed.revision || proposal.digest !== reviewed.digest)) {
      setReviewed(proposal)
      setDraft({
        ...proposal.document,
        automation_v2: {
          ...proposal.document.automation_v2,
          expiration: proposal.document.automation_v2.expiration ?? { kind: 'indefinite' },
        },
      })
      setError('')
    }
  }, [proposal, dirty, reviewed.revision, reviewed.digest])

  let invalid = ''
  try { validateAutomationV2(settings) } catch (cause) { invalid = cause instanceof Error ? cause.message : 'Invalid settings' }
  const update = (patch: Partial<typeof settings>) => setDraft({ ...draft, automation_v2: { ...settings, ...patch } })

  function applyIntentPreset(presetId: AutomationIntentPresetId) {
    const preset = AUTOMATION_INTENT_PRESETS.find(p => p.id === presetId)
    if (!preset) return
    const updatedCheckpoints = draft.checkpoints.map(cp => ({
      ...cp,
      closing_state: preset.recommendedClosingState,
      alert_conditions: preset.recommendedAlertCondition,
    }))
    setDraft({
      ...draft,
      checkpoints: updatedCheckpoints,
      automation_v2: {
        ...settings,
        missed: preset.recommendedMissed,
        overlap: preset.recommendedOverlap,
      },
    })
  }

  function updateCheckpointClosingState(index: number, closingState: string) {
    const updatedCheckpoints = draft.checkpoints.map((cp, i) =>
      i === index ? { ...cp, closing_state: closingState } : cp
    )
    setDraft({ ...draft, checkpoints: updatedCheckpoints })
  }

  function updateCheckpointAlertConditions(index: number, alertConditions: string) {
    const updatedCheckpoints = draft.checkpoints.map((cp, i) =>
      i === index ? { ...cp, alert_conditions: alertConditions } : cp
    )
    setDraft({ ...draft, checkpoints: updatedCheckpoints })
  }

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
  const containerClass = modalMode
    ? "min-w-0 space-y-3.5 font-sans"
    : "min-w-0 space-y-4 rounded-2xl border border-[var(--app-primary-border)] bg-[var(--app-surface)] p-4 font-sans"

  const activeClosingState = draft.checkpoints.length > 0 ? getCheckpointClosingState(draft.checkpoints[0]) : 'routine_clean'
  const activePresetId: AutomationIntentPresetId | null =
    activeClosingState === 'deliverable_ready'
      ? 'deliverable_output'
      : settings.missed === 'coalesce'
      ? 'summary_report'
      : activeClosingState === 'routine_clean' && settings.missed === 'skip'
      ? 'silent_maintenance'
      : null

  return <section aria-label="Automation plan review" className={containerClass} data-testid="automation-v2-plan-review">
    <header>
      <div className="flex items-center gap-2">
        <span className="rounded-full border border-[var(--app-primary-border)] bg-[var(--app-primary-soft)] px-2.5 py-0.5 text-xs font-semibold text-[var(--app-primary)]">
          Recurring Automation Plan
        </span>
        <span className="text-xs text-[var(--app-text-muted)]">
          Workspace: {proposal.workspace_id}
        </span>
      </div>
      <h2 className="mt-2 break-words text-xl font-bold text-[var(--app-text)]">{draft.title}</h2>
      <p className="mt-1 text-xs text-[var(--app-text-muted)]">Choose when this runs, review what Swarm will do, or ask Swarm in the AI sidebar to adjust instructions.</p>
    </header>

    <div className="rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4">
      <div className="text-xs font-semibold uppercase tracking-wider text-[var(--app-text-subtle)]">Goal</div>
      <p className="mt-1 break-words text-sm leading-6 text-[var(--app-text)]">{draft.info.goal}</p>
    </div>

    {/* Suggested Intent Presets */}
    <section aria-label="Suggested intent presets" className="rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-3 space-y-2" data-testid="intent-presets-section">
      <div className="flex items-center justify-between gap-2">
        <div className="text-[11px] font-semibold uppercase tracking-wider text-[var(--app-text-subtle)]">Suggested Intent Presets</div>
        <span className="text-[10px] text-[var(--app-primary)]">1-click configuration</span>
      </div>
      <p className="text-[11px] text-[var(--app-text-muted)]">Select an intent preset to configure recommended outcome states and alert behaviors, or customize them below.</p>
      <div className="grid min-w-0 gap-2.5 sm:grid-cols-3">
        {AUTOMATION_INTENT_PRESETS.map(preset => {
          const isSelected = activePresetId === preset.id
          return (
            <button
              key={preset.id}
              type="button"
              disabled={blocked}
              onClick={() => applyIntentPreset(preset.id)}
              className={`flex flex-col justify-between rounded-xl border p-2.5 text-left transition-all cursor-pointer ${
                isSelected
                  ? 'border-[var(--app-primary)] bg-[var(--app-primary-soft)] text-[var(--app-text)] shadow-xs ring-1 ring-[var(--app-primary)]'
                  : 'border-[var(--app-border)] bg-[var(--app-surface)] hover:border-[var(--app-border-strong)] text-[var(--app-text)]'
              }`}
              data-testid={`intent-preset-${preset.id}`}
            >
              <div>
                <div className="flex items-center justify-between gap-1">
                  <span className="text-xs font-bold">{preset.title}</span>
                  <span className="rounded-md border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-1.5 py-0.5 text-[9.5px] font-medium text-[var(--app-text-muted)]">
                    {preset.tag}
                  </span>
                </div>
                <p className="mt-1 text-[10.5px] leading-3.5 text-[var(--app-text-muted)]">
                  {preset.description}
                </p>
              </div>
              <div className="mt-2.5 flex items-center gap-1.5 border-t border-[var(--app-border)]/50 pt-1.5 text-[10px] text-[var(--app-text-subtle)]">
                <span>Outcome:</span>
                <span className="font-semibold text-[var(--app-text)]">{preset.stateLabel}</span>
              </div>
            </button>
          )
        })}
      </div>
    </section>

    {/* Recommended Closing States & Alert Conditions */}
    <fieldset disabled={blocked} className="min-w-0 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-3 space-y-3" data-testid="closing-states-config">
      <legend className="px-1 text-xs font-semibold text-[var(--app-text)]">Closing States & Alert Conditions</legend>
      <p className="text-[11px] text-[var(--app-text-muted)]">
        The AI proposes how to classify completed runs and when to trigger an alert. You can customize the expected outcome state and alert criteria below.
      </p>
      {draft.checkpoints.map((checkpoint, cpIndex) => {
        const closingState = getCheckpointClosingState(checkpoint)
        const alertConditions = getCheckpointAlertConditions(checkpoint)
        return (
          <div
            key={checkpoint.id || `cp-${cpIndex}`}
            className="rounded-lg border border-[var(--app-border)] bg-[var(--app-bg-alt)] p-3 space-y-2.5"
            data-testid={`checkpoint-closing-state-${checkpoint.id || cpIndex}`}
          >
            {draft.checkpoints.length > 1 && (
              <div className="text-[11px] font-semibold text-[var(--app-text)]">
                Step {cpIndex + 1}: {checkpoint.title}
              </div>
            )}
            <div className="grid min-w-0 gap-3 sm:grid-cols-2">
              <label className="block space-y-1 text-xs">
                <span className="font-semibold text-[var(--app-text)]">Expected outcome (closing state)</span>
                <select
                  aria-label="Closing state"
                  className={field}
                  value={closingState}
                  onChange={e => updateCheckpointClosingState(cpIndex, e.target.value)}
                >
                  <option value="routine_clean">Routine clean — Calm minimal status (no action required)</option>
                  <option value="deliverable_ready">Deliverable ready — Highlights generated files or reports</option>
                  <option value="attention_alert">Attention alert — Raises an alert badge and notification</option>
                  <option value="blocked">Blocked — Flags run as waiting on permissions or inputs</option>
                </select>
                <span className="block text-[10.5px] text-[var(--app-text-subtle)] leading-normal">
                  {closingState === 'routine_clean' && 'Clean background run. Renders as a calm, minimal one-line status without cluttering chat.'}
                  {closingState === 'deliverable_ready' && 'Deliverable run. Highlights generated documents or artifacts on the run card with preview links.'}
                  {closingState === 'attention_alert' && 'Alert run. Prominently flags warnings or issues that require your attention.'}
                  {closingState === 'blocked' && 'Blocked run. Surfaces an alert indicating execution needs permissions or external inputs.'}
                </span>
              </label>
              <label className="block space-y-1 text-xs">
                <span className="font-semibold text-[var(--app-text)]">Alert conditions (when to notify)</span>
                <textarea
                  aria-label="Alert conditions"
                  className={field}
                  rows={2}
                  value={alertConditions}
                  onChange={e => updateCheckpointAlertConditions(cpIndex, e.target.value)}
                  placeholder="e.g. Trigger an alert if checks fail, error rates exceed thresholds, or unexpected drift occurs"
                />
                <span className="block text-[10.5px] text-[var(--app-text-subtle)] leading-normal">
                  Swarm evaluates these conditions at completion to decide whether to trigger an alert.
                </span>
              </label>
            </div>
          </div>
        )
      })}
    </fieldset>

    <fieldset disabled={blocked} className="min-w-0 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4 space-y-3">
      <legend className="px-1 text-sm font-semibold text-[var(--app-text)]">Execution Schedule</legend>
      <AutomationV2ScheduleFields schedule={settings.schedule} onChange={schedule => update({ schedule })} />
      <div className="rounded-lg bg-[var(--app-bg-alt)] p-3 text-xs leading-5 text-[var(--app-text-muted)]">
        <span className="font-semibold text-[var(--app-text)]">{scheduleLabel(settings.schedule)}.</span> Accepting activates this schedule with the instructions below. It does not start a run immediately.
      </div>
    </fieldset>

    {display && (
      modalMode ? (
        <section className="space-y-3">
          <div className="flex items-center justify-between gap-2">
            <h3 className="text-sm font-semibold text-[var(--app-text)]">
              What Swarm will do · {draft.checkpoints.length} {draft.checkpoints.length === 1 ? 'step' : 'steps'}
            </h3>
            <span className="rounded-full border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-2.5 py-0.5 text-xs text-[var(--app-text-muted)]">
              Full plan
            </span>
          </div>
          <div className="rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4">
            <StructuredPlanReviewView document={display} />
          </div>
        </section>
      ) : (
        <details className="rounded-xl border border-[var(--app-border)]/60 p-3"><summary className="cursor-pointer text-sm font-medium">What Swarm will do · {draft.checkpoints.length} {draft.checkpoints.length === 1 ? 'step' : 'steps'}</summary><div className="mt-3"><StructuredPlanReviewView document={display} /></div></details>
      )
    )}

    <details className="rounded-xl border border-[var(--app-border)] p-3">
      <summary className="cursor-pointer text-sm font-medium">Edit instructions manually</summary>
      <fieldset disabled={blocked} className="mt-3 space-y-3">
        <label className="block text-xs">
          Goal
          <textarea className={field} value={draft.info.goal} onChange={e => setDraft({ ...draft, info: { ...draft.info, goal: e.target.value } })} />
        </label>
        {draft.checkpoints.map((checkpoint, index) => (
          <div key={checkpoint.id} className="space-y-2 border-t border-[var(--app-border)]/40 pt-2">
            <label className="block text-xs">
              {checkpoint.title} · tasks (one per line)
              <textarea className={field} rows={3} value={(checkpoint.tasks ?? [checkpoint.objective ?? '']).join('\n')} onChange={e => setDraft({ ...draft, checkpoints: draft.checkpoints.map((c, i) => i === index ? { ...c, tasks: e.target.value.split('\n') } : c) })} />
            </label>
            <label className="block text-xs">
              {checkpoint.title} · acceptance criteria (one per line)
              <textarea className={field} rows={2} value={(checkpoint.acceptance_criteria ?? []).join('\n')} onChange={e => setDraft({ ...draft, checkpoints: draft.checkpoints.map((c, i) => i === index ? { ...c, acceptance_criteria: e.target.value.split('\n') } : c) })} />
            </label>
          </div>
        ))}
      </fieldset>
    </details>

    <div className="grid min-w-0 gap-4 sm:grid-cols-2">
      <fieldset disabled={blocked} className="rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4 space-y-2">
        <legend className="px-1 text-xs font-semibold uppercase tracking-wider text-[var(--app-text-subtle)]">Expiration</legend>
        <label className="block space-y-1 text-xs text-[var(--app-text-muted)]">
          <span className="font-semibold text-[var(--app-text)]">Active period</span>
          <select aria-label="Expiration" className={field} value={settings.expiration.kind} onChange={e => update({ expiration: e.target.value === 'indefinite' ? { kind: 'indefinite' } : { kind: 'at', expires_at: undefined } })}>
            <option value="indefinite">Until I stop it (indefinite)</option>
            <option value="at">On a specific date</option>
          </select>
        </label>
        {settings.expiration.kind === 'at' && (
          <label className="block space-y-1 text-xs text-[var(--app-text-muted)]">
            <span>Expiration Date & Time (UTC)</span>
            <input className={field} type="datetime-local" step="0.001" value={settings.expiration.expires_at ? new Date(settings.expiration.expires_at).toISOString().slice(0, -1) : ''} onChange={e => update({ expiration: { kind: 'at', expires_at: e.target.value ? Date.parse(e.target.value + 'Z') : undefined } })} />
          </label>
        )}
      </fieldset>

      {/* Plain-English Run Behavior & Policy Configuration */}
      <fieldset disabled={blocked} className="rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4 space-y-3">
        <legend className="px-1 text-xs font-semibold uppercase tracking-wider text-[var(--app-text-subtle)]">Run behavior & policies</legend>
        <div className="space-y-3 text-xs text-[var(--app-text-muted)]">
          <label className="block space-y-1">
            <span className="font-semibold text-[var(--app-text)]">Missed runs:</span>
            <select
              aria-label="Missed runs"
              className={field}
              value={settings.missed}
              onChange={e => update({ missed: e.target.value as 'skip' | 'coalesce' })}
            >
              <option value="skip">Skip missed runs (resume on next scheduled time)</option>
              <option value="coalesce">Catch up once (run immediately when back online)</option>
            </select>
            <span className="block text-[11px] text-[var(--app-text-subtle)] leading-normal mt-0.5">
              {settings.missed === 'skip'
                ? 'If your computer is sleeping or offline when a run is scheduled, Swarm skips missed runs and starts fresh on the next scheduled interval.'
                : 'If your computer is sleeping or offline when a run is scheduled, Swarm executes a single catch-up run immediately upon reconnecting.'}
            </span>
          </label>
          <label className="block space-y-1">
            <span className="font-semibold text-[var(--app-text)]">Overlap policy:</span>
            <select
              aria-label="Overlap policy"
              className={field}
              value={settings.overlap}
              onChange={e => update({ overlap: e.target.value as 'serialize' | 'independent' })}
            >
              <option value="serialize">Wait for earlier run (execute one at a time in order)</option>
              <option value="independent">Run concurrently (execute in parallel without waiting)</option>
            </select>
            <span className="block text-[11px] text-[var(--app-text-subtle)] leading-normal mt-0.5">
              {settings.overlap === 'serialize'
                ? 'If a previous execution is still running when the next scheduled time arrives, the new run waits in queue for earlier work to finish.'
                : 'If a previous execution is still running when the next scheduled time arrives, the new run starts immediately in parallel.'}
            </span>
          </label>
          <p className="text-[11px] text-[var(--app-text-subtle)]">
            Changes apply to future work; already admitted runs keep their original instructions.
          </p>
        </div>
      </fieldset>
    </div>

    {!modalMode && <p className="rounded-xl bg-[var(--app-bg-alt)] p-3 text-xs leading-5">{scheduleLabel(settings.schedule)}. Accepting activates this schedule with the instructions below. It does not start a run immediately.</p>}
    {dirty && <p role="status" className="rounded-lg bg-[var(--app-warning-soft)] p-3 text-sm text-[var(--app-warning)]">Your manual changes aren’t active yet. Save the schedule, then accept it.</p>}
    {stale && <p role="alert" className="rounded-lg bg-[var(--app-warning-soft)] p-3 text-sm text-[var(--app-warning)]">A newer proposal arrived. Your draft was not applied. <Button size="sm" className="ml-2" onClick={() => { setReviewed(proposal); setDraft(proposal.document); setError('') }}>Discard draft and load current review</Button></p>}
    {(error || invalid) && <p role="alert" className="break-words rounded-lg bg-[var(--app-danger-soft)] p-3 text-sm text-[var(--app-danger)]">{error || invalid}</p>}
    {accepted && <section aria-label="Automation handoff" role="status" className="rounded-xl border border-[var(--app-primary-border)] bg-[var(--app-primary-soft)] p-4"><h3 className="font-semibold text-[var(--app-primary)]">Automation accepted · scheduled</h3><p className="mt-1 text-sm text-[var(--app-text)]">Activated revision {accepted.revision}. Acceptance did not start a run.</p><p className="mt-1 text-sm text-[var(--app-text-muted)]">{accepted.next_due_at ? `Next scheduled time: ${new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'long', timeZone: accepted.document.automation_v2.schedule.timezone || 'UTC' }).format(accepted.next_due_at)}` : 'No next scheduled time is available.'}</p><p className="mt-2 text-xs text-[var(--app-text-muted)]">{accepted.document.automation_v2.schedule.kind === 'interval' ? 'Elapsed timer anchored to this acceptance; displayed in UTC.' : `Wall-clock schedule in ${accepted.document.automation_v2.schedule.timezone}.`} Scheduled is not admitted or running. Open Automation details for observed work.</p></section>}

    <footer className={modalMode ? "sticky bottom-0 -mx-4 -mb-4 sm:-mx-6 sm:-mb-6 flex flex-wrap items-center justify-between gap-3 border-t border-[var(--app-border)] bg-[var(--app-surface)]/95 backdrop-blur-sm p-4 sm:px-6" : "flex flex-wrap justify-end gap-2 border-t border-[var(--app-border)] pt-4"}>
      {modalMode && <span className="text-xs text-[var(--app-text-muted)]">Changes from AI sidebar update this preview live</span>}
      <div className="flex flex-wrap items-center gap-2">
        {onReject && <Button variant="outline" disabled={blocked} onClick={() => void run('reject')}>Reject</Button>}
        {dirty ? <Button disabled={blocked || !!invalid} onClick={() => void run('review')}>{busy ? 'Saving changes…' : 'Save schedule changes'}</Button> : <Button disabled={blocked || !!invalid} onClick={() => void run('accept')}>{accepted ? 'Automation accepted' : rejected ? 'Proposal rejected' : busy ? 'Accepting…' : 'Accept automation'}</Button>}
      </div>
    </footer>
  </section>
}
