import { requestJson } from '../../../app/api'
import { normalizeDesktopSessionPlan, type DesktopSessionPlanWire } from '../chat/services/session-plan-record'
import type { DesktopSessionPlanRecord } from '../chat/types/chat'
import { dispatchDesktopV3Cache } from '../state/desktop-v3-cache-store'

export type DesktopPlanExecutionAction =
  | 'approve_and_start'
  | 'start_checkpoint'
  | 'continue_checkpoint'
  | 'accept_checkpoint'
  | 'accept_and_continue'
  | 'restart_checkpoint'
  | 'rewind_to_checkpoint'
  | 'set_automatic_mode'

export interface DesktopPlanCheckpointRunRequest {
  plan_checkpoint_context?: {
    plan_id?: string
    checkpoint_id?: string
    attempt_id?: string
  }
}

export interface DesktopPlanExecutionResponse {
  ok?: boolean
  session_id?: string
  plan_id?: string
  action?: string
  status?: string
  summary?: string
  next_action?: string
  checkpoint_id?: string
  run_request?: DesktopPlanCheckpointRunRequest
  plan?: DesktopSessionPlanWire | null
  execution_summary?: unknown
}

export interface DesktopPlanExecutionControlInput {
  action: DesktopPlanExecutionAction
  planId?: string
  checkpointId?: string
  executionGranularity?: 'checkpointed' | 'run_through'
  continueAutomatically?: boolean
  continuationPolicy?: 'automatic' | 'review_each_checkpoint'
  result?: string
  notes?: string
}

export async function executeDesktopPlanAction(
  sessionId: string,
  input: DesktopPlanExecutionControlInput,
): Promise<DesktopPlanExecutionResponse> {
  const normalizedSessionId = sessionId.trim()
  if (!normalizedSessionId) throw new Error('Desktop plan execution action requires session_id')
  const response = await requestJson<DesktopPlanExecutionResponse>(
    `/v3/sessions/${encodeURIComponent(normalizedSessionId)}/plans/execution`,
    {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        action: input.action,
        plan_id: input.planId?.trim() || undefined,
        checkpoint_id: input.checkpointId?.trim() || undefined,
        execution_granularity: input.executionGranularity,
        continue_automatically: input.continueAutomatically,
        continuation_policy: input.continuationPolicy,
        result: input.result,
        notes: input.notes,
      }),
    },
  )
  const plan = response.plan ? normalizeDesktopSessionPlan(response.plan) : null
  if (plan) applyDesktopPlanExecutionResult(normalizedSessionId, plan)
  return response
}

export async function startDesktopPlanCheckpointRun(
  sessionId: string,
  response: DesktopPlanExecutionResponse,
): Promise<Record<string, unknown> | null> {
  const normalizedSessionId = sessionId.trim()
  if (!normalizedSessionId) throw new Error('Desktop plan checkpoint run requires session_id')
  if (response.next_action !== 'run_checkpoint_with_fresh_context') return null
  const runRequest = response.run_request?.plan_checkpoint_context
  const planId = runRequest?.plan_id?.trim() || response.plan_id?.trim() || ''
  const checkpointId = runRequest?.checkpoint_id?.trim() || response.checkpoint_id?.trim() || ''
  if (!planId || !checkpointId) {
    throw new Error('Fresh checkpoint run request is missing plan_id or checkpoint_id')
  }
  return requestJson<Record<string, unknown>>(
    `/v3/sessions/${encodeURIComponent(normalizedSessionId)}/run/stream`,
    {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        type: 'run.start',
        plan_checkpoint_context: {
          plan_id: planId,
          checkpoint_id: checkpointId,
          attempt_id: runRequest?.attempt_id?.trim() || undefined,
        },
      }),
    },
  )
}

export async function executeDesktopPlanActionAndStartRun(
  sessionId: string,
  input: DesktopPlanExecutionControlInput,
): Promise<DesktopPlanExecutionResponse> {
  const response = await executeDesktopPlanAction(sessionId, input)
  await startDesktopPlanCheckpointRun(sessionId, response)
  return response
}

function applyDesktopPlanExecutionResult(sessionId: string, plan: DesktopSessionPlanRecord): void {
  dispatchDesktopV3Cache({
    type: 'planSnapshot.apply',
    sessionId,
    activePlan: plan,
    planRevisions: [],
  })
}
