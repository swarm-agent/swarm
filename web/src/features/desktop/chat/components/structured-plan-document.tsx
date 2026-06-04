import type { ReactNode } from 'react'
import { cn } from '../../../../lib/cn'
import {
  Target,
  Compass,
  Brain,
  ShieldCheck,
  FileText,
  CheckCircle2,
  Circle,
  PlayCircle,
  ListTodo,
  type LucideIcon
} from 'lucide-react'

export interface StructuredPlanInfo {
  goal: string
  scope: string
  context: string
  decisions: string[]
  constraints: string[]
  assumptions: string[]
  openQuestions: string[]
  relevantFiles: string[]
  successCriteria: string[]
  validationStrategy: string
}

export interface StructuredPlanCheckpoint {
  id: string
  title: string
  status: string
  objective: string
  tasks: string[]
  acceptanceCriteria: string[]
  notes: string
  report: string
  result: string
  changedFiles: string[]
  validation: string[]
  order: number
}

export interface StructuredPlanDocument {
  id: string
  title: string
  status: string
  schemaVersion: string
  revisionId: string
  info: StructuredPlanInfo
  checkpoints: StructuredPlanCheckpoint[]
  activeCheckpointId: string
  renderedText: string
  displayText: string
}

function objectValue(value: unknown): Record<string, unknown> | null {
  return value && typeof value === 'object' && !Array.isArray(value) ? (value as Record<string, unknown>) : null
}

function stringValue(record: Record<string, unknown>, ...keys: string[]): string {
  for (const key of keys) {
    const value = record[key]
    if (typeof value === 'string') {
      const trimmed = value.trim()
      if (trimmed) {
        return trimmed
      }
    }
  }
  return ''
}

function rawStringValue(record: Record<string, unknown>, ...keys: string[]): string {
  for (const key of keys) {
    const value = record[key]
    if (typeof value === 'string') {
      return value
    }
  }
  return ''
}

function stringArrayValue(record: Record<string, unknown>, ...keys: string[]): string[] {
  for (const key of keys) {
    const value = record[key]
    if (Array.isArray(value)) {
      return value.map((entry) => (typeof entry === 'string' ? entry.trim() : '')).filter(Boolean)
    }
    if (typeof value === 'string') {
      const trimmed = value.trim()
      if (trimmed) {
        return [trimmed]
      }
    }
  }
  return []
}

function numberValue(record: Record<string, unknown>, ...keys: string[]): number {
  for (const key of keys) {
    const value = record[key]
    if (typeof value === 'number' && Number.isFinite(value)) {
      return value
    }
  }
  return 0
}

export function normalizeStructuredPlanDocument(value: unknown): StructuredPlanDocument | null {
  const record = objectValue(value)
  if (!record) {
    return null
  }
  const infoRecord = objectValue(record.info) ?? {}
  const checkpointsValue = Array.isArray(record.checkpoints) ? record.checkpoints : []
  const checkpoints = checkpointsValue
    .map((entry, index): StructuredPlanCheckpoint | null => {
      const checkpoint = objectValue(entry)
      if (!checkpoint) {
        return null
      }
      return {
        id: stringValue(checkpoint, 'id'),
        title: stringValue(checkpoint, 'title'),
        status: stringValue(checkpoint, 'status'),
        objective: stringValue(checkpoint, 'objective'),
        tasks: stringArrayValue(checkpoint, 'tasks'),
        acceptanceCriteria: stringArrayValue(checkpoint, 'acceptanceCriteria', 'acceptance_criteria'),
        notes: stringValue(checkpoint, 'notes'),
        report: stringValue(checkpoint, 'report'),
        result: stringValue(checkpoint, 'result'),
        changedFiles: stringArrayValue(checkpoint, 'changedFiles', 'changed_files'),
        validation: stringArrayValue(checkpoint, 'validation'),
        order: numberValue(checkpoint, 'order') || index + 1,
      }
    })
    .filter((entry): entry is StructuredPlanCheckpoint => entry !== null)
    .sort((left, right) => left.order - right.order)

  const document: StructuredPlanDocument = {
    id: stringValue(record, 'id'),
    title: stringValue(record, 'title'),
    status: stringValue(record, 'status'),
    schemaVersion: stringValue(record, 'schemaVersion', 'schema_version'),
    revisionId: stringValue(record, 'revisionId', 'revision_id'),
    info: {
      goal: stringValue(infoRecord, 'goal'),
      scope: stringValue(infoRecord, 'scope', 'context'),
      context: stringValue(infoRecord, 'context'),
      decisions: stringArrayValue(infoRecord, 'decisions'),
      constraints: stringArrayValue(infoRecord, 'constraints'),
      assumptions: stringArrayValue(infoRecord, 'assumptions'),
      openQuestions: stringArrayValue(infoRecord, 'openQuestions', 'open_questions'),
      relevantFiles: stringArrayValue(infoRecord, 'relevantFiles', 'relevant_files', 'files'),
      successCriteria: stringArrayValue(infoRecord, 'successCriteria', 'success_criteria'),
      validationStrategy: stringValue(infoRecord, 'validationStrategy', 'validation_strategy', 'validation'),
    },
    checkpoints,
    activeCheckpointId: stringValue(record, 'activeCheckpointId', 'active_checkpoint_id'),
    renderedText: rawStringValue(record, 'renderedText', 'rendered_text'),
    displayText: rawStringValue(record, 'displayText', 'display_text'),
  }

  if (!document.id && !document.title && !document.info.goal && document.checkpoints.length === 0) {
    return null
  }
  return document
}

