import { useEffect, useMemo, useRef, useState } from 'react'
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
  PlayCircle,
  RotateCcw,
  ShieldCheck,
  Target,
  type LucideIcon,
} from 'lucide-react'
import { Dialog, DialogBackdrop, DialogPanel } from '../../../../components/ui/dialog'
import { Button } from '../../../../components/ui/button'
import { ModalCloseButton } from '../../../../components/ui/modal-close-button'
import { Select } from '../../../../components/ui/select'
import { cn } from '../../../../lib/cn'
import { ChatMarkdown } from './chat-markdown'
import type { DesktopSessionPlanCheckpoint, DesktopSessionPlanDocument, DesktopSessionPlanExecutionPolicy, DesktopSessionPlanRecord, DesktopSessionPlanRevisionRecord } from '../types/chat'

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
  onRestoreRevision: (revision: DesktopSessionPlanRevisionRecord, input?: DesktopPlanRecoveryInput) => Promise<void>
  onApproveStart?: (input: { executionGranularity: 'checkpointed' | 'run_through'; continueAutomatically: boolean; continuationPolicy: 'automatic' | 'review_each_checkpoint' }) => Promise<void>
}

export interface DesktopPlanRecoveryInput {
  checkpointId?: string
  executionGranularity?: 'checkpointed' | 'run_through'
  continuationPolicy?: 'automatic' | 'review_each_checkpoint'
  continueAutomatically?: boolean
  restart?: boolean
  start?: boolean
  skipPrior?: boolean
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

function revisionKindLabel(revision: DesktopSessionPlanRevisionRecord): string {
  if (revision.revisionKind === 'execution' || revision.checkpoint) {
    return 'Execution snapshot'
  }
  return 'Plan version'
}

function revisionOptionLabel(revision: DesktopSessionPlanRevisionRecord): string {
  const restored = revision.restoredFromVersion > 0 ? `Restored from revision ${revision.restoredFromVersion}` : ''
  const summary = restored || revision.updateSummary || revision.updateKind || revision.updateScope || 'Plan snapshot'
  return `${revisionLabel(revision)} · ${revisionKindLabel(revision)} — ${summary}`
}

function formatTimestamp(value: number): string {
  if (!Number.isFinite(value) || value <= 0) return ''
  try {
    return new Date(value).toLocaleString()
  } catch {
    return ''
  }
}

function displayValue(value: string): string {
  return value.trim() || 'Not set'
}

function displayExecutionMode(mode: 'automatic' | 'review_each_checkpoint'): string {
  return mode === 'automatic' ? 'Automatic' : 'Review each checkpoint'
}

function displayExecutionShape(shape: 'checkpointed' | 'run_through'): string {
  return shape === 'run_through' ? 'Run through as one execution' : 'Checkpoint by checkpoint'
}

function firstNonBlankText(...values: Array<string | null | undefined>): string {
  for (const value of values) {
    const normalized = (value ?? '').trim()
    if (normalized) return normalized
  }
  return ''
}

function appendTextSection(lines: string[], title: string, value: string) {
  const normalized = value.trim()
  if (!normalized) return
  lines.push('', `## ${title}`, '', normalized)
}

function appendBulletSection(lines: string[], title: string, values: string[]) {
  const normalized = values.map((value) => value.trim()).filter(Boolean)
  if (normalized.length === 0) return
  lines.push('', `## ${title}`, '')
  normalized.forEach((value) => lines.push(`- ${value}`))
}

function appendFieldList(lines: string[], title: string, fields: Array<[string, string | number | undefined]>) {
  const normalized = fields
    .map(([label, value]): [string, string] => [label, typeof value === 'number' ? (value > 0 ? String(value) : '') : (value ?? '').trim()])
    .filter(([, value]) => value !== '')
  if (normalized.length === 0) return
  lines.push('', `## ${title}`, '')
  normalized.forEach(([label, value]) => lines.push(`- ${label}: ${value}`))
}

export function structuredPlanCopyText(document: DesktopSessionPlanDocument): string {
  const lines: string[] = [`# ${firstNonBlankText(document.title, document.id, 'Plan')}`]
  const identityFields: Array<[string, string]> = [
    ['Plan ID', document.id],
    ['Status', document.status],
    ['Schema version', document.schemaVersion],
    ['Revision', document.revisionId],
    ['Active checkpoint', document.activeCheckpointId],
  ]
  const presentIdentityFields = identityFields.filter(([, value]) => value.trim() !== '')
  if (presentIdentityFields.length > 0) {
    lines.push('')
    presentIdentityFields.forEach(([label, value]) => lines.push(`- ${label}: ${value}`))
  }

  appendTextSection(lines, 'Goal', document.info.goal)
  appendTextSection(lines, 'Scope', document.info.scope)
  appendTextSection(lines, 'Context', document.info.context)
  appendBulletSection(lines, 'Decisions', document.info.decisions)
  appendBulletSection(lines, 'Success criteria', document.info.successCriteria)
  appendBulletSection(lines, 'Constraints', document.info.constraints)
  appendBulletSection(lines, 'Assumptions', document.info.assumptions)
  appendBulletSection(lines, 'Open questions', document.info.openQuestions)
  appendBulletSection(lines, 'Relevant files', document.info.relevantFiles)
  appendTextSection(lines, 'Validation strategy', document.info.validationStrategy)
  appendFieldList(lines, 'Execution policy', [
    ['Mode', document.executionPolicy?.mode],
    ['Shape', document.executionPolicy?.shape],
    ['Follow-up checkpoint policy', document.executionPolicy?.followupCheckpointPolicy],
  ])
  appendFieldList(lines, 'Execution state', [
    ['Status', document.executionState?.status],
    ['Active attempt', document.executionState?.activeAttemptId],
    ['Current run', document.executionState?.currentRunId],
    ['Current session', document.executionState?.currentSessionId],
    ['Parent session', document.executionState?.parentSessionId],
    ['Last checkpoint', document.executionState?.lastCheckpointId],
    ['Last attempt', document.executionState?.lastAttemptId],
    ['Last outcome', document.executionState?.lastOutcome],
    ['Started at', document.executionState?.startedAt],
    ['Updated at', document.executionState?.updatedAt],
    ['Completed at', document.executionState?.completedAt],
  ])

  if (document.originalCheckpoints.length > 0) {
    lines.push('', '## Original checkpoint plan')
    document.originalCheckpoints.forEach((checkpoint, index) => {
      lines.push('', `### ${index + 1}. ${firstNonBlankText(checkpoint.title, checkpoint.id, 'Checkpoint')}`)
      const fields: Array<[string, string | number]> = [
        ['ID', checkpoint.id],
        ['Status', checkpoint.status],
        ['Objective', checkpoint.objective],
        ['Notes', checkpoint.notes],
        ['Report', checkpoint.report],
        ['Result', checkpoint.result],
      ]
      fields
        .map(([label, value]): [string, string] => [label, typeof value === 'number' ? (value > 0 ? String(value) : '') : value.trim()])
        .filter(([, value]) => value !== '')
        .forEach(([label, value]) => lines.push(`- ${label}: ${value}`))
      if (checkpoint.tasks.length > 0) {
        lines.push('- Tasks:')
        checkpoint.tasks.forEach((task) => lines.push(`  - ${task}`))
      }
      if (checkpoint.acceptanceCriteria.length > 0) {
        lines.push('- Acceptance criteria:')
        checkpoint.acceptanceCriteria.forEach((criterion) => lines.push(`  - ${criterion}`))
      }
    })
  }

  if (document.checkpoints.length > 0) {
    lines.push('', document.originalCheckpoints.length > 0 ? '## Execution checkpoints' : '## Checkpoints')
    document.checkpoints.forEach((checkpoint, index) => {
      lines.push('', `### ${index + 1}. ${firstNonBlankText(checkpoint.title, checkpoint.id, 'Checkpoint')}`)
      const fields: Array<[string, string | number]> = [
        ['ID', checkpoint.id],
        ['Status', checkpoint.status],
        ['Objective', checkpoint.objective],
        ['Attempt', checkpoint.attemptId],
        ['Run', checkpoint.runId],
        ['Session', checkpoint.sessionId],
        ['Started at', checkpoint.startedAt],
        ['Completed at', checkpoint.completedAt],
        ['Notes', checkpoint.notes],
        ['Report', checkpoint.report],
        ['Result', checkpoint.result],
      ]
      fields
        .map(([label, value]): [string, string] => [label, typeof value === 'number' ? (value > 0 ? String(value) : '') : value.trim()])
        .filter(([, value]) => value !== '')
        .forEach(([label, value]) => lines.push(`- ${label}: ${value}`))
      if (checkpoint.tasks.length > 0) {
        lines.push('- Tasks:')
        checkpoint.tasks.forEach((task) => lines.push(`  - ${task}`))
      }
      if (checkpoint.acceptanceCriteria.length > 0) {
        lines.push('- Acceptance criteria:')
        checkpoint.acceptanceCriteria.forEach((criterion) => lines.push(`  - ${criterion}`))
      }
      if (checkpoint.changedFiles.length > 0) {
        lines.push('- Changed files:')
        checkpoint.changedFiles.forEach((file) => lines.push(`  - ${file}`))
      }
      if (checkpoint.validation.length > 0) {
        lines.push('- Validation:')
        checkpoint.validation.forEach((entry) => lines.push(`  - ${entry}`))
      }
      if (checkpoint.review) {
        appendFieldList(lines, 'Review', [
          ['Status', checkpoint.review.status],
          ['Reviewer', checkpoint.review.reviewerId],
          ['Reviewer type', checkpoint.review.reviewerType],
          ['Result', checkpoint.review.result],
          ['Notes', checkpoint.review.notes],
          ['Reviewed at', checkpoint.review.reviewedAt],
        ])
      }
    })
  }

  return lines.join('\n').replace(/\n{3,}/g, '\n\n').trim()
}

export function selectedPlanCopyText(document: DesktopSessionPlanDocument | null, markdownFallback: string): string {
  if (document) {
    const structuredText = structuredPlanCopyText(document)
    if (structuredText) return structuredText
  }
  return markdownFallback
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

type PlanApprovalExecutionChoice = 'checkpointed_automatic' | 'run_through' | 'checkpointed_manual'

const planApprovalExecutionChoices: Array<{
  id: PlanApprovalExecutionChoice
  title: string
  description: string
  detail: string
}> = [
  {
    id: 'checkpointed_automatic',
    title: 'Automatic mode',
    description: 'Run checkpoints automatically until completion or until review, blocker, or failure stops execution.',
    detail: 'Default: preserves checkpoint boundaries and continues automatically.',
  },
  {
    id: 'run_through',
    title: 'Single run',
    description: 'Run the approved plan as one fresh-context execution.',
    detail: 'Disabled for automatic checkpoint plans; those must keep checkpoint boundaries.',
  },
  {
    id: 'checkpointed_manual',
    title: 'Manual checkpoint review',
    description: 'Run one checkpoint at a time and pause for your review between checkpoints.',
    detail: 'Use when you want to review every checkpoint before continuing.',
  },
]

function approvalChoiceFromSelection(executionGranularity: 'checkpointed' | 'run_through', continueAutomatically: boolean): PlanApprovalExecutionChoice {
  if (executionGranularity === 'run_through') return 'run_through'
  return continueAutomatically ? 'checkpointed_automatic' : 'checkpointed_manual'
}

function approvalSelectionFromChoice(choice: PlanApprovalExecutionChoice): {
  executionGranularity: 'checkpointed' | 'run_through'
  continueAutomatically: boolean
} {
  if (choice === 'run_through') return { executionGranularity: 'run_through', continueAutomatically: true }
  if (choice === 'checkpointed_manual') return { executionGranularity: 'checkpointed', continueAutomatically: false }
  return { executionGranularity: 'checkpointed', continueAutomatically: true }
}

function disablesSingleRunApproval(policy: DesktopSessionPlanExecutionPolicy | null | undefined): boolean {
  const mode = (policy?.mode ?? '').trim().toLowerCase()
  const shape = (policy?.shape ?? '').trim().toLowerCase()
  return mode === 'automatic' && (shape === '' || shape === 'checkpointed')
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

function CheckpointList({ document, checkpoints = document.checkpoints, title = 'Checkpoints', description }: { document: DesktopSessionPlanDocument; checkpoints?: DesktopSessionPlanCheckpoint[]; title?: string; description?: string }) {
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
          <SectionEyebrow>{title}</SectionEyebrow>
          <div className="mt-1 text-sm text-[var(--app-text-muted)]">
            {description || `${checkpoints.length} step${checkpoints.length === 1 ? '' : 's'} · ${activeLabel} active`}
          </div>
        </div>
      </div>
      {checkpoints.length > 0 ? (
        <div className="mt-4">
          {checkpoints.map((checkpoint, index) => {
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
    <div className="grid min-h-0 gap-4 rounded-2xl border border-[var(--app-border)] bg-[var(--app-bg-alt)] p-5">
      <PlanDetails document={document} />
      {document.originalCheckpoints.length > 0 ? (
        <div className="border-t border-[var(--app-border)] pt-5">
          <CheckpointList
            document={document}
            checkpoints={document.originalCheckpoints}
            title="Original checkpoint plan"
            description="Preserved approved checkpoint boundaries before single-run execution."
          />
        </div>
      ) : null}
      <div className="border-t border-[var(--app-border)] pt-5">
        <CheckpointList
          document={document}
          title={document.originalCheckpoints.length > 0 ? 'Execution checkpoint' : 'Checkpoints'}
          description={document.originalCheckpoints.length > 0 ? 'Single-run execution state; the original checkpoint plan above remains preserved for /plan.' : undefined}
        />
      </div>
    </div>
  )
}

function PlanRevisionHistory({
  revisions,
  selectedRevisionKey,
  historyLoading,
  onSelect,
}: {
  revisions: DesktopSessionPlanRevisionRecord[]
  selectedRevisionKey: string
  historyLoading: boolean
  onSelect: (key: string) => void
}) {
  const selectedRevision = revisions.find((revision) => revision.key === selectedRevisionKey) ?? null
  const selectedLabel = selectedRevision ? revisionOptionLabel(selectedRevision) : 'Current plan · Live structured document'
  return (
    <section className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-bg-alt)] p-4">
      <div className="flex flex-wrap items-end justify-between gap-3">
        <label className="grid min-w-0 flex-1 gap-1.5 text-sm text-[var(--app-text)]">
          <SectionEyebrow>Revision history</SectionEyebrow>
          <Select
            value={selectedRevisionKey}
            onChange={(event) => onSelect(event.target.value)}
            disabled={historyLoading && revisions.length === 0}
          >
            <option value="current">Current plan · Live structured document</option>
            {revisions.map((revision) => (
              <option key={revision.key} value={revision.key}>{revisionOptionLabel(revision)} · {formatTimestamp(revision.createdAt || revision.updatedAt) || 'No timestamp'}</option>
            ))}
          </Select>
        </label>
        {historyLoading ? <span className="pb-2 text-xs text-[var(--app-text-muted)]">Loading…</span> : null}
      </div>
      <p className="mt-2 truncate text-xs text-[var(--app-text-muted)]" title={selectedLabel}>
        {revisions.length > 0 || selectedRevision ? selectedLabel : 'No prior plan definition revisions yet.'}
      </p>
    </section>
  )
}

function PlanRevisionSummary({ revision }: { revision: DesktopSessionPlanRevisionRecord }) {
  const timestamp = formatTimestamp(revision.createdAt || revision.updatedAt)
  return (
    <section className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-bg-alt)] p-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <SectionEyebrow>Whole-plan snapshot</SectionEyebrow>
          <h3 className="mt-1 text-base font-semibold text-[var(--app-text)]">{revisionLabel(revision)}</h3>
          <p className="mt-1 text-sm text-[var(--app-text-muted)]">
            {revisionKindLabel(revision)} · {revision.updateSummary || revision.updateKind || 'Plan definition snapshot'}
          </p>
        </div>
        <div className="grid gap-1 text-right text-xs text-[var(--app-text-muted)]">
          {timestamp ? <span>{timestamp}</span> : null}
          {revision.parentRevision > 0 ? <span>Parent revision {revision.parentRevision}</span> : null}
          {revision.restoredFromVersion > 0 ? <span>Restored from revision {revision.restoredFromVersion}</span> : null}
        </div>
      </div>
      <dl className="mt-4 grid gap-2 text-sm sm:grid-cols-2 lg:grid-cols-4">
        <div className="rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] px-3 py-2">
          <dt className="text-[11px] font-semibold uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Status</dt>
          <dd className="mt-1 text-[var(--app-text)]">{displayValue(revision.status)}</dd>
        </div>
        <div className="rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] px-3 py-2">
          <dt className="text-[11px] font-semibold uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Approval</dt>
          <dd className="mt-1 text-[var(--app-text)]">{displayValue(revision.approvalState)}</dd>
        </div>
        <div className="rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] px-3 py-2">
          <dt className="text-[11px] font-semibold uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Scope</dt>
          <dd className="mt-1 text-[var(--app-text)]">{displayValue(revision.updateScope)}</dd>
        </div>
        <div className="rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] px-3 py-2">
          <dt className="text-[11px] font-semibold uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Kind</dt>
          <dd className="mt-1 text-[var(--app-text)]">{displayValue(revision.updateKind)}</dd>
        </div>
      </dl>
    </section>
  )
}

function PlanRecoveryControls({
  document,
  viewingRevision,
  selectedRevision,
  saving,
  executing,
  executionGranularity,
  continueAutomatically,
  onExecutionGranularityChange,
  onContinueAutomaticallyChange,
  onRestore,
}: {
  document: DesktopSessionPlanDocument | null
  viewingRevision: boolean
  selectedRevision: DesktopSessionPlanRevisionRecord | null
  saving: boolean
  executing: boolean
  executionGranularity: 'checkpointed' | 'run_through'
  continueAutomatically: boolean
  onExecutionGranularityChange: (value: 'checkpointed' | 'run_through') => void
  onContinueAutomaticallyChange: (value: boolean) => void
  onRestore: (input: DesktopPlanRecoveryInput) => void
}) {
  const [checkpointId, setCheckpointId] = useState('')
  useEffect(() => {
    setCheckpointId(document?.activeCheckpointId || document?.checkpoints[0]?.id || '')
  }, [document?.id, document?.revisionId, document?.activeCheckpointId])
  if (!viewingRevision || !selectedRevision || !document) return null
  const selectedCheckpoint = document.checkpoints.find((checkpoint) => checkpoint.id === checkpointId) ?? document.checkpoints[0]
  const effectiveCheckpointId = selectedCheckpoint?.id || ''
  const singleRunDisabled = disablesSingleRunApproval(document.executionPolicy)
  const effectiveExecutionGranularity = singleRunDisabled && executionGranularity === 'run_through' ? 'checkpointed' : executionGranularity
  const effectiveContinueAutomatically = effectiveExecutionGranularity === 'run_through' ? true : continueAutomatically
  const continuationPolicy: 'automatic' | 'review_each_checkpoint' = effectiveContinueAutomatically ? 'automatic' : 'review_each_checkpoint'
  const disabled = saving || executing
  return (
    <section className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-bg-alt)] p-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <SectionEyebrow>Recovery controls</SectionEyebrow>
          <p className="mt-1 text-sm text-[var(--app-text-muted)]">
            Restore this full snapshot, restart from a checkpoint, or jump to a later checkpoint with prior steps recorded as skipped.
          </p>
        </div>
      </div>
      <div className="mt-4 grid gap-3 lg:grid-cols-[minmax(0,1.2fr)_minmax(0,0.8fr)]">
        <label className="grid gap-1.5 text-sm text-[var(--app-text)]">
          <span className="font-medium">Checkpoint</span>
          <Select value={effectiveCheckpointId} onChange={(event) => setCheckpointId(event.target.value)} disabled={disabled || document.checkpoints.length === 0}>
            {document.checkpoints.map((checkpoint, index) => (
              <option key={checkpoint.id || `${index}:${checkpoint.title}`} value={checkpoint.id}>
                {index + 1}. {checkpoint.title || checkpoint.id || 'Untitled checkpoint'} · {formatStatusLabel(checkpoint.status, checkpoint.id === document.activeCheckpointId)}
              </option>
            ))}
          </Select>
          <span className="text-xs text-[var(--app-text-muted)]">Restart uses this checkpoint. Jump marks earlier incomplete checkpoints skipped by the backend restore lifecycle.</span>
        </label>
        <label className="grid gap-1.5 text-sm text-[var(--app-text)]">
          <span className="font-medium">Execution mode</span>
          <Select value={effectiveExecutionGranularity} onChange={(event) => onExecutionGranularityChange(event.target.value === 'run_through' ? 'run_through' : 'checkpointed')} disabled={disabled}>
            <option value="checkpointed">{displayExecutionShape('checkpointed')}</option>
            <option value="run_through" disabled={singleRunDisabled}>{displayExecutionShape('run_through')}</option>
          </Select>
          <span className="text-xs text-[var(--app-text-muted)]">Stored with the restored revision so recovery starts with the chosen backend policy. Single run is disabled for automatic checkpoint plans.</span>
        </label>
        {effectiveExecutionGranularity === 'checkpointed' ? (
          <label className="flex items-start gap-2 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] px-3 py-2 text-sm text-[var(--app-text)] lg:col-span-2">
            <input type="checkbox" className="mt-1" checked={continueAutomatically} onChange={(event) => onContinueAutomaticallyChange(event.target.checked)} disabled={disabled} />
            <span className="grid gap-1">
              <span className="font-medium">{displayExecutionMode(continuationPolicy)}</span>
              <span className="text-xs text-[var(--app-text-muted)]">Automatic continues after successful checkpoints; review mode pauses after each checkpoint.</span>
            </span>
          </label>
        ) : null}
      </div>
      <div className="mt-4 flex flex-wrap gap-2">
        <Button type="button" variant="secondary" size="sm" onClick={() => onRestore({})} disabled={disabled}>
          <RotateCcw className={cn('size-4', saving ? 'animate-pulse' : '')} />
          Restore snapshot
        </Button>
        <Button
          type="button"
          variant="primary"
          size="sm"
          onClick={() => onRestore({ checkpointId: effectiveCheckpointId, executionGranularity: effectiveExecutionGranularity, continuationPolicy, continueAutomatically: effectiveContinueAutomatically, restart: true, start: true })}
          disabled={disabled || !effectiveCheckpointId}
        >
          <PlayCircle className={cn('size-4', saving || executing ? 'animate-pulse' : '')} />
          Restore & start checkpoint
        </Button>
        <Button
          type="button"
          variant="outline"
          size="sm"
          onClick={() => onRestore({ checkpointId: effectiveCheckpointId, executionGranularity: effectiveExecutionGranularity, continuationPolicy, continueAutomatically: effectiveContinueAutomatically, restart: true, start: true, skipPrior: true })}
          disabled={disabled || !effectiveCheckpointId}
        >
          Jump to selected checkpoint
        </Button>
        {document.checkpoints.length > 0 ? (
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={() => onRestore({ checkpointId: document.checkpoints[document.checkpoints.length - 1]?.id, executionGranularity: effectiveExecutionGranularity, continuationPolicy, continueAutomatically: effectiveContinueAutomatically, restart: true, start: true, skipPrior: true })}
            disabled={disabled || !document.checkpoints[document.checkpoints.length - 1]?.id}
          >
            Jump to final checkpoint
          </Button>
        ) : null}
      </div>
    </section>
  )
}

export function DesktopPlanModal({
  open,
  plan,
  revisions,
  historyLoading,
  saving,
  executing = false,
  error,
  onOpenChange,
  onCopy,
  onRestoreRevision,
  onApproveStart,
}: DesktopPlanModalProps) {
  const [copyState, setCopyState] = useState<'idle' | 'copied' | 'error'>('idle')
  const [selectedRevisionKey, setSelectedRevisionKey] = useState('current')
  const [executionGranularity, setExecutionGranularity] = useState<'checkpointed' | 'run_through'>('checkpointed')
  const [continueAutomatically, setContinueAutomatically] = useState(true)
  const modalWasOpenRef = useRef(false)
  const syncedPlanIdRef = useRef<string | null>(null)

  useEffect(() => {
    if (!open) {
      modalWasOpenRef.current = false
      syncedPlanIdRef.current = null
      return
    }

    const planId = plan?.id ?? ''
    const opening = !modalWasOpenRef.current
    const planChanged = syncedPlanIdRef.current !== null && syncedPlanIdRef.current !== planId
    modalWasOpenRef.current = true
    syncedPlanIdRef.current = planId

    if (opening || planChanged) {
      setCopyState('idle')
      setSelectedRevisionKey('current')
      const executionSelection = planExecutionSelectionFromPolicy(plan?.document?.executionPolicy)
      setExecutionGranularity(executionSelection.executionGranularity)
      setContinueAutomatically(executionSelection.continueAutomatically)
    }
  }, [open, plan?.id, plan?.document])

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

  if (!open) {
    return null
  }

  const viewingRevision = selectedRevision !== null
  const selectedDocument = viewingRevision ? selectedRevision.document : (plan?.document ?? null)
  const preview = viewingRevision ? selectedRevision.plan : (plan?.plan ?? '')

  const handleCopy = async () => {
    const ok = await onCopy(selectedPlanCopyText(selectedDocument, preview))
    setCopyState(ok ? 'copied' : 'error')
  }

  const handleRestoreRevision = async (input: DesktopPlanRecoveryInput = {}) => {
    if (!selectedRevision) {
      return
    }
    await onRestoreRevision(selectedRevision, input)
    setSelectedRevisionKey('current')
  }

  const singleRunApprovalDisabled = disablesSingleRunApproval(selectedDocument?.executionPolicy)
  const approvalChoice = approvalChoiceFromSelection(
    singleRunApprovalDisabled && executionGranularity === 'run_through' ? 'checkpointed' : executionGranularity,
    continueAutomatically,
  )
  const approvalSelection = approvalSelectionFromChoice(approvalChoice)
  const effectiveContinueAutomatically = approvalSelection.executionGranularity === 'run_through' ? true : approvalSelection.continueAutomatically
  const effectiveContinuationPolicy: 'automatic' | 'review_each_checkpoint' = effectiveContinueAutomatically ? 'automatic' : 'review_each_checkpoint'
  const canApproveStart = Boolean(onApproveStart && plan?.document && !viewingRevision)

  const handleApproveStart = async () => {
    if (!onApproveStart || viewingRevision) return
    await onApproveStart({ executionGranularity: approvalSelection.executionGranularity, continueAutomatically: effectiveContinueAutomatically, continuationPolicy: effectiveContinuationPolicy })
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
            {canApproveStart ? (
              <Button type="button" variant="primary" size="sm" onClick={() => void handleApproveStart()} disabled={saving || executing}>
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
            <ModalCloseButton onClick={() => onOpenChange(false)} aria-label="Close current plan dialog" />
          </div>
        </div>

        <div className="min-h-0 overflow-y-auto px-6 py-5">
          <div className="grid gap-4">
              <PlanRevisionHistory
                revisions={revisions}
                selectedRevisionKey={selectedRevisionKey}
                historyLoading={historyLoading}
                onSelect={setSelectedRevisionKey}
              />
              {viewingRevision && selectedRevision ? <PlanRevisionSummary revision={selectedRevision} /> : null}
              <PlanRecoveryControls
                document={selectedDocument}
                viewingRevision={viewingRevision}
                selectedRevision={selectedRevision}
                saving={saving}
                executing={executing}
                executionGranularity={executionGranularity}
                continueAutomatically={continueAutomatically}
                onExecutionGranularityChange={setExecutionGranularity}
                onContinueAutomaticallyChange={setContinueAutomatically}
                onRestore={(input) => void handleRestoreRevision(input)}
              />
              {!viewingRevision && selectedDocument ? (
                <section className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-bg-alt)] p-4">
                  <SectionEyebrow>Execution on approval</SectionEyebrow>
                  <p className="mt-1 text-sm text-[var(--app-text-muted)]">
                    Choose how approval starts execution. Single run is disabled for automatic checkpoint plans because they must preserve checkpoint boundaries.
                  </p>
                  <div className="mt-3 grid gap-3 md:grid-cols-3" role="radiogroup" aria-label="Execution on approval">
                    {planApprovalExecutionChoices.map((choice) => {
                      const selected = approvalChoice === choice.id
                      const choiceDisabled = executing || (choice.id === 'run_through' && singleRunApprovalDisabled)
                      return (
                        <button
                          key={choice.id}
                          type="button"
                          role="radio"
                          aria-checked={selected}
                          disabled={choiceDisabled}
                          onClick={() => {
                            if (choiceDisabled) return
                            const selection = approvalSelectionFromChoice(choice.id)
                            setExecutionGranularity(selection.executionGranularity)
                            setContinueAutomatically(selection.continueAutomatically)
                          }}
                          className={cn(
                            'grid min-h-[132px] gap-2 rounded-2xl border px-4 py-3 text-left transition focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--app-focus-ring)]',
                            selected
                              ? 'border-[var(--app-primary-border)] bg-[var(--app-primary-soft)] shadow-[0_12px_28px_rgba(0,0,0,0.12)]'
                              : 'border-[var(--app-border)] bg-[var(--app-surface)] hover:border-[var(--app-border-active)]',
                            choiceDisabled ? 'cursor-not-allowed opacity-50 hover:border-[var(--app-border)]' : 'cursor-pointer',
                          )}
                        >
                          <span className="flex items-start justify-between gap-3">
                            <span className="text-sm font-semibold text-[var(--app-text)]">{choice.title}</span>
                            <span
                              className={cn(
                                'mt-0.5 inline-flex size-4 shrink-0 items-center justify-center rounded-full border',
                                selected ? 'border-[var(--app-primary)] bg-[var(--app-primary)] text-[var(--app-primary-contrast)]' : 'border-[var(--app-border-strong)]',
                              )}
                              aria-hidden="true"
                            >
                              {selected ? <Check className="size-3" /> : null}
                            </span>
                          </span>
                          <span className="text-xs leading-5 text-[var(--app-text)]">{choice.description}</span>
                          <span className="text-[11px] leading-4 text-[var(--app-text-muted)]">{choice.detail}</span>
                        </button>
                      )
                    })}
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
                      No active plan is available for this session.
                    </p>
                  )}
                </section>
              )}
            </div>

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
