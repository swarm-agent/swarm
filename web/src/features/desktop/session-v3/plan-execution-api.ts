import { requestJson } from '../../../app/api'
import { normalizeDesktopSessionPlan, type DesktopSessionPlanWire } from '../chat/services/session-plan-record'
import type { DesktopSessionPlanRecord } from '../chat/types/chat'
import { dispatchDesktopV3Cache } from '../state/desktop-v3-cache-store'

export type DesktopPlanExecutionGranularity = 'checkpointed' | 'run_through'
export type DesktopPlanContinuationPolicy = 'automatic' | 'review_each_checkpoint'

export interface DesktopPlanLifecycleResponse {
  ok?: boolean
  session_id?: string
  plan_id?: string
  transition?: string
  status?: string
  summary?: string
  checkpoint_id?: string
  attempt_id?: string
  run_queued?: boolean
  run_intent?: unknown
  plan?: DesktopSessionPlanWire | null
  execution_summary?: unknown
  session?: unknown
}

export interface DesktopPlanEnterInput {
  reason?: string
}

export interface DesktopPlanSubmitInput {
  title?: string
  plan?: string
  document?: Record<string, unknown>
  executionGranularity?: DesktopPlanExecutionGranularity
  continuationPolicy?: DesktopPlanContinuationPolicy
  continueAutomatically?: boolean
}

export interface DesktopPlanApprovalInput {
  executionGranularity?: DesktopPlanExecutionGranularity
  continuationPolicy?: DesktopPlanContinuationPolicy
  continueAutomatically?: boolean
}

export interface DesktopPlanStartAutomaticInput {
  executionGranularity?: DesktopPlanExecutionGranularity
}

export interface DesktopPlanStartCheckpointedInput {
  executionGranularity?: DesktopPlanExecutionGranularity
  continuationPolicy?: DesktopPlanContinuationPolicy
}

export interface DesktopPlanCurrentRunInput {
  planId?: string
}

export interface DesktopPlanCheckpointInput {
  planId?: string
}

export interface DesktopPlanCheckpointAcceptInput {
  planId?: string
  result?: string
  notes?: string
  reviewedAt?: number
}

export async function enterDesktopPlanMode(sessionId: string, input: DesktopPlanEnterInput = {}): Promise<DesktopPlanLifecycleResponse> {
  return postDesktopPlanLifecycle(sessionId, 'enter', {
    reason: trimmed(input.reason),
  })
}

export async function submitDesktopPlanForApproval(sessionId: string, planId: string, input: DesktopPlanSubmitInput = {}): Promise<DesktopPlanLifecycleResponse> {
  return postDesktopPlanLifecycle(sessionId, `plans/${encodePathSegment(planId)}/submit`, {
    title: trimmed(input.title),
    plan: input.plan,
    document: input.document,
    execution_granularity: input.executionGranularity,
    continuation_policy: input.continuationPolicy,
    continue_automatically: input.continueAutomatically,
  })
}

export async function approveDesktopPlan(sessionId: string, planId: string, input: DesktopPlanApprovalInput = {}): Promise<DesktopPlanLifecycleResponse> {
  return postDesktopPlanLifecycle(sessionId, `plans/${encodePathSegment(planId)}/approve`, {
    execution_granularity: input.executionGranularity,
    continuation_policy: input.continuationPolicy,
    continue_automatically: input.continueAutomatically,
  })
}

export async function startDesktopPlanAutomatic(sessionId: string, planId: string, input: DesktopPlanStartAutomaticInput = {}): Promise<DesktopPlanLifecycleResponse> {
  return postDesktopPlanLifecycle(sessionId, `plans/${encodePathSegment(planId)}/start-automatic`, {
    execution_granularity: input.executionGranularity,
  })
}

export async function startDesktopPlanCheckpointed(sessionId: string, planId: string, input: DesktopPlanStartCheckpointedInput = {}): Promise<DesktopPlanLifecycleResponse> {
  return postDesktopPlanLifecycle(sessionId, `plans/${encodePathSegment(planId)}/start-checkpointed`, {
    execution_granularity: input.executionGranularity,
    continuation_policy: input.continuationPolicy,
  })
}

export async function pauseDesktopPlanRun(sessionId: string, input: DesktopPlanCurrentRunInput = {}): Promise<DesktopPlanLifecycleResponse> {
  return postDesktopPlanLifecycle(sessionId, 'runs/current/pause', {
    plan_id: trimmed(input.planId),
  })
}