export function structuredPlanInfoToWire(info: StructuredPlanInfo): Record<string, unknown> {
  return {
    goal: info.goal,
    scope: info.scope,
    context: info.context,
    decisions: info.decisions,
    constraints: info.constraints,
    assumptions: info.assumptions,
    open_questions: info.openQuestions,
    relevant_files: info.relevantFiles,
    success_criteria: info.successCriteria,
    validation_strategy: info.validationStrategy,
  }
}

export function structuredPlanCheckpointToWire(checkpoint: StructuredPlanCheckpoint): Record<string, unknown> {
  return {
    id: checkpoint.id,
    title: checkpoint.title,
    status: checkpoint.status,
    objective: checkpoint.objective,
    tasks: checkpoint.tasks,
    acceptance_criteria: checkpoint.acceptanceCriteria,
    notes: checkpoint.notes,
    report: checkpoint.report,
    result: checkpoint.result,
    changed_files: checkpoint.changedFiles,
    validation: checkpoint.validation,
    order: checkpoint.order,
  }
}

export function structuredPlanDocumentToWire(document: StructuredPlanDocument): Record<string, unknown> {
  return {
    id: document.id,
    title: document.title,
    status: document.status,
    schema_version: document.schemaVersion,
    revision_id: document.revisionId,
    info: structuredPlanInfoToWire(document.info),
    checkpoints: document.checkpoints.map((checkpoint) => structuredPlanCheckpointToWire(checkpoint)),
    active_checkpoint_id: document.activeCheckpointId,
    rendered_text: document.renderedText,
    display_text: document.displayText,
  }
}

function InfoCard({ title, icon: Icon, children }: { title: string; icon: LucideIcon; children: ReactNode }) {
  return (
    <section className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-bg-alt)] p-4 sm:p-5">
      <div className="mb-3 flex items-center gap-2 border-b border-[var(--app-border)] pb-3">
        <Icon className="size-4 shrink-0 text-[var(--app-primary)]" />
        <h4 className="text-xs font-semibold uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">{title}</h4>
      </div>
      <div className="min-w-0">{children}</div>
    </section>
  )
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

