import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import {
  AlertCircle,
  Brain,
  Check,
  CheckCircle2,
  ChevronDown,
  ChevronRight,
  Circle,
  Compass,
  Copy,
  FileText,
  ListTodo,
  Pencil,
  PlayCircle,
  RotateCcw,
  Save,
  ShieldCheck,
  Target,
  type LucideIcon,
} from 'lucide-react'
import { Dialog, DialogBackdrop, DialogPanel } from '../../../../components/ui/dialog'
import { Button } from '../../../../components/ui/button'
import { ModalCloseButton } from '../../../../components/ui/modal-close-button'
import { Select } from '../../../../components/ui/select'
import { Textarea } from '../../../../components/ui/textarea'
import { cn } from '../../../../lib/cn'
import { ChatMarkdown } from './chat-markdown'
import type { DesktopSessionPlanCheckpoint, DesktopSessionPlanDocument, DesktopSessionPlanExecutionPolicy, DesktopSessionPlanRecord, DesktopSessionPlanRevisionRecord } from '../types/chat'
import { structuredPlanDocumentToWire } from './structured-plan-document'

interface DesktopPlanModalProps {
  open: boolean
  plan: DesktopSessionPlanRecord | null
  revisions: DesktopSessionPlanRevisionRecord[]
  historyLoading: boolean
  saving: boolean
  error: string | null
  onOpenChange: (open: boolean) => void
  executing?: boolean
  onCopy: (text: string) => Promise<boolean>
  onSave: (planText: string, document?: Record<string, unknown>) => Promise<void>
  onApproveStart?: (input: { executionGranularity: 'checkpointed' | 'run_through'; continueAutomatically: boolean; continuationPolicy: 'automatic' | 'review_each_checkpoint' }) => Promise<void>
}

function useEscapeToClose(open: boolean, onClose: () => void) {
  useEffect(() => {
    if (!open) {
      return undefined
    }
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.preventDefault()
        onClose()
      }
    }
    window.addEventListener('keydown', handleKeyDown)
    return () => window.removeEventListener('keydown', handleKeyDown)
  }, [open, onClose])
}

function revisionLabel(revision: DesktopSessionPlanRevisionRecord): string {
  if (revision.version > 0) {
    return `Revision ${revision.version}`
  }
  return 'Revision'
}

function revisionOptionLabel(revision: DesktopSessionPlanRevisionRecord): string {
  const summary = revision.updateSummary || revision.updateKind || revision.updateScope || 'Plan snapshot'
  return `${revisionLabel(revision)} — ${summary}`
}

function SectionEyebrow({ children, className }: { children: string; className?: string }) {
  return <div className={cn('text-xs font-semibold uppercase tracking-[0.08em] text-[var(--app-text-subtle)]', className)}>{children}</div>
}

function TextBlock({ value }: { value: string }) {
  if (!value.trim()) {
    return null
  }
  return <p className="whitespace-pre-wrap break-words text-sm leading-6 text-[var(--app-text)]">{value}</p>
}

function BulletList({ values, mono = false }: { values: string[]; mono?: boolean }) {
  if (values.length === 0) {
    return null
  }
  return (
    <ul className="grid gap-1.5">
      {values.map((value, index) => (
        <li key={`${index}:${value}`} className={cn('flex min-w-0 gap-2 text-sm leading-6 text-[var(--app-text)]', mono ? 'font-mono text-xs' : '')}>
          <span className="mt-2.5 size-1.5 shrink-0 rounded-full bg-[var(--app-primary)]" />
          <span className="min-w-0 whitespace-pre-wrap break-words">{value}</span>
        </li>
      ))}
    </ul>
  )
}

function PlanInfoSection({ title, icon: Icon, children }: { title: string; icon: LucideIcon; children: React.ReactNode }) {
  return (
    <section className="grid gap-2 py-4 last:pb-0">
      <div className="flex items-center gap-2">
        <Icon className="size-4 shrink-0 text-[var(--app-primary)]" />
        <h4 className="text-sm font-semibold text-[var(--app-text)]">{title}</h4>
      </div>
      <div className="min-w-0 pl-6">{children}</div>
    </section>
  )
}