export async function stopDesktopPlanRun(sessionId: string, input: DesktopPlanCurrentRunInput = {}): Promise<DesktopPlanLifecycleResponse> {
  return postDesktopPlanLifecycle(sessionId, 'runs/current/stop', {
    plan_id: trimmed(input.planId),
  })
}

export async function resumeDesktopPlanAutomatic(sessionId: string, input: DesktopPlanCurrentRunInput = {}): Promise<DesktopPlanLifecycleResponse> {
  return postDesktopPlanLifecycle(sessionId, 'runs/current/resume-automatic', {
    plan_id: trimmed(input.planId),
  })
}

export async function resumeDesktopPlanCheckpointed(sessionId: string, input: DesktopPlanCurrentRunInput = {}): Promise<DesktopPlanLifecycleResponse> {
  return postDesktopPlanLifecycle(sessionId, 'runs/current/resume-checkpointed', {
    plan_id: trimmed(input.planId),
  })
}

export async function startDesktopPlanCheckpoint(sessionId: string, checkpointId: string, input: DesktopPlanCheckpointInput = {}): Promise<DesktopPlanLifecycleResponse> {
  return postDesktopPlanLifecycle(sessionId, `checkpoints/${encodePathSegment(checkpointId)}/start`, {
    plan_id: trimmed(input.planId),
  })
}

export async function continueDesktopPlanCheckpoint(sessionId: string, checkpointId: string, input: DesktopPlanCheckpointInput = {}): Promise<DesktopPlanLifecycleResponse> {
  return postDesktopPlanLifecycle(sessionId, `checkpoints/${encodePathSegment(checkpointId)}/continue`, {
    plan_id: trimmed(input.planId),
  })
}

export async function acceptDesktopPlanCheckpoint(sessionId: string, checkpointId: string, input: DesktopPlanCheckpointAcceptInput = {}): Promise<DesktopPlanLifecycleResponse> {
  return postDesktopPlanLifecycle(sessionId, `checkpoints/${encodePathSegment(checkpointId)}/accept`, {
    plan_id: trimmed(input.planId),
    result: input.result,
    notes: input.notes,
    reviewed_at: input.reviewedAt,
  })
}

export async function restartDesktopPlanCheckpoint(sessionId: string, checkpointId: string, input: DesktopPlanCheckpointInput = {}): Promise<DesktopPlanLifecycleResponse> {
  return postDesktopPlanLifecycle(sessionId, `checkpoints/${encodePathSegment(checkpointId)}/restart`, {
    plan_id: trimmed(input.planId),
  })
}

export async function rewindDesktopPlanCheckpoint(sessionId: string, checkpointId: string, input: DesktopPlanCheckpointInput = {}): Promise<DesktopPlanLifecycleResponse> {
  return postDesktopPlanLifecycle(sessionId, `checkpoints/${encodePathSegment(checkpointId)}/rewind`, {
    plan_id: trimmed(input.planId),
  })
}

async function postDesktopPlanLifecycle(sessionId: string, path: string, body: Record<string, unknown> = {}): Promise<DesktopPlanLifecycleResponse> {
  const normalizedSessionId = sessionId.trim()
  if (!normalizedSessionId) throw new Error('Desktop plan lifecycle request requires session_id')
  const response = await requestJson<DesktopPlanLifecycleResponse>(
    `/v3/sessions/${encodeURIComponent(normalizedSessionId)}/plan-mode/${path}`,
    {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    },
  )
  const plan = response.plan ? normalizeDesktopSessionPlan(response.plan) : null
  if (plan) applyDesktopPlanLifecycleResult(normalizedSessionId, plan)
  return response
}

function encodePathSegment(value: string): string {
  const trimmedValue = value.trim()
  if (!trimmedValue) throw new Error('Desktop plan lifecycle request requires a path id')
  return encodeURIComponent(trimmedValue)
}

function trimmed(value: string | undefined): string | undefined {
  const normalized = value?.trim() ?? ''
  return normalized || undefined
}

function applyDesktopPlanLifecycleResult(sessionId: string, plan: DesktopSessionPlanRecord): void {
  dispatchDesktopV3Cache({
    type: 'planSnapshot.apply',
    sessionId,
    hasActivePlan: true,
    activePlan: plan,
    planRevisions: [],
  })
}