function PlanDetailObjects({ document }: { document: StructuredPlanDocument }) {
  const validationFiles = document.info.validationStrategy.trim() !== '' || document.info.relevantFiles.length > 0
  return (
    <div className="grid gap-4">
      {document.info.goal ? (
        <InfoCard title="Goal" icon={Target}>
          <TextBlock value={document.info.goal} />
        </InfoCard>
      ) : null}

      {document.info.scope ? (
        <InfoCard title="Scope" icon={Compass}>
          <TextBlock value={document.info.scope} />
        </InfoCard>
      ) : null}

      {document.info.decisions.length > 0 ? (
        <InfoCard title="Decisions" icon={Brain}>
          <BulletList values={document.info.decisions} />
        </InfoCard>
      ) : null}

      {validationFiles ? (
        <InfoCard title="Validation & files" icon={ShieldCheck}>
          <div className="grid gap-4">
            <TextBlock value={document.info.validationStrategy} />
            {document.info.relevantFiles.length > 0 ? (
              <div className="grid gap-2 border-t border-[var(--app-border)] pt-3">
                <div className="flex items-center gap-1.5 text-xs font-semibold uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">
                  <FileText className="size-3.5 shrink-0 text-[var(--app-primary)]" />
                  Relevant files
                </div>
                <BulletList values={document.info.relevantFiles} mono />
              </div>
            ) : null}
          </div>
        </InfoCard>
      ) : null}

      {document.info.successCriteria.length > 0 ? (
        <InfoCard title="Success criteria" icon={CheckCircle2}>
          <BulletList values={document.info.successCriteria} />
        </InfoCard>
      ) : null}

      {document.info.constraints.length > 0 ? (
        <InfoCard title="Constraints" icon={ShieldCheck}>
          <BulletList values={document.info.constraints} />
        </InfoCard>
      ) : null}

      {document.info.assumptions.length > 0 ? (
        <InfoCard title="Assumptions" icon={Compass}>
          <BulletList values={document.info.assumptions} />
        </InfoCard>
      ) : null}

      {document.info.openQuestions.length > 0 ? (
        <InfoCard title="Open questions" icon={ListTodo}>
          <BulletList values={document.info.openQuestions} />
        </InfoCard>
      ) : null}
    </div>
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

function StatusBadge({ status, active }: { status: string; active: boolean }) {
  if (!status.trim() && !active) {
    return null
  }
  const normStatus = status.toLowerCase()
  const done = normStatus === 'done' || normStatus === 'completed' || normStatus === 'success'
  return (
    <span
      className={cn(
        'rounded-full border px-2 py-0.5 text-[10px] font-medium uppercase tracking-[0.08em]',
        done
          ? 'border-[var(--app-success-border)] bg-[var(--app-success-bg)] text-[var(--app-success)]'
          : active
            ? 'border-[var(--app-primary-border)] bg-[var(--app-primary-soft)] text-[var(--app-primary)]'
            : 'border-[var(--app-border)] bg-[var(--app-surface)] text-[var(--app-text-muted)]',
      )}
    >
      {active ? 'active' : status}
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

function CheckpointItem({ checkpoint, active }: { checkpoint: StructuredPlanCheckpoint; active: boolean }) {
  return (
    <article
      className={cn(
        'rounded-2xl border bg-[var(--app-bg-alt)] p-4 sm:p-5',
        active ? 'border-[var(--app-primary)] shadow-sm shadow-[var(--app-primary-soft)]' : 'border-[var(--app-border)]',
      )}
    >
      <div className="flex min-w-0 items-start gap-3">
        <div className="mt-0.5 flex size-8 shrink-0 items-center justify-center rounded-full border border-[var(--app-border)] bg-[var(--app-surface)]">
          <CheckpointStatusIcon status={checkpoint.status} active={active} />
        </div>
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2">
            <span className="rounded-full border border-[var(--app-border)] bg-[var(--app-surface)] px-2 py-0.5 text-[10px] font-semibold uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">
              #{checkpoint.order}
            </span>
            <StatusBadge status={checkpoint.status} active={active} />
            {checkpoint.id ? (
              <code className="rounded bg-[var(--app-surface-subtle)] px-1.5 py-0.5 font-mono text-[10px] text-[var(--app-text-muted)]">
                {checkpoint.id}
              </code>
            ) : null}
          </div>
          <h4 className={cn('mt-2 text-base font-semibold leading-snug', active ? 'text-[var(--app-primary)]' : 'text-[var(--app-text)]')}>
            {checkpoint.title || checkpoint.id || 'Untitled checkpoint'}
          </h4>
          {checkpoint.objective ? (
            <p className="mt-1 whitespace-pre-wrap break-words text-sm leading-6 text-[var(--app-text-muted)]">{checkpoint.objective}</p>
          ) : null}
        </div>
      </div>

      <div className="mt-4 grid gap-3 border-t border-[var(--app-border)] pt-4">
        <CheckpointSection title="Tasks" values={checkpoint.tasks} />
        <CheckpointSection title="Acceptance" values={checkpoint.acceptanceCriteria} />
        <CheckpointTextSection title="Notes" value={checkpoint.notes} />
        <CheckpointTextSection title="Report" value={checkpoint.report} />
        <CheckpointTextSection title="Result" value={checkpoint.result} />
        <CheckpointSection title="Changed files" values={checkpoint.changedFiles} mono />
        <CheckpointSection title="Validation" values={checkpoint.validation} />
      </div>
    </article>
  )
}

function CheckpointsList({ document, activeID }: { document: StructuredPlanDocument; activeID: string }) {
  if (document.checkpoints.length === 0) {
    return (
      <div className="rounded-2xl border border-dashed border-[var(--app-border)] px-4 py-5 text-sm text-[var(--app-text-muted)]">
        No checkpoints are defined.
      </div>
    )
  }
  return (
    <div className="grid gap-3">
      {document.checkpoints.map((checkpoint) => {
        const active = activeID !== '' && checkpoint.id === activeID
        return (
          <CheckpointItem
            key={checkpoint.id || `${checkpoint.order}:${checkpoint.title}`}
            checkpoint={checkpoint}
            active={active}
          />
        )
      })}
    </div>
  )
}

export function StructuredPlanDocumentView({
  document,
  emptyText = 'No structured plan document was provided.',
  className,
  compact = false,
}: {
  document: StructuredPlanDocument | null
  emptyText?: string
  className?: string
  compact?: boolean
}) {
  if (!document) {
    return (
      <section className={cn('rounded-2xl border border-dashed border-[var(--app-border)] bg-[var(--app-bg-alt)] px-4 py-5 text-sm text-[var(--app-text-muted)]', className)}>
        {emptyText}
      </section>
    )
  }

  const activeID = document.activeCheckpointId.trim()

  if (compact) {
    return (
      <div className={cn('grid gap-5', className)}>
        <PlanDetailObjects document={document} />
        <section className="grid gap-3 border-t border-[var(--app-border)] pt-5">
          <div className="flex items-center justify-between gap-3">
            <div className="flex items-center gap-2">
              <ListTodo className="size-4 text-[var(--app-primary)]" />
              <h3 className="text-base font-semibold text-[var(--app-text)]">Checkpoints</h3>
            </div>
            <span className="rounded-full border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-2.5 py-0.5 text-xs text-[var(--app-text-muted)]">
              {document.checkpoints.length}
            </span>
          </div>
          <CheckpointsList document={document} activeID={activeID} />
        </section>
      </div>
    )
  }

  return (
    <div className={cn('grid min-h-0 grid-cols-1 gap-6 lg:grid-cols-2', className)}>
      <section className="grid min-w-0 content-start gap-4">
        <div className="flex min-w-0 items-start justify-between gap-3 border-b border-[var(--app-border)] pb-3">
          <div className="min-w-0">
            <div className="text-xs font-semibold uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Plan details</div>
            <h3 className="mt-1 truncate text-lg font-semibold text-[var(--app-text)]">
              {document.title || document.info.goal || 'Structured execution blueprint'}
            </h3>
          </div>
          <div className="flex shrink-0 flex-wrap justify-end gap-2">
            {document.status ? (
              <span className="rounded-full border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-2.5 py-1 text-xs text-[var(--app-text-muted)]">
                {document.status}
              </span>
            ) : null}
            {activeID ? (
              <span className="rounded-full border border-[var(--app-primary-border)] bg-[var(--app-primary-soft)] px-2.5 py-1 text-xs font-medium text-[var(--app-primary)]">
                Active {activeID}
              </span>
            ) : null}
          </div>
        </div>
        <PlanDetailObjects document={document} />
      </section>

      <section className="grid min-w-0 content-start gap-4 lg:border-l lg:border-[var(--app-border)] lg:pl-6">
        <div className="flex items-start justify-between gap-3 border-b border-[var(--app-border)] pb-3">
          <div className="flex min-w-0 items-center gap-2">
            <ListTodo className="size-5 shrink-0 text-[var(--app-primary)]" />
            <div className="min-w-0">
              <div className="text-xs font-semibold uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Checkpoint objects</div>
              <h3 className="mt-1 text-lg font-semibold text-[var(--app-text)]">Flat execution list</h3>
            </div>
          </div>
          <span className="shrink-0 rounded-full border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-2.5 py-1 text-xs text-[var(--app-text-muted)]">
            {document.checkpoints.length} step{document.checkpoints.length === 1 ? '' : 's'}
          </span>
        </div>
        <CheckpointsList document={document} activeID={activeID} />
      </section>
    </div>
  )
}