function planExecutionSelectionFromPolicy(policy: DesktopSessionPlanExecutionPolicy | null | undefined): {
  executionGranularity: 'checkpointed' | 'run_through'
  continueAutomatically: boolean
} {
  const shape = (policy?.shape ?? '').trim().toLowerCase()
  const mode = (policy?.mode ?? '').trim().toLowerCase()
  if (shape === 'single_run') {
    return { executionGranularity: 'run_through', continueAutomatically: true }
  }
  return {
    executionGranularity: 'checkpointed',
    continueAutomatically: mode === 'automatic',
  }
}

function PlanDetails({ document }: { document: DesktopSessionPlanDocument }) {
  const validationFiles = document.info.validationStrategy.trim() !== '' || document.info.relevantFiles.length > 0
  const hasDetails = Boolean(
    document.info.goal
      || document.info.scope
      || document.info.decisions.length
      || validationFiles
      || document.info.successCriteria.length
      || document.info.constraints.length
      || document.info.assumptions.length
      || document.info.openQuestions.length,
  )

  return (
    <section className="min-w-0 content-start">
      <SectionEyebrow>Plan details</SectionEyebrow>
      {hasDetails ? (
        <div className="mt-2 grid">
          {document.info.goal ? (
            <PlanInfoSection title="Goal" icon={Target}>
              <TextBlock value={document.info.goal} />
            </PlanInfoSection>
          ) : null}
          {document.info.scope ? (
            <PlanInfoSection title="Scope" icon={Compass}>
              <TextBlock value={document.info.scope} />
            </PlanInfoSection>
          ) : null}
          {document.info.decisions.length > 0 ? (
            <PlanInfoSection title="Decisions" icon={Brain}>
              <BulletList values={document.info.decisions} />
            </PlanInfoSection>
          ) : null}
          {validationFiles ? (
            <PlanInfoSection title="Validation & files" icon={ShieldCheck}>
              <div className="grid gap-3">
                <TextBlock value={document.info.validationStrategy} />
                {document.info.relevantFiles.length > 0 ? (
                  <div className="grid gap-2">
                    <div className="flex items-center gap-1.5 text-[11px] font-semibold uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">
                      <FileText className="size-3.5 shrink-0 text-[var(--app-primary)]" />
                      Relevant files
                    </div>
                    <BulletList values={document.info.relevantFiles} mono />
                  </div>
                ) : null}
              </div>
            </PlanInfoSection>
          ) : null}
          {document.info.successCriteria.length > 0 ? (
            <PlanInfoSection title="Success criteria" icon={CheckCircle2}>
              <BulletList values={document.info.successCriteria} />
            </PlanInfoSection>
          ) : null}
          {document.info.constraints.length > 0 ? (
            <PlanInfoSection title="Constraints" icon={ShieldCheck}>
              <BulletList values={document.info.constraints} />
            </PlanInfoSection>
          ) : null}
          {document.info.assumptions.length > 0 ? (
            <PlanInfoSection title="Assumptions" icon={Compass}>
              <BulletList values={document.info.assumptions} />
            </PlanInfoSection>
          ) : null}
          {document.info.openQuestions.length > 0 ? (
            <PlanInfoSection title="Open questions" icon={ListTodo}>
              <BulletList values={document.info.openQuestions} />
            </PlanInfoSection>
          ) : null}
        </div>
      ) : (
        <p className="mt-3 rounded-xl border border-dashed border-[var(--app-border)] px-3 py-3 text-sm text-[var(--app-text-muted)]">No plan details are defined.</p>
      )}
    </section>
  )
}

function CheckpointStatusIcon({ status, active }: { status: string; active: boolean }) {
  const normStatus = status.toLowerCase()
  if (normStatus === 'completed' || normStatus === 'done' || normStatus === 'success') {
    return <CheckCircle2 className="size-4 text-[var(--app-success)]" />
  }
  if (active || normStatus === 'in_progress' || normStatus === 'in-progress' || normStatus === 'active') {
    return <PlayCircle className="size-4 text-[var(--app-primary)]" />
  }
  return <Circle className="size-4 text-[var(--app-text-muted)]" />
}

