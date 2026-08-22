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
  ChevronRight,
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

export interface StructuredPlanExecutionPolicy {
  mode: string
  shape: string
  followupCheckpointPolicy: string
}

export interface StructuredPlanExecutionState {
  status: string
  activeAttemptId: string
  parentSessionId: string
  currentSessionId: string
  currentRunId: string
  lastCheckpointId: string
  lastAttemptId: string
  lastOutcome: string
  startedAt: number
  updatedAt: number
  completedAt: number
}

export interface StructuredPlanCheckpointReview {
  status: string
  reviewerId: string
  reviewerType: string
  result: string
  notes: string
  reviewedAt: number
}

export interface StructuredPlanCheckpointAttempt {
  id: string
  checkpointId: string
  status: string
  outcome: string
  runId: string
  sessionId: string
  parentSessionId: string
  startedAt: number
  completedAt: number
  report: string
  result: string
  changedFiles: string[]
  validation: string[]
}

export interface StructuredPlanTaskProgram {
  id: string
  maxConcurrency: number
  stages: Array<{ id: string; dependsOn: string[]; dependencyEvidence: string }>
  jobs: Array<{
    id: string
    stageId: string
    dependsOn: string[]
    agentType: string
    title: string
    metaPrompt: string
    deliverable: string
    ownedScope: string[]
    outputMode: string
    outputRequirements: Record<string, unknown> | null
    acceptanceCriteria: string[]
    dependencyEvidence: string
  }>
}

export interface StructuredPlanCheckpoint {
  id: string
  title: string
  status: string
  objective: string
  tasks: string[]
  acceptanceCriteria: string[]
  taskProgram: StructuredPlanTaskProgram | null
  notes: string
  report: string
  result: string
  changedFiles: string[]
  validation: string[]
  attemptId: string
  runId: string
  sessionId: string
  startedAt: number
  completedAt: number
  review: StructuredPlanCheckpointReview | null
  attempts: StructuredPlanCheckpointAttempt[]
  order: number
}

export interface StructuredPlanDocument {
  id: string
  title: string
  status: string
  schemaVersion: string
  revisionId: string
  info: StructuredPlanInfo
  executionPolicy: StructuredPlanExecutionPolicy | null
  executionState: StructuredPlanExecutionState | null
  checkpoints: StructuredPlanCheckpoint[]
  activeCheckpointId: string
  renderedText: string
  displayText: string
}

export interface StructuredPlanReviewCheckpoint {
  id: string
  order: number
  title: string
  objective: string
  tasks: string[]
  acceptanceCriteria: string[]
}

export interface StructuredPlanReviewProjection {
  title: string
  objective: string
  checkpoints: StructuredPlanReviewCheckpoint[]
  /** The unabridged normalized document remains the authority for expansion and agent context. */
  authoritativeDocument: StructuredPlanDocument
}

