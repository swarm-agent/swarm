import { requestJson } from '../../../app/api'

/** Canonical /v3/workers wire records; never infer worker identity from sessions or legacy automations. */
export type WorkerLifecycleState = 'idle' | 'active' | 'stopping' | 'paused' | 'archived' | 'deleted'
export interface WorkerCapabilityRequest { type: string; name: string; description?: string; required: boolean }
export interface WorkerWorkspaceRequirement { role: string; description?: string; required: boolean }
export interface WorkerInputRequirement { name: string; kind: string; description?: string; required: boolean; default?: unknown }
export interface WorkerDeliverableRequirement { name: string; kind: string; description?: string; required: boolean }
export interface WorkerTriggerConfig { trigger_kind: string; format?: string; secret_ref?: string }
export interface WorkerAutomation {
  id: string; worker_id: string; name: string; description?: string | null
  activation_mode: 'manual' | 'interval' | 'cron' | 'external_trigger'
  schedule?: { kind: 'interval' | 'cron' | 'trigger'; interval_seconds?: number; cron?: string; timezone?: string } | null
  trigger?: WorkerTriggerConfig | null; enabled: boolean
  plan_document: { title: string; info?: { goal?: string; [key: string]: unknown }; checkpoints?: Array<{ id: string; title?: string; [key: string]: unknown }>; [key: string]: unknown }; revision: number; created_at: number; updated_at: number
  input_requirements?: WorkerInputRequirement[] | null; deliverable_requirements?: WorkerDeliverableRequirement[] | null
}
export interface WorkerRecord {
  id: string; account_scope_id: string; name: string; description?: string | null; instructions: string
  lifecycle_state: WorkerLifecycleState; revision: number; created_at: number; updated_at: number
  requested_capabilities?: WorkerCapabilityRequest[] | null
  workspace_requirements?: WorkerWorkspaceRequirement[] | null
  local_bindings?: Record<string, string> | null
  automations?: WorkerAutomation[] | null; metadata?: Record<string, unknown> | null
  provenance?: { source_worker_id?: string; source_revision?: number; source_session_id?: string; source_proposal_id?: string; imported_at?: number; migrated_at?: number; exported_at?: number; author?: string } | null
}
export interface WorkerAutomationInput extends Omit<WorkerAutomation, 'id' | 'worker_id' | 'revision' | 'created_at' | 'updated_at' | 'enabled'> { enabled?: boolean }
export interface WorkerRun {
  id: string; account_scope_id: string; worker_id: string; worker_revision: number
  automation_id?: string; automation_revision?: number; occurrence_id?: string; session_id?: string
  request_source: string; input?: Record<string, unknown>
  status: 'admitted' | 'running' | 'succeeded' | 'failed' | 'cancelled'
  cancel_requested?: boolean; error?: string; deliverables?: Array<Record<string, unknown>>
  started_at?: number; completed_at?: number; created_at: number
}
export interface WorkerRunSummary {
  active: Array<{ id: string; session_id?: string; status: string; created_at: number }>
  active_truncated: boolean; date: string; timezone: string; day_start_at: number; day_end_at: number
  daily_runs: number; daily_success: number; daily_failed: number; daily_cancelled: number
  active_runs: number; scanned_runs: number; truncated: boolean
}
export interface WorkerSummary { worker_id: string; runs: WorkerRunSummary; next_scheduled_at: number }
export interface WorkerRevision {
  worker_id: string; account_scope_id: string; revision: number; worker: WorkerRecord
  committed_at: number; committed_by?: string; change_summary?: string
}
export type WorkerRead =
  | { kind: 'list'; accountScopeId: string; cursor?: string; limit?: number; lifecycleState?: WorkerLifecycleState; includeDeleted?: boolean }
  | { kind: 'detail'; accountScopeId: string; workerId: string }
  | { kind: 'runs'; accountScopeId: string; workerId: string; cursor?: string; limit?: number }
  | { kind: 'history'; accountScopeId: string; workerId: string; cursor?: string; limit?: number }
  | { kind: 'run'; accountScopeId: string; workerId: string; runId: string }
  | { kind: 'summary'; accountScopeId: string; workerId: string; timezone: string; date: string }