function formatStatusLabel(status: string, active: boolean): string {
  if (active) {
    return 'Active'
  }
  const trimmed = status.trim()
  if (!trimmed) {
    return ''
  }
  return trimmed
    .replace(/[-_]+/g, ' ')
    .replace(/\w\S*/g, (word) => word.charAt(0).toUpperCase() + word.slice(1).toLowerCase())
}

function CheckpointStatusText({ status, active }: { status: string; active: boolean }) {
  const label = formatStatusLabel(status, active)
  if (!label) {
    return null
  }
  const normStatus = status.toLowerCase()
  const done = normStatus === 'done' || normStatus === 'completed' || normStatus === 'success'
  return (
    <span className={cn('shrink-0 text-xs font-medium', done ? 'text-[var(--app-success)]' : active ? 'text-[var(--app-primary)]' : 'text-[var(--app-text-muted)]')}>
      {label}
    </span>
  )
}

function CheckpointSection({ title, values, mono = false }: { title: string; values: string[]; mono?: boolean }) {
  if (values.length === 0) {
    return null
  }
  return (
    <div className="grid min-w-0 gap-1.5">
      <div className="text-[11px] font-semibold uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">{title}</div>
      <BulletList values={values} mono={mono} />
    </div>
  )
}

function CheckpointTextSection({ title, value }: { title: string; value: string }) {
  if (!value.trim()) {
    return null
  }
  return (
    <div className="grid min-w-0 gap-1.5">
      <div className="text-[11px] font-semibold uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">{title}</div>
      <p className="whitespace-pre-wrap break-words text-sm leading-6 text-[var(--app-text-muted)]">{value}</p>
    </div>
  )
}

function CheckpointRow({
  checkpoint,
  index,
  active,
  expanded,
  onToggle,
}: {
  checkpoint: DesktopSessionPlanCheckpoint
  index: number
  active: boolean
  expanded: boolean
  onToggle: () => void
}) {
  const title = checkpoint.title || checkpoint.id || 'Untitled checkpoint'
  return (
    <div className={cn('border-b border-[var(--app-border)] last:border-b-0', active ? 'bg-[var(--app-primary-soft)]/35' : '')}>
      <button
        type="button"
        onClick={onToggle}
        className={cn(
          'grid w-full grid-cols-[auto_minmax(0,1fr)_auto_auto] items-center gap-3 px-1 py-3 text-left transition hover:bg-[var(--app-surface-subtle)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--app-focus-ring)] sm:px-2',
          active ? 'border-l-2 border-[var(--app-primary)] pl-2 sm:pl-3' : 'border-l-2 border-transparent pl-2 sm:pl-3',
        )}
        aria-expanded={expanded}
      >
        <CheckpointStatusIcon status={checkpoint.status} active={active} />
        <span className={cn('min-w-0 truncate text-sm font-medium', active ? 'text-[var(--app-primary)]' : 'text-[var(--app-text)]')}>
          <span className="mr-1.5 font-semibold text-[var(--app-text-muted)]">{index + 1}.</span>
          {title}
        </span>
        <CheckpointStatusText status={checkpoint.status} active={active} />
        {expanded ? <ChevronDown className="size-4 text-[var(--app-text-muted)]" /> : <ChevronRight className="size-4 text-[var(--app-text-muted)]" />}
      </button>
      {expanded ? (
        <div className={cn('grid gap-3 pb-4 pl-12 pr-3 pt-1', active ? 'border-l-2 border-[var(--app-primary)]' : 'border-l-2 border-transparent')}>
          <CheckpointTextSection title="Objective" value={checkpoint.objective} />
          <CheckpointSection title="Tasks" values={checkpoint.tasks} />
          <CheckpointSection title="Acceptance" values={checkpoint.acceptanceCriteria} />
          <CheckpointTextSection title="Notes" value={checkpoint.notes} />
          <CheckpointTextSection title="Report" value={checkpoint.report} />
          <CheckpointTextSection title="Result" value={checkpoint.result} />
          <CheckpointSection title="Changed files" values={checkpoint.changedFiles} mono />
          <CheckpointSection title="Validation" values={checkpoint.validation} />
        </div>
      ) : null}
    </div>
  )
}