export function structuredPlanReviewProjection(document: StructuredPlanDocument): StructuredPlanReviewProjection {
  return {
    title: document.title || document.info.goal || 'Plan proposal',
    objective: document.info.goal || document.title,
    checkpoints: document.checkpoints.map((checkpoint) => ({
      id: checkpoint.id,
      order: checkpoint.order,
      title: checkpoint.title || checkpoint.objective || `Checkpoint ${checkpoint.order}`,
      objective: checkpoint.objective,
      tasks: checkpoint.tasks,
      acceptanceCriteria: checkpoint.acceptanceCriteria,
    })),
    authoritativeDocument: document,
  }
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

function normalizeStructuredPlanTaskProgram(value: unknown): StructuredPlanTaskProgram | null {
  const record = objectValue(value)
  if (!record) return null
  const stages = (Array.isArray(record.stages) ? record.stages : []).map((value) => objectValue(value)).filter((value): value is Record<string, unknown> => value !== null).map((stage) => ({
    id: stringValue(stage, 'id'),
    dependsOn: stringArrayValue(stage, 'dependsOn', 'depends_on'),
    dependencyEvidence: stringValue(stage, 'dependencyEvidence', 'dependency_evidence'),
  }))
  const jobs = (Array.isArray(record.jobs) ? record.jobs : []).map((value) => objectValue(value)).filter((value): value is Record<string, unknown> => value !== null).map((job) => ({
    id: stringValue(job, 'id'),
    stageId: stringValue(job, 'stageId', 'stage_id'),
    dependsOn: stringArrayValue(job, 'dependsOn', 'depends_on'),
    agentType: stringValue(job, 'agentType', 'agent_type', 'subagent_type'),
    title: stringValue(job, 'title'),
    metaPrompt: stringValue(job, 'metaPrompt', 'meta_prompt'),
    deliverable: stringValue(job, 'deliverable'),
    ownedScope: stringArrayValue(job, 'ownedScope', 'owned_scope'),
    outputMode: stringValue(job, 'outputMode', 'output_mode'),
    outputRequirements: objectValue(job.outputRequirements ?? job.output_requirements),
    acceptanceCriteria: stringArrayValue(job, 'acceptanceCriteria', 'acceptance_criteria'),
    dependencyEvidence: stringValue(job, 'dependencyEvidence', 'dependency_evidence'),
  }))
  const id = stringValue(record, 'id')
  return id && stages.length > 0 && jobs.length > 0 ? { id, maxConcurrency: numberValue(record, 'maxConcurrency', 'max_concurrency'), stages, jobs } : null
}

function normalizeStructuredPlanExecutionPolicy(value: unknown): StructuredPlanDocument['executionPolicy'] {
  const record = objectValue(value)
  if (!record) return null
  const policy = {
    mode: stringValue(record, 'mode'),
    shape: stringValue(record, 'shape'),
    followupCheckpointPolicy: stringValue(record, 'followupCheckpointPolicy', 'followup_checkpoint_policy'),
  }
  return policy.mode || policy.shape || policy.followupCheckpointPolicy ? policy : null
}

function normalizeStructuredPlanExecutionState(value: unknown): StructuredPlanDocument['executionState'] {
  const record = objectValue(value)
  if (!record) return null
  const state = {
    status: stringValue(record, 'status'),
    activeAttemptId: stringValue(record, 'activeAttemptId', 'active_attempt_id'),
    parentSessionId: stringValue(record, 'parentSessionId', 'parent_session_id'),
    currentSessionId: stringValue(record, 'currentSessionId', 'current_session_id'),
    currentRunId: stringValue(record, 'currentRunId', 'current_run_id'),
    lastCheckpointId: stringValue(record, 'lastCheckpointId', 'last_checkpoint_id'),
    lastAttemptId: stringValue(record, 'lastAttemptId', 'last_attempt_id'),
    lastOutcome: stringValue(record, 'lastOutcome', 'last_outcome'),
    startedAt: numberValue(record, 'startedAt', 'started_at'),
    updatedAt: numberValue(record, 'updatedAt', 'updated_at'),
    completedAt: numberValue(record, 'completedAt', 'completed_at'),
  }
  return state.status || state.activeAttemptId || state.currentRunId || state.lastCheckpointId ? state : null
}

function normalizeStructuredPlanCheckpointReview(value: unknown): StructuredPlanCheckpoint['review'] {
  const record = objectValue(value)
  if (!record) return null
  const review = {
    status: stringValue(record, 'status'),
    reviewerId: stringValue(record, 'reviewerId', 'reviewer_id'),
    reviewerType: stringValue(record, 'reviewerType', 'reviewer_type'),
    result: stringValue(record, 'result'),
    notes: stringValue(record, 'notes'),
    reviewedAt: numberValue(record, 'reviewedAt', 'reviewed_at'),
  }
  return review.status || review.reviewerId || review.result || review.notes || review.reviewedAt > 0 ? review : null
}

function normalizeStructuredPlanCheckpointAttempts(value: unknown): StructuredPlanCheckpoint['attempts'] {
  if (!Array.isArray(value)) return []
  return value.map((entry) => {
    const record = objectValue(entry) ?? {}
    return {
      id: stringValue(record, 'id'),
      checkpointId: stringValue(record, 'checkpointId', 'checkpoint_id'),
      status: stringValue(record, 'status'),
      outcome: stringValue(record, 'outcome'),
      runId: stringValue(record, 'runId', 'run_id'),
      sessionId: stringValue(record, 'sessionId', 'session_id'),
      parentSessionId: stringValue(record, 'parentSessionId', 'parent_session_id'),
      startedAt: numberValue(record, 'startedAt', 'started_at'),
      completedAt: numberValue(record, 'completedAt', 'completed_at'),
      report: stringValue(record, 'report'),
      result: stringValue(record, 'result'),
      changedFiles: stringArrayValue(record, 'changedFiles', 'changed_files'),
      validation: stringArrayValue(record, 'validation'),
    }
  })
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
        taskProgram: normalizeStructuredPlanTaskProgram(checkpoint.taskProgram ?? checkpoint.task_program),
        notes: stringValue(checkpoint, 'notes'),
        report: stringValue(checkpoint, 'report'),
        result: stringValue(checkpoint, 'result'),
        changedFiles: stringArrayValue(checkpoint, 'changedFiles', 'changed_files'),
        validation: stringArrayValue(checkpoint, 'validation'),
        attemptId: stringValue(checkpoint, 'attemptId', 'attempt_id'),
        runId: stringValue(checkpoint, 'runId', 'run_id'),
        sessionId: stringValue(checkpoint, 'sessionId', 'session_id'),
        startedAt: numberValue(checkpoint, 'startedAt', 'started_at'),
        completedAt: numberValue(checkpoint, 'completedAt', 'completed_at'),
        review: normalizeStructuredPlanCheckpointReview(checkpoint.review),
        attempts: normalizeStructuredPlanCheckpointAttempts(checkpoint.attempts),
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
    executionPolicy: normalizeStructuredPlanExecutionPolicy(record.executionPolicy ?? record.execution_policy),
    executionState: normalizeStructuredPlanExecutionState(record.executionState ?? record.execution_state),
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
    task_program: checkpoint.taskProgram ? {
      id: checkpoint.taskProgram.id,
      max_concurrency: checkpoint.taskProgram.maxConcurrency || undefined,
      stages: checkpoint.taskProgram.stages.map((stage) => ({ id: stage.id, depends_on: stage.dependsOn, dependency_evidence: stage.dependencyEvidence })),
      jobs: checkpoint.taskProgram.jobs.map((job) => ({ id: job.id, stage_id: job.stageId, depends_on: job.dependsOn, agent_type: job.agentType, title: job.title, meta_prompt: job.metaPrompt, deliverable: job.deliverable, owned_scope: job.ownedScope, output_mode: job.outputMode || undefined, output_requirements: job.outputRequirements ?? undefined, acceptance_criteria: job.acceptanceCriteria, dependency_evidence: job.dependencyEvidence })),
    } : undefined,
    notes: checkpoint.notes,
    report: checkpoint.report,
    result: checkpoint.result,
    changed_files: checkpoint.changedFiles,
    validation: checkpoint.validation,
    attempt_id: checkpoint.attemptId,
    run_id: checkpoint.runId,
    session_id: checkpoint.sessionId,
    started_at: checkpoint.startedAt,
    completed_at: checkpoint.completedAt,
    review: checkpoint.review ? {
      status: checkpoint.review.status,
      reviewer_id: checkpoint.review.reviewerId,
      reviewer_type: checkpoint.review.reviewerType,
      result: checkpoint.review.result,
      notes: checkpoint.review.notes,
      reviewed_at: checkpoint.review.reviewedAt,
    } : undefined,
    attempts: checkpoint.attempts.map((attempt) => ({
      id: attempt.id,
      checkpoint_id: attempt.checkpointId,
      status: attempt.status,
      outcome: attempt.outcome,
      run_id: attempt.runId,
      session_id: attempt.sessionId,
      parent_session_id: attempt.parentSessionId,
      started_at: attempt.startedAt,
      completed_at: attempt.completedAt,
      report: attempt.report,
      result: attempt.result,
      changed_files: attempt.changedFiles,
      validation: attempt.validation,
    })),
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
    execution_policy: document.executionPolicy ? {
      mode: document.executionPolicy.mode,
      shape: document.executionPolicy.shape,
      followup_checkpoint_policy: document.executionPolicy.followupCheckpointPolicy,
    } : undefined,
    execution_state: document.executionState ? {
      status: document.executionState.status,
      active_attempt_id: document.executionState.activeAttemptId,
      parent_session_id: document.executionState.parentSessionId,
      current_session_id: document.executionState.currentSessionId,
      current_run_id: document.executionState.currentRunId,
      last_checkpoint_id: document.executionState.lastCheckpointId,
      last_attempt_id: document.executionState.lastAttemptId,
      last_outcome: document.executionState.lastOutcome,
      started_at: document.executionState.startedAt,
      updated_at: document.executionState.updatedAt,
      completed_at: document.executionState.completedAt,
    } : undefined,
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
      {active ? 'In progress' : status}
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

function TaskProgramSection({ program }: { program: StructuredPlanTaskProgram | null }) {
  if (!program) return null
  return (
    <div>
      <div className="text-[11px] font-semibold uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Task Program · {program.id}</div>
      <div className="mt-2 grid gap-2">
        {program.stages.map((stage) => (
          <div key={stage.id} className="rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] p-3">
            <div className="text-sm font-semibold text-[var(--app-text)]">{stage.id}</div>
            {stage.dependencyEvidence ? <p className="mt-1 text-xs text-[var(--app-text-muted)]">{stage.dependencyEvidence}</p> : null}
            <ul className="mt-2 space-y-1 text-sm text-[var(--app-text-muted)]">
              {program.jobs.filter((job) => job.stageId === stage.id).map((job) => <li key={job.id}>• {job.title} <span className="text-[var(--app-text-subtle)]">({job.agentType})</span></li>)}
            </ul>
          </div>
        ))}
      </div>
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
          </div>
          <h4 className={cn('mt-2 text-base font-semibold leading-snug', active ? 'text-[var(--app-primary)]' : 'text-[var(--app-text)]')}>
            {checkpoint.title || `Checkpoint ${checkpoint.order}`}
          </h4>
          {checkpoint.objective ? (
            <p className="mt-1 whitespace-pre-wrap break-words text-sm leading-6 text-[var(--app-text-muted)]">{checkpoint.objective}</p>
          ) : null}
        </div>
      </div>

      <div className="mt-4 grid gap-3 border-t border-[var(--app-border)] pt-4">
        <CheckpointSection title="Tasks" values={checkpoint.tasks} />
        <CheckpointSection title="Acceptance" values={checkpoint.acceptanceCriteria} />
        <TaskProgramSection program={checkpoint.taskProgram} />
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

export function StructuredPlanReviewView({ document, className }: { document: StructuredPlanDocument; className?: string }) {
  const review = structuredPlanReviewProjection(document)
  return (
    <div className={cn('grid gap-4', className)}>
      <section className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-bg-alt)] p-4 sm:p-5">
        <div className="text-xs font-semibold uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Objective</div>
        <h3 className="mt-1 text-lg font-semibold text-[var(--app-text)]">{review.title}</h3>
        {review.objective && review.objective !== review.title ? (
          <p className="mt-2 whitespace-pre-wrap break-words text-sm leading-6 text-[var(--app-text)]">{review.objective}</p>
        ) : null}
      </section>
      <section className="overflow-hidden rounded-2xl border border-[var(--app-border)] bg-[var(--app-bg-alt)]">
        <div className="flex items-center justify-between gap-3 border-b border-[var(--app-border)] px-4 py-3">
          <div className="flex items-center gap-2"><ListTodo className="size-4 text-[var(--app-primary)]" /><h3 className="font-semibold text-[var(--app-text)]">Checkpoints</h3></div>
          <span className="text-xs text-[var(--app-text-muted)]">{review.checkpoints.length}</span>
        </div>
        {review.checkpoints.length ? review.checkpoints.map((checkpoint, index) => (
          <details key={checkpoint.id || `${checkpoint.order}:${checkpoint.title}`} className="group border-b border-[var(--app-border)] last:border-b-0">
            <summary className="flex cursor-pointer list-none items-center gap-3 px-4 py-3 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-[var(--app-focus-ring)]">
              <Circle className="size-4 shrink-0 text-[var(--app-text-muted)]" />
              <span className="min-w-0 flex-1 text-sm font-medium text-[var(--app-text)]"><span className="mr-1.5 text-[var(--app-text-muted)]">{index + 1}.</span>{checkpoint.title}</span>
              <ChevronRight className="size-4 shrink-0 text-[var(--app-text-muted)] transition-transform group-open:rotate-90" />
            </summary>
            <div className="grid gap-3 px-4 pb-4 pl-11">
              <CheckpointTextSection title="Objective" value={checkpoint.objective} />
              <CheckpointSection title="Tasks" values={checkpoint.tasks} />
              <CheckpointSection title="Acceptance" values={checkpoint.acceptanceCriteria} />
              <TaskProgramSection program={review.authoritativeDocument.checkpoints.find((item) => item.id === checkpoint.id)?.taskProgram ?? null} />
            </div>
          </details>
        )) : <p className="px-4 py-4 text-sm text-[var(--app-text-muted)]">No checkpoints are defined.</p>}
      </section>
    </div>
  )
}

export function StructuredPlanDocumentView({
  document,
  emptyText = 'No structured plan document was provided.',
  className,
  compact = false,
  review = false,
}: {
  document: StructuredPlanDocument | null
  emptyText?: string
  className?: string
  compact?: boolean
  review?: boolean
}) {
  if (!document) {
    return (
      <section className={cn('rounded-2xl border border-dashed border-[var(--app-border)] bg-[var(--app-bg-alt)] px-4 py-5 text-sm text-[var(--app-text-muted)]', className)}>
        {emptyText}
      </section>
    )
  }

  const activeID = document.activeCheckpointId.trim()
  const activeCheckpoint = document.checkpoints.find((checkpoint) => checkpoint.id === activeID)
  const activeCheckpointNumber = activeCheckpoint ? activeCheckpoint.order : 0

  if (review) {
    return <StructuredPlanReviewView document={document} className={className} />
  }

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
            {activeCheckpointNumber > 0 ? (
              <span className="rounded-full border border-[var(--app-primary-border)] bg-[var(--app-primary-soft)] px-2.5 py-1 text-xs font-medium text-[var(--app-primary)]">
                Checkpoint {activeCheckpointNumber} active
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
