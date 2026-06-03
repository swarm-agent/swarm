import { cn } from '../../../../lib/cn'

export interface StructuredPlanInfo {
  goal: string
  context: string
  decisions: string[]
  constraints: string[]
  assumptions: string[]
  openQuestions: string[]
  relevantFiles: string[]
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
      context: stringValue(infoRecord, 'context'),
      decisions: stringArrayValue(infoRecord, 'decisions'),
      constraints: stringArrayValue(infoRecord, 'constraints'),
      assumptions: stringArrayValue(infoRecord, 'assumptions'),
      openQuestions: stringArrayValue(infoRecord, 'openQuestions', 'open_questions'),
      relevantFiles: stringArrayValue(infoRecord, 'relevantFiles', 'relevant_files'),
      validationStrategy: stringValue(infoRecord, 'validationStrategy', 'validation_strategy'),
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
    context: info.context,
    decisions: info.decisions,
    constraints: info.constraints,
    assumptions: info.assumptions,
    open_questions: info.openQuestions,
    relevant_files: info.relevantFiles,
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

function FieldList({ title, values }: { title: string; values: string[] }) {
  if (values.length === 0) {
    return null
  }
  return (
    <div className="grid gap-1.5">
      <div className="text-xs font-semibold uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">{title}</div>
      <ul className="grid gap-1 text-sm text-[var(--app-text)]">
        {values.map((value, index) => (
          <li key={`${title}:${index}:${value}`} className="flex gap-2">
            <span className="mt-2 size-1.5 shrink-0 rounded-full bg-[var(--app-primary)]" />
            <span className="min-w-0 whitespace-pre-wrap break-words">{value}</span>
          </li>
        ))}
      </ul>
    </div>
  )
}

function TextField({ title, value }: { title: string; value: string }) {
  if (!value.trim()) {
    return null
  }
  return (
    <div className="grid gap-1.5">
      <div className="text-xs font-semibold uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">{title}</div>
      <p className="whitespace-pre-wrap break-words text-sm leading-6 text-[var(--app-text)]">{value}</p>
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
  return (
    <div className={cn('grid gap-4', className)}>
      <section className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-bg-alt)] p-4 sm:p-5">
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div className="min-w-0">
            <div className="text-xs font-semibold uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Structured plan</div>
            <h3 className="mt-1 text-lg font-semibold text-[var(--app-text)]">{document.title || document.info.goal || 'Untitled plan'}</h3>
            <div className="mt-1 flex flex-wrap gap-x-3 gap-y-1 text-xs text-[var(--app-text-muted)]">
              {document.id ? <span>ID {document.id}</span> : null}
              {document.status ? <span>Status {document.status}</span> : null}
              {document.revisionId ? <span>Revision {document.revisionId}</span> : null}
            </div>
          </div>
          {activeID ? (
            <span className="rounded-full border border-[var(--app-primary-border)] bg-[var(--app-primary-soft)] px-2.5 py-1 text-xs font-medium text-[var(--app-primary)]">
              Active checkpoint {activeID}
            </span>
          ) : null}
        </div>
        <div className={cn('mt-4 grid gap-4', compact ? 'md:grid-cols-1' : 'md:grid-cols-2')}>
          <TextField title="Goal" value={document.info.goal} />
          <TextField title="Context" value={document.info.context} />
          <FieldList title="Decisions" values={document.info.decisions} />
          <FieldList title="Constraints" values={document.info.constraints} />
          <FieldList title="Assumptions" values={document.info.assumptions} />
          <FieldList title="Open questions" values={document.info.openQuestions} />
          <FieldList title="Relevant files" values={document.info.relevantFiles} />
          <TextField title="Validation strategy" value={document.info.validationStrategy} />
        </div>
      </section>

      <section className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4 sm:p-5">
        <div className="mb-3 flex flex-wrap items-center justify-between gap-2">
          <div>
            <div className="text-xs font-semibold uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Checkpoints</div>
            <div className="mt-1 text-sm text-[var(--app-text-muted)]">{document.checkpoints.length} structured execution object{document.checkpoints.length === 1 ? '' : 's'}</div>
          </div>
        </div>
        {document.checkpoints.length > 0 ? (
          <div className="grid gap-3">
            {document.checkpoints.map((checkpoint) => {
              const active = activeID !== '' && checkpoint.id === activeID
              return (
                <article key={checkpoint.id || `${checkpoint.order}:${checkpoint.title}`} className={cn('rounded-xl border bg-[var(--app-bg-alt)] p-4', active ? 'border-[var(--app-primary)]' : 'border-[var(--app-border)]')}>
                  <div className="flex flex-wrap items-start justify-between gap-2">
                    <div className="min-w-0">
                      <div className="flex flex-wrap items-center gap-2">
                        <span className="text-xs font-semibold uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">#{checkpoint.order}</span>
                        {checkpoint.status ? <span className="rounded-full border border-[var(--app-border)] px-2 py-0.5 text-[11px] text-[var(--app-text-muted)]">{checkpoint.status}</span> : null}
                        {active ? <span className="rounded-full border border-[var(--app-primary-border)] bg-[var(--app-primary-soft)] px-2 py-0.5 text-[11px] text-[var(--app-primary)]">active</span> : null}
                      </div>
                      <h4 className="mt-1 text-base font-semibold text-[var(--app-text)]">{checkpoint.title || checkpoint.id || 'Untitled checkpoint'}</h4>
                      {checkpoint.objective ? <p className="mt-1 whitespace-pre-wrap break-words text-sm leading-6 text-[var(--app-text-muted)]">{checkpoint.objective}</p> : null}
                    </div>
                    {checkpoint.id ? <code className="rounded bg-[var(--app-surface)] px-2 py-1 text-[11px] text-[var(--app-text-muted)]">{checkpoint.id}</code> : null}
                  </div>
                  <div className="mt-3 grid gap-3 md:grid-cols-2">
                    <FieldList title="Tasks" values={checkpoint.tasks} />
                    <FieldList title="Acceptance" values={checkpoint.acceptanceCriteria} />
                    <TextField title="Notes" value={checkpoint.notes} />
                    <TextField title="Report" value={checkpoint.report} />
                    <TextField title="Result" value={checkpoint.result} />
                    <FieldList title="Changed files" values={checkpoint.changedFiles} />
                    <FieldList title="Validation" values={checkpoint.validation} />
                  </div>
                </article>
              )
            })}
          </div>
        ) : (
          <div className="rounded-xl border border-dashed border-[var(--app-border)] px-3 py-4 text-sm text-[var(--app-text-muted)]">No checkpoints are defined.</div>
        )}
      </section>
    </div>
  )
}
