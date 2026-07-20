export const PLAN_RUNTIME_PROTOCOL = 'v3.plan_execution' as const
export const PLAN_RUNTIME_PROTOCOL_VERSION = 1 as const
export const PLAN_RUNTIME_DELTA_KIND = 'plan.execution.delta' as const

export interface PlanDefinitionWire {
  schema_version: number
  session_id: string
  plan_id: string
  definition_revision: number
  title: string
  goal?: string
  scope?: string
  continuation_default?: string
  checkpoint_order: string[]
  [key: string]: unknown
}

export interface PlanCheckpointDefinitionWire {
  session_id: string
  plan_id: string
  definition_revision: number
  checkpoint_id: string
  order: number
  title: string
  subtask_order?: string[]
  [key: string]: unknown
}

export interface PlanSubtaskDefinitionWire {
  session_id: string
  plan_id: string
  definition_revision: number
  checkpoint_id: string
  subtask_id: string
  order: number
  title: string
  [key: string]: unknown
}

export interface PlanExecutionSummaryWire {
  session_id: string
  plan_id: string
  definition_revision: number
  execution_seq: number
  status: string
  active_checkpoint_id?: string
  next_checkpoint_id?: string
  active_attempt_id?: string
  completed_checkpoint_count?: number
  [key: string]: unknown
}

export interface PlanCheckpointExecutionWire {
  session_id: string
  plan_id: string
  checkpoint_id: string
  execution_seq: number
  status: string
  active_subtask_id?: string
  next_subtask_id?: string
  [key: string]: unknown
}

export interface PlanSubtaskExecutionWire {
  session_id: string
  plan_id: string
  checkpoint_id: string
  subtask_id: string
  execution_seq: number
  status: string
  [key: string]: unknown
}

export interface PlanRuntimeDeltaWire {
  protocol: typeof PLAN_RUNTIME_PROTOCOL
  protocol_version: typeof PLAN_RUNTIME_PROTOCOL_VERSION
  kind: typeof PLAN_RUNTIME_DELTA_KIND
  schema_version: number
  session_id: string
  plan_id: string
  definition_revision: number
  execution_seq: number
  event_id: string
  event_type: string
  checkpoint_id?: string
  subtask_ids?: string[]
  summary_change: PlanExecutionSummaryWire
  checkpoint_change?: PlanCheckpointExecutionWire
  subtask_changes?: PlanSubtaskExecutionWire[]
  next_action?: string
  created_at: number
}

export interface PlanRuntimeHydrationWire {
  schema_version: number
  definition: PlanDefinitionWire
  checkpoint_definitions: Record<string, PlanCheckpointDefinitionWire>
  subtask_definitions: Record<string, PlanSubtaskDefinitionWire>
  summary: PlanExecutionSummaryWire
  checkpoint_executions: Record<string, PlanCheckpointExecutionWire>
  subtask_executions: Record<string, PlanSubtaskExecutionWire>
}

export interface PlanRuntimeClientState {
  definition?: PlanDefinitionWire
  checkpointDefinitionsById: Record<string, PlanCheckpointDefinitionWire>
  subtaskDefinitionsById: Record<string, PlanSubtaskDefinitionWire>
  summary?: PlanExecutionSummaryWire
  checkpointExecutionsById: Record<string, PlanCheckpointExecutionWire>
  subtaskExecutionsById: Record<string, PlanSubtaskExecutionWire>
  appliedExecutionSeq: number
  stale: boolean
  hydrateRequired: boolean
  staleReason?: 'missing_hydration' | 'definition_revision_mismatch' | 'execution_sequence_gap' | 'invalid_target'
}

export type PlanRuntimeApplyResult = 'applied' | 'duplicate' | 'stale'