function CheckpointList({ document }: { document: DesktopSessionPlanDocument }) {
  const activeID = document.activeCheckpointId.trim()
  const activeLabel = activeID || 'none'
  const [collapsedIds, setCollapsedIds] = useState<Set<string>>(() => new Set())

  useEffect(() => {
    setCollapsedIds(new Set())
  }, [document.id, document.revisionId])

  return (
    <section className="min-w-0 content-start">
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <SectionEyebrow>Checkpoints</SectionEyebrow>
          <div className="mt-1 text-sm text-[var(--app-text-muted)]">
            {document.checkpoints.length} step{document.checkpoints.length === 1 ? '' : 's'} · {activeLabel} active
          </div>
        </div>
      </div>
      {document.checkpoints.length > 0 ? (
        <div className="mt-4">
          {document.checkpoints.map((checkpoint, index) => {
            const active = activeID !== '' && checkpoint.id === activeID
            const rowId = checkpoint.id || `${checkpoint.order}:${checkpoint.title}`
            return (
              <CheckpointRow
                key={rowId}
                checkpoint={checkpoint}
                index={index}
                active={active}
                expanded={!collapsedIds.has(rowId)}
                onToggle={() => {
                  setCollapsedIds((current) => {
                    const next = new Set(current)
                    if (next.has(rowId)) {
                      next.delete(rowId)
                    } else {
                      next.add(rowId)
                    }
                    return next
                  })
                }}
              />
            )
          })}
        </div>
      ) : (
        <p className="mt-3 rounded-xl border border-dashed border-[var(--app-border)] px-3 py-3 text-sm text-[var(--app-text-muted)]">No checkpoints are defined.</p>
      )}
    </section>
  )
}

function PlanModalDocumentView({ document, emptyText }: { document: DesktopSessionPlanDocument | null; emptyText: string }) {
  if (!document) {
    return <section className="rounded-2xl border border-dashed border-[var(--app-border)] bg-[var(--app-bg-alt)] px-4 py-5 text-sm text-[var(--app-text-muted)]">{emptyText}</section>
  }
  return (
    <div className="grid min-h-0 grid-cols-1 gap-6 min-[901px]:grid-cols-[minmax(380px,0.85fr)_minmax(520px,1.15fr)] min-[901px]:gap-0">
      <div className="min-w-0 min-[901px]:pr-6">
        <PlanDetails document={document} />
      </div>
      <div className="min-w-0 border-t border-[var(--app-border)] pt-6 min-[901px]:border-l min-[901px]:border-t-0 min-[901px]:pl-6 min-[901px]:pt-0">
        <CheckpointList document={document} />
      </div>
    </div>
  )
}

