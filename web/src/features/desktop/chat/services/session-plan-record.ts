import type { DesktopPlanFinalHandoff, DesktopSessionPlanCheckpoint, DesktopSessionPlanDocument, DesktopSessionPlanInfo, DesktopSessionPlanRecord, DesktopSessionPlanRevisionRecord } from '../types/chat'

export interface DesktopSessionPlanWire {
  id?: string
  title?: string
  plan?: string
  document?: unknown
  status?: string
  approval_state?: string
  updated_at?: number
  created_at?: number
  version?: number
  prior_title?: string
  prior_plan?: string
  diff_lines?: unknown[]
  update_summary?: string
  update_scope?: string
  update_kind?: string
  revision_kind?: string
  restored_from_version?: number
  parent_revision?: number
  checkpoint?: boolean
}

export function normalizeDesktopSessionPlan(plan: unknown): DesktopSessionPlanRecord {
  const planRecord = objectValue(plan) ?? {}
  return {
    id: String(planRecord.id ?? '').trim(),
    title: String(planRecord.title ?? '').trim(),
    plan: String(planRecord.plan ?? ''),
    document: normalizeDesktopSessionPlanDocument(planRecord.document),
    status: String(planRecord.status ?? '').trim(),
    approvalState: String(planRecord.approval_state ?? '').trim(),
    updatedAt: numberValue(planRecord.updated_at),
  }
}

export function normalizeDesktopSessionPlanRevisions(revisions: unknown): DesktopSessionPlanRevisionRecord[] {
  const revisionList = Array.isArray(revisions) ? revisions : []
  return revisionList.map((revision, index) => {
    const revisionRecord = objectValue(revision) ?? {}
    const plan = normalizeDesktopSessionPlan(revisionRecord)
    const version = numberValue(revisionRecord.version)
    return {
      ...plan,
      key: `${plan.id || 'plan'}:${version}:${index}`,
      createdAt: numberValue(revisionRecord.created_at),
      priorTitle: String(revisionRecord.prior_title ?? ''),
      priorPlan: String(revisionRecord.prior_plan ?? ''),
      diffLines: Array.isArray(revisionRecord.diff_lines) ? revisionRecord.diff_lines.map((line) => String(line)) : [],
      updateSummary: String(revisionRecord.update_summary ?? '').trim(),
      updateScope: String(revisionRecord.update_scope ?? '').trim(),
      updateKind: String(revisionRecord.update_kind ?? '').trim(),
      revisionKind: String(revisionRecord.revision_kind ?? '').trim(),
      restoredFromVersion: numberValue(revisionRecord.restored_from_version),
      version,
      parentRevision: numberValue(revisionRecord.parent_revision),
      checkpoint: Boolean(revisionRecord.checkpoint),
    }
  })
}

function normalizeDesktopSessionPlanDocument(value: unknown): DesktopSessionPlanDocument | null {
  const record = objectValue(value)
  if (!record) {
    return null
  }
  const infoRecord = objectValue(record.info) ?? {}
  const checkpoints = normalizeDesktopSessionPlanCheckpoints(record.checkpoints)

  const info: DesktopSessionPlanInfo = {
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
  }

  const document: DesktopSessionPlanDocument = {
    id: stringValue(record, 'id'),
    title: stringValue(record, 'title'),
    status: stringValue(record, 'status'),
    schemaVersion: stringValue(record, 'schemaVersion', 'schema_version'),
    revisionId: stringValue(record, 'revisionId', 'revision_id'),
    info,
    executionPolicy: normalizeDesktopSessionPlanExecutionPolicy(record.executionPolicy ?? record.execution_policy),
    executionState: normalizeDesktopSessionPlanExecutionState(record.executionState ?? record.execution_state),
    artifacts: normalizeDesktopSessionPlanArtifacts(record.artifacts),
    checkpoints,
    originalCheckpoints: normalizeDesktopSessionPlanCheckpoints(record.originalCheckpoints ?? record.original_checkpoints),
    activeCheckpointId: stringValue(record, 'activeCheckpointId', 'active_checkpoint_id'),
    renderedText: rawStringValue(record, 'renderedText', 'rendered_text'),
    displayText: rawStringValue(record, 'displayText', 'display_text'),
  }
  if (!document.id && !document.title && !document.info.goal && document.checkpoints.length === 0) {
    return null
  }
  return document
}