export type WorkerReadResult =
  | { workers: WorkerRecord[]; next_cursor?: string }
  | { worker: WorkerRecord }
  | { runs: WorkerRun[]; next_cursor?: string }
  | { revisions: WorkerRevision[]; next_cursor?: string }
  | { run: WorkerRun }
  | WorkerSummary
export type WorkerMutation =
  | { action: 'create'; name: string; instructions?: string; description?: string; idempotency_key: string; requested_capabilities?: WorkerCapabilityRequest[]; workspace_requirements?: WorkerWorkspaceRequirement[]; metadata?: Record<string, unknown> }
  | { action: 'update'; workerId: string; expected_revision: number; changes: { name?: string; description?: string; instructions?: string; change_summary?: string; requested_capabilities?: WorkerCapabilityRequest[]; workspace_requirements?: WorkerWorkspaceRequirement[]; metadata?: Record<string, unknown> } }
  | { action: 'activate'; workerId: string; expected_revision: number; local_bindings: Record<string, string>; activate?: boolean }
  | { action: 'pause' | 'resume' | 'archive' | 'delete'; workerId: string; expected_revision: number }
  | { action: 'attachAutomation'; workerId: string; expected_revision: number; automation: WorkerAutomationInput }
  | { action: 'updateAutomation'; workerId: string; automationId: string; expected_revision: number; automation: WorkerAutomationInput }
  | { action: 'removeAutomation' | 'enableAutomation' | 'disableAutomation'; workerId: string; automationId: string; expected_revision: number }
  | { action: 'direct'; workerId: string; prompt?: string; input?: Record<string, unknown>; idempotency_key?: string }
  | { action: 'test'; workerId: string; automationId?: string; prompt?: string; input?: Record<string, unknown>; idempotency_key?: string }
  | { action: 'cancelRun'; workerId: string; runId: string }
export type WorkerMutationResult = { worker: WorkerRecord } | { run: WorkerRun } | { ok: true; deleted: true }