export function createEmptyPlanRuntimeClientState(): PlanRuntimeClientState {
  return {
    checkpointDefinitionsById: {},
    subtaskDefinitionsById: {},
    checkpointExecutionsById: {},
    subtaskExecutionsById: {},
    appliedExecutionSeq: 0,
    stale: false,
    hydrateRequired: false,
  }
}

export function hydratePlanRuntimeState(raw: PlanRuntimeHydrationWire): PlanRuntimeClientState {
  const planId = requireID(raw.definition?.plan_id, 'definition.plan_id')
  const sessionId = requireID(raw.definition?.session_id, 'definition.session_id')
  const revision = requireSequence(raw.definition?.definition_revision, 'definition.definition_revision', false)
  const checkpointDefinitionsById: Record<string, PlanCheckpointDefinitionWire> = {}
  const subtaskDefinitionsById: Record<string, PlanSubtaskDefinitionWire> = {}

  for (const [wireKey, checkpoint] of Object.entries(raw.checkpoint_definitions ?? {})) {
    const checkpointId = requireID(checkpoint.checkpoint_id, 'checkpoint_definition.checkpoint_id')
    assertIdentity(checkpoint, sessionId, planId, revision)
    if (wireKey !== checkpointId) throw new Error(`plan runtime checkpoint key mismatch: ${wireKey}`)
    checkpointDefinitionsById[checkpointId] = checkpoint
  }
  for (const [wireKey, subtask] of Object.entries(raw.subtask_definitions ?? {})) {
    const checkpointId = requireID(subtask.checkpoint_id, 'subtask_definition.checkpoint_id')
    const subtaskId = requireID(subtask.subtask_id, 'subtask_definition.subtask_id')
    assertIdentity(subtask, sessionId, planId, revision)
    if (!checkpointDefinitionsById[checkpointId]) throw new Error(`plan runtime subtask references unknown checkpoint ${checkpointId}`)
    const key = runtimeSubtaskKey(checkpointId, subtaskId)
    if (wireKey !== key) throw new Error(`plan runtime subtask key mismatch: ${wireKey}`)
    subtaskDefinitionsById[key] = subtask
  }

  const appliedExecutionSeq = normalizeExecutionSeq(raw.summary)
  assertSummaryIdentity(raw.summary, sessionId, planId, revision)
  const state: PlanRuntimeClientState = {
    definition: raw.definition,
    checkpointDefinitionsById,
    subtaskDefinitionsById,
    summary: raw.summary,
    checkpointExecutionsById: {},
    subtaskExecutionsById: {},
    appliedExecutionSeq,
    stale: false,
    hydrateRequired: false,
  }
  for (const [wireKey, checkpoint] of Object.entries(raw.checkpoint_executions ?? {})) {
    const checkpointId = requireID(checkpoint.checkpoint_id, 'checkpoint_execution.checkpoint_id')
    if (wireKey !== checkpointId || !checkpointDefinitionsById[checkpointId]) throw new Error(`invalid checkpoint execution target ${wireKey}`)
    state.checkpointExecutionsById[checkpointId] = checkpoint
  }
  for (const [wireKey, subtask] of Object.entries(raw.subtask_executions ?? {})) {
    const key = runtimeSubtaskKey(requireID(subtask.checkpoint_id, 'subtask_execution.checkpoint_id'), requireID(subtask.subtask_id, 'subtask_execution.subtask_id'))
    if (wireKey !== key || !subtaskDefinitionsById[key]) throw new Error(`invalid subtask execution target ${wireKey}`)
    state.subtaskExecutionsById[key] = subtask
  }
  return state
}