function objectValue(value: unknown): Record<string, unknown> | null {
  return value && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : null
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

function normalizeDesktopSessionPlanExecutionPolicy(value: unknown): DesktopSessionPlanDocument['executionPolicy'] {
  const record = objectValue(value)
  if (!record) return null
  const policy = {
    mode: stringValue(record, 'mode'),
    shape: stringValue(record, 'shape'),
    followupCheckpointPolicy: stringValue(record, 'followupCheckpointPolicy', 'followup_checkpoint_policy'),
  }
  return policy.mode || policy.shape || policy.followupCheckpointPolicy ? policy : null
}

function normalizeDesktopSessionPlanExecutionState(value: unknown): DesktopSessionPlanDocument['executionState'] {
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
    startedAt: numberValue(record.startedAt ?? record.started_at),
    updatedAt: numberValue(record.updatedAt ?? record.updated_at),
    completedAt: numberValue(record.completedAt ?? record.completed_at),
  }
  return state.status || state.activeAttemptId || state.currentRunId || state.lastCheckpointId ? state : null
}

function normalizeDesktopSessionPlanCheckpoints(value: unknown): DesktopSessionPlanCheckpoint[] {
  return (Array.isArray(value) ? value : [])
    .map((entry, index): DesktopSessionPlanCheckpoint | null => {
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
        subtasks: normalizeDesktopSessionPlanSubtasks(checkpoint.subtasks, stringArrayValue(checkpoint, 'tasks')),
        activeSubtaskId: stringValue(checkpoint, 'activeSubtaskId', 'active_subtask_id'),
        acceptanceCriteria: stringArrayValue(checkpoint, 'acceptanceCriteria', 'acceptance_criteria'),
        artifacts: normalizeDesktopSessionPlanArtifacts(checkpoint.artifacts),
        notes: stringValue(checkpoint, 'notes'),
        report: stringValue(checkpoint, 'report'),
        result: stringValue(checkpoint, 'result'),
        changedFiles: stringArrayValue(checkpoint, 'changedFiles', 'changed_files'),
        validation: stringArrayValue(checkpoint, 'validation'),
        attemptId: stringValue(checkpoint, 'attemptId', 'attempt_id'),
        runId: stringValue(checkpoint, 'runId', 'run_id'),
        sessionId: stringValue(checkpoint, 'sessionId', 'session_id'),
        startedAt: numberValue(checkpoint.startedAt ?? checkpoint.started_at),
        completedAt: numberValue(checkpoint.completedAt ?? checkpoint.completed_at),
        review: normalizeDesktopSessionPlanCheckpointReview(checkpoint.review),
        recommendation: normalizeDesktopSessionPlanCheckpointRecommendation(checkpoint.recommendation),
        finalHandoff: normalizeDesktopPlanFinalHandoff(checkpoint.finalHandoff ?? checkpoint.final_handoff ?? checkpoint.handoff),
        attempts: normalizeDesktopSessionPlanCheckpointAttempts(checkpoint.attempts),
        order: numberValue(checkpoint.order) || index + 1,
      }
    })
    .filter((entry): entry is DesktopSessionPlanCheckpoint => entry !== null)
    .sort((left, right) => left.order - right.order)
}

function normalizeDesktopSessionPlanArtifacts(value: unknown): DesktopSessionPlanDocument['artifacts'] {
  return (Array.isArray(value) ? value : []).map((entry) => {
    const record = objectValue(entry) ?? {}
    return {
      path: stringValue(record, 'path'),
      role: stringValue(record, 'role'),
      description: stringValue(record, 'description'),
      mediaType: stringValue(record, 'mediaType', 'media_type'),
    }
  }).filter((entry) => entry.path)
}

function normalizeDesktopSessionPlanSubtasks(value: unknown, legacyTasks: string[]): DesktopSessionPlanCheckpoint['subtasks'] {
  const source = Array.isArray(value) && value.length > 0
    ? value
    : legacyTasks.map((title, index) => ({ id: `task-${index + 1}`, title, status: 'pending', order: index + 1 }))
  return source.map((entry, index) => {
    const record = objectValue(entry) ?? {}
    return {
      id: stringValue(record, 'id') || `task-${index + 1}`,
      title: stringValue(record, 'title'),
      status: stringValue(record, 'status') || 'pending',
      notes: stringValue(record, 'notes'),
      result: stringValue(record, 'result'),
      startedAt: numberValue(record.startedAt ?? record.started_at),
      completedAt: numberValue(record.completedAt ?? record.completed_at),
      order: numberValue(record.order) || index + 1,
    }
  }).filter((entry) => entry.title).sort((left, right) => left.order - right.order)
}