export function DesktopPlanModal({
  open,
  plan,
  revisions,
  historyLoading: _historyLoading,
  saving,
  executing = false,
  error,
  onOpenChange,
  onCopy,
  onSave,
  onApproveStart,
}: DesktopPlanModalProps) {
  const [draft, setDraft] = useState('')
  const [documentDraft, setDocumentDraft] = useState('')
  const [documentDraftError, setDocumentDraftError] = useState<string | null>(null)
  const [editing, setEditing] = useState(false)
  const [copyState, setCopyState] = useState<'idle' | 'copied' | 'error'>('idle')
  const [selectedRevisionKey, setSelectedRevisionKey] = useState('current')
  const [executionGranularity, setExecutionGranularity] = useState<'checkpointed' | 'run_through'>('checkpointed')
  const [continueAutomatically, setContinueAutomatically] = useState(true)
  const [revisionSelectWidth, setRevisionSelectWidth] = useState<number | undefined>(undefined)
  const revisionSelectSizerRef = useRef<HTMLSpanElement | null>(null)

  useEffect(() => {
    if (!open) {
      return
    }
    setDraft(plan?.plan ?? '')
    setDocumentDraft(plan?.document ? JSON.stringify(structuredPlanDocumentToWire(plan.document), null, 2) : '')
    setDocumentDraftError(null)
    setEditing(false)
    setCopyState('idle')
    setSelectedRevisionKey('current')
    const executionSelection = planExecutionSelectionFromPolicy(plan?.document?.executionPolicy)
    setExecutionGranularity(executionSelection.executionGranularity)
    setContinueAutomatically(executionSelection.continueAutomatically)
  }, [open, plan?.id, plan?.updatedAt, plan?.plan, plan?.document])

  useEscapeToClose(open, () => onOpenChange(false))

  const title = useMemo(() => {
    const value = plan?.title?.trim() ?? ''
    return value || 'Current Plan'
  }, [plan?.title])

  const selectedRevision = useMemo(() => {
    if (selectedRevisionKey === 'current') {
      return null
    }
    return revisions.find((revision) => revision.key === selectedRevisionKey) ?? null
  }, [revisions, selectedRevisionKey])

  const selectedRevisionLabel = selectedRevision ? revisionOptionLabel(selectedRevision) : 'Current revision'

  useLayoutEffect(() => {
    if (!open) {
      return
    }
    const sizer = revisionSelectSizerRef.current
    if (!sizer) {
      return
    }
    const textWidth = Math.ceil(sizer.getBoundingClientRect().width)
    const selectChromeWidth = 54
    const maxWidth = 360
    setRevisionSelectWidth(Math.min(textWidth + selectChromeWidth, maxWidth))
  }, [open, selectedRevisionLabel])

  if (!open) {
    return null
  }

  const viewingRevision = selectedRevision !== null
  const selectedDocument = viewingRevision ? selectedRevision.document : (plan?.document ?? null)
  const preview = viewingRevision ? selectedRevision.plan : (draft.trim() !== '' ? draft : (plan?.plan ?? ''))
  const currentDocumentWire = plan?.document ? JSON.stringify(structuredPlanDocumentToWire(plan.document), null, 2) : ''
  const dirty = draft !== (plan?.plan ?? '') || documentDraft !== currentDocumentWire

  const handleCopy = async () => {
    const ok = await onCopy(preview)
    setCopyState(ok ? 'copied' : 'error')
  }

  const handleSave = async () => {
    setDocumentDraftError(null)
    let parsedDocument: Record<string, unknown> | undefined
    if (documentDraft.trim()) {
      try {
        const parsed = JSON.parse(documentDraft) as unknown
        if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
          throw new Error('Structured document must be a JSON object.')
        }
        parsedDocument = parsed as Record<string, unknown>
      } catch (error) {
        setDocumentDraftError(error instanceof Error ? error.message : 'Structured document JSON is invalid.')
        return
      }
    }
    await onSave(draft, parsedDocument)
    setEditing(false)
    setSelectedRevisionKey('current')
  }

  const handleRestoreRevision = async () => {
    if (!selectedRevision) {
      return
    }
    setDraft(selectedRevision.plan)
    const revisionDocument = selectedRevision.document ? structuredPlanDocumentToWire(selectedRevision.document) : undefined
    await onSave(selectedRevision.plan, revisionDocument)
    setEditing(false)
    setSelectedRevisionKey('current')
  }

  const handleCancelEdit = () => {
    setDraft(plan?.plan ?? '')
    setDocumentDraft(currentDocumentWire)
    setDocumentDraftError(null)
    setEditing(false)
  }

  const effectiveContinueAutomatically = executionGranularity === 'run_through' ? true : continueAutomatically
  const effectiveContinuationPolicy: 'automatic' | 'review_each_checkpoint' = effectiveContinueAutomatically ? 'automatic' : 'review_each_checkpoint'
  const executionChoiceLabel = executionGranularity === 'run_through'
    ? 'Execute as one run'
    : effectiveContinueAutomatically
      ? 'Execute checkpoint by checkpoint automatically'
      : 'Execute checkpoint by checkpoint with review pauses'

  const handleApproveStart = async () => {
    if (!onApproveStart || editing || viewingRevision) return
    await onApproveStart({ executionGranularity, continueAutomatically: effectiveContinueAutomatically, continuationPolicy: effectiveContinuationPolicy })
  }

  return (
    <Dialog role="dialog" aria-modal="true" aria-label={title} className="z-[80] p-3 sm:p-6">
      <DialogBackdrop onClick={() => onOpenChange(false)} />
      <DialogPanel className="grid max-h-[min(900px,calc(100vh-48px))] w-[min(1180px,calc(100vw-24px))] grid-rows-[auto_minmax(0,1fr)] overflow-hidden rounded-3xl border border-[var(--app-border-strong)] bg-[var(--app-surface)] p-0 shadow-[var(--shadow-panel)] sm:w-[min(1280px,calc(100vw-48px))]">
        <div className="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-4 border-b border-[var(--app-border)] px-6 py-4">
          <div className="min-w-0">
            <h2 className="truncate text-xl font-semibold tracking-tight text-[var(--app-text)]">{title}</h2>
          </div>
          <div className="flex min-w-0 shrink-0 flex-nowrap items-center justify-end gap-2 overflow-x-auto whitespace-nowrap">
            <span
              ref={revisionSelectSizerRef}
              aria-hidden="true"
              className="pointer-events-none absolute -z-10 whitespace-pre text-sm opacity-0"
            >
              {selectedRevisionLabel}
            </span>
            <Select
              value={selectedRevisionKey}
              onChange={(event) => {
                setEditing(false)
                setSelectedRevisionKey(event.target.value)
              }}
              className="h-9 min-h-9 max-w-[360px] shrink-0 rounded-lg bg-[var(--app-surface)] py-1.5 pl-3 pr-10 text-sm"
              style={revisionSelectWidth ? { width: `${revisionSelectWidth}px` } : undefined}
              aria-label="Select plan revision"
              disabled={editing}
            >
              <option value="current">Current revision</option>
              {revisions.map((revision) => (
                <option key={revision.key} value={revision.key}>
                  {revisionOptionLabel(revision)}
                </option>
              ))}
            </Select>
            {viewingRevision ? (
              <Button type="button" variant="primary" size="sm" onClick={() => void handleRestoreRevision()} disabled={saving || editing || executing}>
                <RotateCcw className={cn('size-4', saving ? 'animate-pulse' : '')} />
                {saving ? 'Activating…' : 'Activate revision'}
              </Button>
            ) : null}
            {!editing && !viewingRevision && plan?.document ? (
              <Button type="button" variant="primary" size="sm" onClick={() => void handleApproveStart()} disabled={!onApproveStart || saving || executing}>
                <PlayCircle className={cn('size-4', executing ? 'animate-pulse' : '')} />
                {executing ? 'Starting…' : 'Approve & Start'}
              </Button>
            ) : null}
            <Button type="button" variant="outline" size="sm" onClick={() => void handleCopy()}>
              {copyState === 'copied' ? (
                <Check className="size-4" />
              ) : copyState === 'error' ? (
                <AlertCircle className="size-4" />
              ) : (
                <Copy className="size-4" />
              )}
              {copyState === 'copied' ? 'Copied' : copyState === 'error' ? 'Copy failed' : 'Copy'}
            </Button>
            {editing ? (
              <>
                <Button type="button" variant="secondary" size="sm" onClick={handleCancelEdit} disabled={saving}>
                  Cancel
                </Button>
                <Button type="button" variant="primary" size="sm" onClick={() => void handleSave()} disabled={saving || !dirty}>
                  <Save className={cn('size-4', saving ? 'animate-pulse' : '')} />
                  {saving ? 'Saving…' : 'Save plan'}
                </Button>
              </>
            ) : (
              <Button
                type="button"
                variant="primary"
                size="sm"
                onClick={() => {
                  setSelectedRevisionKey('current')
                  setEditing(true)
                }}
              >
                <Pencil className="size-4" />
                Edit plan
              </Button>
            )}
            <ModalCloseButton onClick={() => onOpenChange(false)} aria-label="Close current plan dialog" />
          </div>
        </div>

        <div className="min-h-0 overflow-y-auto px-6 py-5">
          {editing ? (
            <section className="grid gap-3">
              {plan?.document ? (
                <>
                  <label className="grid gap-2">
                    <span className="text-xs font-semibold uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Structured plan document</span>
                    <Textarea
                      value={documentDraft}
                      onChange={(event) => {
                        setDocumentDraft(event.target.value)
                        setDocumentDraftError(null)
                      }}
                      placeholder="Edit structured plan info and checkpoints as JSON…"
                      className="min-h-[420px] w-full resize-y bg-[var(--app-bg-alt)] font-mono text-sm leading-6"
                    />
                  </label>
                  {documentDraftError ? (
                    <p className="rounded-xl border border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-3 py-2 text-xs text-[var(--app-danger)]">{documentDraftError}</p>
                  ) : null}
                  <p className="text-xs text-[var(--app-text-muted)]">
                    This edits the canonical structured plan document: base info plus checkpoint objects. Saving records one revision.
                  </p>
                </>
              ) : (
                <>
                  <Textarea
                    value={draft}
                    onChange={(event) => setDraft(event.target.value)}
                    placeholder="Write or paste the current session plan…"
                    className="min-h-[420px] w-full resize-y bg-[var(--app-bg-alt)] font-mono text-sm leading-6"
                  />
                  <p className="text-xs text-[var(--app-text-muted)]">
                    No structured document exists yet; saving this display text records a new revision.
                  </p>
                </>
              )}
            </section>
          ) : (
            <div className="grid gap-4">
              {!viewingRevision && selectedDocument ? (
                <section className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-bg-alt)] p-4">
                  <SectionEyebrow>Execution on approval</SectionEyebrow>
                  <div className="mt-3 grid gap-3">
                    <div className="rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] px-3 py-2 text-sm text-[var(--app-text)]">
                      <span className="font-semibold">Selected: {executionChoiceLabel}.</span>{' '}
                      Approval will send <code className="rounded bg-[var(--app-bg-alt)] px-1 py-0.5">execution_granularity={executionGranularity}</code>{' '}
                      and <code className="rounded bg-[var(--app-bg-alt)] px-1 py-0.5">continuation_policy={effectiveContinuationPolicy}</code>.
                    </div>
                    <label className="grid gap-1.5 text-sm text-[var(--app-text)]">
                      <span className="font-medium">Execution style</span>
                      <Select
                        value={executionGranularity}
                        onChange={(event) => setExecutionGranularity(event.target.value === 'checkpointed' ? 'checkpointed' : 'run_through')}
                        disabled={executing}
                      >
                        <option value="checkpointed">Execute checkpoint by checkpoint</option>
                        <option value="run_through">Execute as one run</option>
                      </Select>
                      <span className="text-xs text-[var(--app-text-muted)]">
                        {executionGranularity === 'run_through'
                          ? 'Runs the approved plan as a single fresh-context execution.'
                          : 'Runs each checkpoint separately with fresh context.'}
                      </span>
                    </label>
                    {executionGranularity === 'checkpointed' ? (
                      <label className="grid gap-1 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] px-3 py-2 text-sm text-[var(--app-text)]">
                        <span className="flex items-center gap-2">
                          <input
                            type="checkbox"
                            checked={continueAutomatically}
                            onChange={(event) => setContinueAutomatically(event.target.checked)}
                            disabled={executing}
                          />
                          Continue automatically after each completed checkpoint
                        </span>
                        <span className="pl-6 text-xs text-[var(--app-text-muted)]">
                          {continueAutomatically
                            ? 'Swarm starts the next checkpoint automatically after successful completion, and still stops for review requests, blockers, failures, or final completion.'
                            : 'If unchecked, Swarm pauses for your review before starting the next checkpoint.'}
                        </span>
                      </label>
                    ) : null}
                  </div>
                </section>
              ) : null}
              {selectedDocument ? (
                <PlanModalDocumentView document={selectedDocument} emptyText="No structured plan data is available for this plan." />
              ) : (
                <section className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-bg-alt)] p-5">
                  {preview.trim() ? (
                    <ChatMarkdown content={preview} className="text-base leading-7" />
                  ) : (
                    <p className="text-sm text-[var(--app-text-muted)]">
                      No active plan yet. Use Edit plan to create one for this session.
                    </p>
                  )}
                </section>
              )}
            </div>
          )}

          {error ? (
            <div className="mt-4 rounded-2xl border border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-4 py-3 text-sm text-[var(--app-danger)]">
              {error}
            </div>
          ) : null}
        </div>

      </DialogPanel>
    </Dialog>
  )
}
