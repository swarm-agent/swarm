import { apiFetch } from '../../../app/api'
import type { PlanRuntimeDeltaWire, PlanRuntimeHydrationWire } from './plan-runtime'

export interface PlanRuntimeHydrateResponse {
  ok: true
  plan_runtime: PlanRuntimeHydrationWire
}

export interface PlanRuntimeCommandInput {
  session_id: string
  plan_id: string
  action: string
  definition_revision: number
  expected_execution_seq: number
  client_request_id: string
  checkpoint_id?: string
  subtask_ids?: string[]
  complete_checkpoint?: boolean
  next_subtask_id?: string
  attempt_id?: string
  outcome?: string
  evidence_ref?: string
  next_action?: string
  run_id?: string
  epoch_id?: string
  run_session_id?: string
  parent_session_id?: string
}

export interface PlanRuntimeCommandResponse {
  ok: true
  receipt: Record<string, unknown>
}

export interface PlanRuntimeReplayResponse {
  ok: true
  protocol: 'v3.plan_execution'
  protocol_version: 1
  plan_id: string
  records: PlanRuntimeDeltaWire[]
  next_after_execution_seq: number
  has_more: boolean
  encoded_bytes: number
}

export async function hydratePlanRuntime(sessionId: string, planId: string, definitionRevision: number): Promise<PlanRuntimeHydrationWire> {
  const response = await postPlanRuntime('/v3/plan-runtime:hydrate', {
    session_id: requireValue(sessionId, 'session_id'),
    plan_id: requireValue(planId, 'plan_id'),
    definition_revision: definitionRevision,
  }) as PlanRuntimeHydrateResponse
  return response.plan_runtime
}

export async function commandPlanRuntime(input: PlanRuntimeCommandInput): Promise<PlanRuntimeCommandResponse> {
  return await postPlanRuntime('/v3/plan-runtime:command', {
    ...input,
    session_id: requireValue(input.session_id, 'session_id'),
    plan_id: requireValue(input.plan_id, 'plan_id'),
    client_request_id: requireValue(input.client_request_id, 'client_request_id'),
  }) as PlanRuntimeCommandResponse
}

export async function replayPlanRuntime(sessionId: string, planId: string, afterExecutionSeq: number, limit = 256): Promise<PlanRuntimeReplayResponse> {
  return await postPlanRuntime('/v3/plan-runtime:replay', {
    session_id: requireValue(sessionId, 'session_id'),
    plan_id: requireValue(planId, 'plan_id'),
    after_execution_seq: afterExecutionSeq,
    limit,
  }) as PlanRuntimeReplayResponse
}

async function postPlanRuntime(path: string, body: Record<string, unknown>): Promise<unknown> {
  const response = await apiFetch(path, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
  const payload = await response.json() as { error?: string }
  if (!response.ok) throw new Error(payload.error || `plan runtime request failed (${response.status})`)
  return payload
}

function requireValue(value: string, name: string): string {
  const normalized = value.trim()
  if (!normalized) throw new Error(`plan runtime ${name} is required`)
  return normalized
}