function required(value: string, label: string): string {
  if (!value?.trim()) throw new Error(`${label} is required`)
  return value.trim()
}
function revision(value: number): number {
  if (!Number.isSafeInteger(value) || value < 1) throw new Error('A current worker revision is required')
  return value
}
function paging(input: { cursor?: string; limit?: number }): URLSearchParams {
  const query = new URLSearchParams()
  if (input.cursor) {
    if (input.cursor.length > 1024) throw new Error('Worker cursor is too long')
    query.set('cursor', input.cursor)
  }
  if (input.limit !== undefined) {
    if (!Number.isSafeInteger(input.limit) || input.limit < 1 || input.limit > 100) throw new Error('Worker page limit must be 1–100')
    query.set('limit', String(input.limit))
  }
  return query
}
function pagePath(path: string, query: URLSearchParams): string {
  return `${path}${query.toString() ? `?${query}` : ''}`
}
function workerPath(id: string): string { return `/v3/workers/${encodeURIComponent(required(id, 'Worker ID'))}` }
function automationPath(id: string, automationId: string): string {
  return `${workerPath(id)}/automations/${encodeURIComponent(required(automationId, 'Automation ID'))}`
}
function envelope<T>(raw: T | null, field: keyof T): T {
  if (!raw || typeof raw !== 'object' || !(field in raw) || raw[field] === null || raw[field] === undefined) throw new Error(`Invalid worker ${String(field)} response`)
  return raw
}
export async function readWorkers(input: WorkerRead): Promise<WorkerReadResult> {
  const path = input.kind === 'list' ? '/v3/workers' : workerPath(input.workerId)
  if (input.kind === 'detail') return envelope(await requestJson<{ worker: WorkerRecord }>(path), 'worker')
  if (input.kind === 'summary') {
    if (!/^\d{4}-\d{2}-\d{2}$/.test(input.date) || !input.timezone) throw new Error('Worker summary needs date and timezone')
    const query = new URLSearchParams({ timezone: input.timezone, date: input.date })
    const raw = await requestJson<WorkerSummary>(`${path}/summary?${query}`)
    if (!raw || raw.worker_id !== input.workerId || raw.runs?.date !== input.date || raw.runs?.timezone !== input.timezone || typeof raw.runs?.daily_runs !== 'number' || typeof raw.next_scheduled_at !== 'number') throw new Error('Invalid worker summary response')
    return raw
  }
  if (input.kind === 'run') return envelope(await requestJson<{ run: WorkerRun }>(`${path}/runs/${encodeURIComponent(required(input.runId, 'Run ID'))}`), 'run')
  const query = paging(input)
  if (input.kind === 'list') {
    if (input.lifecycleState) query.set('lifecycle_state', input.lifecycleState)
    if (input.includeDeleted) query.set('include_deleted', 'true')
  }
  const suffix = input.kind === 'list' ? '' : input.kind === 'runs' ? '/runs' : '/history'
  const raw = await requestJson<unknown>(pagePath(path + suffix, query))
  const field = input.kind === 'list' ? 'workers' : input.kind === 'runs' ? 'runs' : 'revisions'
  if (!raw || typeof raw !== 'object' || !Array.isArray((raw as Record<string, unknown>)[field])
    || ('next_cursor' in raw && (raw as { next_cursor?: unknown }).next_cursor !== undefined && typeof (raw as { next_cursor?: unknown }).next_cursor !== 'string')) throw new Error('Invalid worker page response')
  return raw as WorkerReadResult
}
export async function mutateWorker(input: WorkerMutation): Promise<WorkerMutationResult> {
  const action = input.action
  let path: string, method = 'POST', body: Record<string, unknown> | undefined
  if (action === 'create') {
    path = '/v3/workers'
    body = { name: required(input.name, 'Name'), instructions: input.instructions ?? '', description: input.description, requested_capabilities: input.requested_capabilities, workspace_requirements: input.workspace_requirements, metadata: input.metadata, idempotency_key: required(input.idempotency_key, 'Idempotency key') }
  } else {
    path = workerPath(input.workerId)
    if ('expected_revision' in input) revision(input.expected_revision)
    if (action === 'update') { method = 'PUT'; body = { ...input.changes, expected_revision: input.expected_revision } }
    else if (action === 'activate') { path += '/activate'; body = { expected_revision: input.expected_revision, local_bindings: input.local_bindings, ...(input.activate === undefined ? {} : { activate: input.activate }) } }
    else if (action === 'pause' || action === 'resume' || action === 'archive') { path += `/${action}`; body = { expected_revision: input.expected_revision } }
    else if (action === 'delete') { method = 'DELETE'; path += `?expected_revision=${input.expected_revision}` }
    else if (action === 'attachAutomation') { path += '/automations'; body = { expected_worker_revision: input.expected_revision, automation: input.automation } }
    else if (action === 'updateAutomation') { path = automationPath(input.workerId, input.automationId); method = 'PUT'; body = { expected_worker_revision: input.expected_revision, automation: input.automation } }
    else if (action === 'removeAutomation') { path = `${automationPath(input.workerId, input.automationId)}?expected_worker_revision=${input.expected_revision}`; method = 'DELETE' }
    else if (action === 'enableAutomation' || action === 'disableAutomation') { path = `${automationPath(input.workerId, input.automationId)}/${action === 'enableAutomation' ? 'enable' : 'disable'}`; body = { expected_worker_revision: input.expected_revision } }
    else if (action === 'direct' || action === 'test') {
      path += action === 'direct' ? '/direct' : '/test'
      if (action === 'direct' && !input.prompt?.trim() && !input.input) throw new Error('Direct request needs a prompt or input')
      body = { prompt: input.prompt, input: input.input, idempotency_key: input.idempotency_key, ...(action === 'test' ? { automation_id: input.automationId } : {}) }
    } else if (input.action === 'cancelRun') { path += `/runs/${encodeURIComponent(required(input.runId, 'Run ID'))}/cancel` }
    else { throw new Error('Unsupported worker action') }
  }
  const raw = await requestJson<WorkerMutationResult>(path, { method, headers: body === undefined ? undefined : { 'Content-Type': 'application/json' }, body: body === undefined ? undefined : JSON.stringify(body) })
  if (action === 'delete') {
    if (!raw || !('deleted' in raw) || raw.deleted !== true || raw.ok !== true) throw new Error('Worker delete was not confirmed')
  } else if (action === 'direct' || action === 'test' || action === 'cancelRun') {
    if (!raw || !('run' in raw) || !raw.run?.id) throw new Error('Worker run was not confirmed')
  } else if (!raw || !('worker' in raw) || !raw.worker?.id) throw new Error('Worker mutation was not confirmed')
  return raw
}
