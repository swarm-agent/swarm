import type { DesktopSessionPlanCheckpoint, DesktopSessionPlanDocument, DesktopSessionPlanInfo, DesktopSessionPlanRecord, DesktopSessionPlanRevisionRecord } from '../types/chat'

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
  const checkpoints = (Array.isArray(record.checkpoints) ? record.checkpoints : [])
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
        acceptanceCriteria: stringArrayValue(checkpoint, 'acceptanceCriteria', 'acceptance_criteria'),
        notes: stringValue(checkpoint, 'notes'),
        report: stringValue(checkpoint, 'report'),
        result: stringValue(checkpoint, 'result'),
        changedFiles: stringArrayValue(checkpoint, 'changedFiles', 'changed_files'),
        validation: stringArrayValue(checkpoint, 'validation'),
        order: numberValue(checkpoint.order) || index + 1,
      }
    })
    .filter((entry): entry is DesktopSessionPlanCheckpoint => entry !== null)
    .sort((left, right) => left.order - right.order)

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