function normalizeDesktopSessionPlanCheckpointReview(value: unknown): DesktopSessionPlanCheckpoint['review'] {
  const record = objectValue(value)
  if (!record) return null
  const review = {
    status: stringValue(record, 'status'),
    reviewerId: stringValue(record, 'reviewerId', 'reviewer_id'),
    reviewerType: stringValue(record, 'reviewerType', 'reviewer_type'),
    result: stringValue(record, 'result'),
    notes: stringValue(record, 'notes'),
    reviewedAt: numberValue(record.reviewedAt ?? record.reviewed_at),
  }
  return review.status || review.reviewerId || review.result || review.notes || review.reviewedAt > 0 ? review : null
}

function normalizeDesktopSessionPlanCheckpointRecommendation(value: unknown): DesktopSessionPlanCheckpoint['recommendation'] {
  const record = objectValue(value)
  if (!record) return null
  const recommendation = {
    decision: stringValue(record, 'decision'),
    action: stringValue(record, 'action'),
    reason: stringValue(record, 'reason'),
    actionState: stringValue(record, 'actionState', 'action_state'),
  }
  return recommendation.decision || recommendation.action || recommendation.reason || recommendation.actionState ? recommendation : null
}

export function normalizeDesktopPlanFinalHandoff(value: unknown): DesktopPlanFinalHandoff | null {
  const record = objectValue(value)
  if (!record) return null
  const schemaVersion = numberValue(record.schemaVersion ?? record.schema_version)
  if (schemaVersion !== 1) return null
  const detailsRecord = objectValue(record.details) ?? {}
  const recommendation = normalizeDesktopSessionPlanCheckpointRecommendation(record.recommendation) ?? null
  const suggestedPromptValue = record.suggestedPrompts ?? record.suggested_prompts
  const suggestedPromptEntries: unknown[] = Array.isArray(suggestedPromptValue) ? suggestedPromptValue : []
  const suggestedPrompts = suggestedPromptEntries
    .map((entry: unknown) => {
      const prompt = objectValue(entry) ?? {}
      return {
        label: stringValue(prompt, 'label'),
        prompt: stringValue(prompt, 'prompt'),
      }
    })
    .filter((entry: { label: string; prompt: string }) => entry.label && entry.prompt)
    .slice(0, 3)
  const handoff: DesktopPlanFinalHandoff = {
    schemaVersion,
    title: stringValue(record, 'title'),
    overview: stringValue(record, 'overview'),
    impactBullets: stringArrayValue(record, 'impactBullets', 'impact_bullets').slice(0, 3),
    recommendation,
    suggestedPrompts,
    details: {
      report: rawStringValue(detailsRecord, 'report'),
      result: rawStringValue(detailsRecord, 'result'),
      changedFiles: stringArrayValue(detailsRecord, 'changedFiles', 'changed_files'),
      validation: stringArrayValue(detailsRecord, 'validation'),
    },
  }
  return handoff.title && handoff.overview ? handoff : null
}

function normalizeDesktopSessionPlanCheckpointAttempts(value: unknown): DesktopSessionPlanCheckpoint['attempts'] {
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
      startedAt: numberValue(record.startedAt ?? record.started_at),
      completedAt: numberValue(record.completedAt ?? record.completed_at),
      report: stringValue(record, 'report'),
      result: stringValue(record, 'result'),
      changedFiles: stringArrayValue(record, 'changedFiles', 'changed_files'),
      validation: stringArrayValue(record, 'validation'),
    }
  })
}

function stringArrayValue(record: Record<string, unknown>, ...keys: string[]): string[] {
  for (const key of keys) {
    const value = record[key]
    if (Array.isArray(value)) {
      return value.map((entry) => typeof entry === 'string' ? entry.trim() : '').filter(Boolean)
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

function numberValue(value: unknown): number {
  return typeof value === 'number' && Number.isFinite(value) ? value : 0
}
