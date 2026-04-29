import { requestJson } from '../../../../app/api'

export type FlowAssignmentStatus = 'accepted' | 'duplicate' | 'rejected' | 'out_of_order' | 'pending_sync' | 'target_offline' | 'target_unusable'
export type FlowRunStatus = 'claimed' | 'running' | 'success' | 'skipped' | 'review' | 'failed'

export interface FlowTargetSelection {
  swarm_id?: string
  kind?: string
  deployment_id?: string
  name?: string
}

export interface FlowAgentSelection {
  target_kind: string
  target_name: string
}

export interface FlowWorkspaceContext {
  workspace_path?: string
  cwd?: string
  worktree_mode?: string
}

export interface FlowScheduleSpec {
  cadence: string
  time?: string
  weekday?: string
  month_day?: number
  timezone: string
}

export interface FlowCatchUpPolicy {
  mode: string
  max_catch_up?: number
}

export interface FlowTaskStep {
  id: string
  title: string
  detail?: string
  action: string
}

export interface FlowPromptIntent {
  prompt: string
  mode?: string
  tasks?: FlowTaskStep[]
}

export interface FlowAssignment {
  flow_id: string
  revision: number
  name: string
  enabled: boolean
  target: FlowTargetSelection
  agent: FlowAgentSelection
  workspace: FlowWorkspaceContext
  schedule: FlowScheduleSpec
  catch_up_policy: FlowCatchUpPolicy
  intent: FlowPromptIntent
}

export interface FlowDefinitionRecord {
  flow_id: string
  revision: number
  assignment: FlowAssignment
  next_due_at?: string
  created_at: string
  updated_at: string
  deleted_at?: string
}

export interface FlowAssignmentStatusRecord {
  flow_id: string
  target_swarm_id: string
  target: FlowTargetSelection
  command_id?: string
  desired_revision: number
  accepted_revision?: number
  status: FlowAssignmentStatus
  reason?: string
  pending_sync: boolean
  target_clock?: string
  updated_at: string
}

export interface FlowOutboxCommandRecord {
  command_id: string
  flow_id: string
  revision: number
  target_swarm_id: string
  target: FlowTargetSelection
  command: unknown
  status: string
  attempt_count?: number
  next_attempt_at?: string
  last_attempt_at?: string
  last_error?: string
  created_at: string
  updated_at: string
}

export interface FlowRunSummaryRecord {
  run_id: string
  flow_id: string
  revision: number
  scheduled_at: string
  started_at: string
  finished_at?: string
  duration_ms?: number
  status: FlowRunStatus
  summary?: string
  session_id?: string
  target_swarm_id?: string
  reported_at?: string
  report_attempt_count?: number
  next_report_at?: string
  report_error?: string
}

export interface FlowSummaryRecord {
  definition: FlowDefinitionRecord
  assignment_statuses?: FlowAssignmentStatusRecord[]
  last_run?: FlowRunSummaryRecord | null
  history_count: number
}

export interface FlowDetailRecord {
  definition: FlowDefinitionRecord
  assignment_statuses: FlowAssignmentStatusRecord[]
  outbox: FlowOutboxCommandRecord[]
  history: FlowRunSummaryRecord[]
}

export interface FlowListResponse {
  ok: boolean
  flows: FlowSummaryRecord[]
}

export interface FlowCreateResponse {
  ok: boolean
  flow: FlowDetailRecord
}

export interface FlowRunNowResponse {
  ok: boolean
  run: {
    command_id: string
    pending_sync: boolean
    reason?: string
  }
}

export interface CreateFlowInput {
  flow_id?: string
  name: string
  enabled?: boolean
  target: FlowTargetSelection
  agent: FlowAgentSelection
  workspace: FlowWorkspaceContext
  schedule: FlowScheduleSpec
  catch_up_policy: FlowCatchUpPolicy
  intent: FlowPromptIntent
}

export async function fetchFlows(signal?: AbortSignal): Promise<FlowSummaryRecord[]> {
  const response = await requestJson<FlowListResponse>('/v1/flows?limit=200', {
    cache: 'no-store',
    signal,
  })
  return Array.isArray(response.flows) ? response.flows : []
}

export async function createFlow(input: CreateFlowInput): Promise<FlowDetailRecord> {
  const response = await requestJson<FlowCreateResponse>('/v1/flows', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify(input),
  })
  return response.flow
}

export async function deleteFlow(flowID: string): Promise<void> {
  await requestJson(`/v1/flows/${encodeURIComponent(flowID)}`, {
    method: 'DELETE',
  })
}

export async function runFlowNow(flowID: string): Promise<FlowRunNowResponse> {
  return requestJson<FlowRunNowResponse>(`/v1/flows/${encodeURIComponent(flowID)}/run-now`, {
    method: 'POST',
  })
}

export const flowsQueryKey = ['flows'] as const