// applyPlanRuntimeDelta mutates only the explicitly named summary/checkpoint/
// subtask records. A gap never attempts event replay or legacy plan parsing; it
// marks this plan stale so the caller can request the targeted runtime hydrate.
export function applyPlanRuntimeDelta(state: PlanRuntimeClientState, delta: PlanRuntimeDeltaWire): PlanRuntimeApplyResult {
  if (delta.protocol !== PLAN_RUNTIME_PROTOCOL || delta.protocol_version !== PLAN_RUNTIME_PROTOCOL_VERSION || delta.kind !== PLAN_RUNTIME_DELTA_KIND) {
    throw new Error('unsupported plan runtime delta schema')
  }
  if (delta.execution_seq <= state.appliedExecutionSeq) return 'duplicate'
  if (!state.definition) return markStale(state, 'missing_hydration')
  if (delta.session_id !== state.definition.session_id || delta.plan_id !== state.definition.plan_id || delta.definition_revision !== state.definition.definition_revision) {
    return markStale(state, 'definition_revision_mismatch')
  }
  if (delta.execution_seq !== state.appliedExecutionSeq + 1) return markStale(state, 'execution_sequence_gap')
  if (delta.summary_change.execution_seq !== delta.execution_seq) return markStale(state, 'invalid_target')

  if (delta.checkpoint_change) {
    const checkpointId = delta.checkpoint_change.checkpoint_id
    if (!checkpointId || !state.checkpointDefinitionsById[checkpointId] || delta.checkpoint_id !== checkpointId) return markStale(state, 'invalid_target')
  }
  for (const subtask of delta.subtask_changes ?? []) {
    const key = runtimeSubtaskKey(subtask.checkpoint_id, subtask.subtask_id)
    if (!state.subtaskDefinitionsById[key] || (delta.subtask_ids?.includes(subtask.subtask_id) !== true)) return markStale(state, 'invalid_target')
  }

  state.summary = delta.summary_change
  if (delta.checkpoint_change) state.checkpointExecutionsById[delta.checkpoint_change.checkpoint_id] = delta.checkpoint_change
  for (const subtask of delta.subtask_changes ?? []) {
    state.subtaskExecutionsById[runtimeSubtaskKey(subtask.checkpoint_id, subtask.subtask_id)] = subtask
  }
  state.appliedExecutionSeq = delta.execution_seq
  state.stale = false
  state.hydrateRequired = false
  state.staleReason = undefined
  return 'applied'
}

export function applyPlanRuntimeReplay(state: PlanRuntimeClientState, records: PlanRuntimeDeltaWire[]): PlanRuntimeApplyResult {
  for (const record of records) {
    const result = applyPlanRuntimeDelta(state, record)
    if (result === 'stale') return result
  }
  return 'applied'
}

export function runtimeSubtaskKey(checkpointId: string, subtaskId: string): string {
  return `${checkpointId}\u0000${subtaskId}`
}

function markStale(state: PlanRuntimeClientState, reason: NonNullable<PlanRuntimeClientState['staleReason']>): 'stale' {
  state.stale = true
  state.hydrateRequired = true
  state.staleReason = reason
  return 'stale'
}

function assertIdentity(value: { session_id: string; plan_id: string; definition_revision: number }, sessionId: string, planId: string, revision: number): void {
  if (value.session_id !== sessionId || value.plan_id !== planId || value.definition_revision !== revision) {
    throw new Error('plan runtime hydration contains mixed definition identities')
  }
}

function assertSummaryIdentity(value: PlanExecutionSummaryWire, sessionId: string, planId: string, revision: number): void {
  if (value.execution_seq === 0 && !value.session_id && !value.plan_id && !value.definition_revision) return
  assertIdentity(value, sessionId, planId, revision)
}

function normalizeExecutionSeq(summary: PlanExecutionSummaryWire): number {
  return summary?.execution_seq ? requireSequence(summary.execution_seq, 'summary.execution_seq', true) : 0
}

function requireSequence(value: number, name: string, allowZero: boolean): number {
  if (!Number.isSafeInteger(value) || value < (allowZero ? 0 : 1)) throw new Error(`invalid ${name}`)
  return value
}

function requireID(value: string | undefined, name: string): string {
  const normalized = value?.trim()
  if (!normalized) throw new Error(`missing ${name}`)
  return normalized
}
