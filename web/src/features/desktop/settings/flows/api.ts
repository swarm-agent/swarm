import { requestJson } from '../../../../app/api'
import { fetchSwarmTargets, type SwarmTarget, type SwarmTargetsResponse } from '../../swarm/api/swarm-targets'
import { listWorkspaces } from '../../../workspaces/launcher/queries/list-workspaces'
import type { WorkspaceEntry } from '../../../workspaces/launcher/types/workspace'
import type { AgentProfileRecord } from '../../../desktop/chat/types/chat'

export type FlowAssignmentStatus = 'accepted' | 'duplicate' | 'rejected' | 'out_of_order' | 'pending_sync' | 'target_offline' | 'target_unusable'
export type FlowRunStatus = 'claimed' | 'running' | 'success' | 'skipped' | 'review' | 'failed'

export interface FlowTargetSelection {
  swarm_id?: string
  kind?: string
  deployment_id?: string
  name?: string
}

export type FlowSwarmTarget = SwarmTarget
export type FlowWorkspaceEntry = WorkspaceEntry
export type FlowAgentProfile = AgentProfileRecord

export interface FlowAgentProfileDetail {
  name: string
  mode?: string
  description?: string
  enabled?: boolean
}

export interface FlowAgentSelection {
  profile_name: string
  profile_mode?: string
}

export interface FlowWorkspaceContext {
  workspace_path?: string
  host_workspace_path?: string
  runtime_workspace_path?: string
  cwd?: string
  worktree_mode?: string
}

export interface FlowScheduleSpec {
  cadence: string
  time?: string
  times?: string[]
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

export interface FlowDefinitionRecord {
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
  next_due_at?: string
  created_at?: string
  updated_at?: string
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

export interface FlowOutboxCommandRecord {
  command_id: string
  flow_id: string
  revision: number
  target_swarm_id: string
  status: string
  attempt_count?: number
  last_error?: string
}

export interface FlowWorkspaceDetail {
  workspace_path: string
  host_workspace_path?: string
  runtime_workspace_path?: string
  cwd?: string
  worktree_mode?: string
}

export interface FlowSummaryRecord {
  definition: FlowDefinitionRecord
  target_detail?: FlowSwarmTarget | null
  agent_detail?: FlowAgentProfileDetail | null
  workspace_detail?: FlowWorkspaceDetail | null
  assignment_statuses?: FlowAssignmentStatusRecord[]
  last_run?: FlowRunSummaryRecord | null
  history_count: number
  history?: FlowRunSummaryRecord[]
  outbox?: FlowOutboxCommandRecord[]
}

export interface FlowDetailRecord extends FlowSummaryRecord {
  assignment_statuses: FlowAssignmentStatusRecord[]
  history: FlowRunSummaryRecord[]
  outbox: FlowOutboxCommandRecord[]
}

export interface FlowDetailResponse extends FlowDetailRecord {
  ok: boolean
}

export interface FlowListResponse {
  ok: boolean
  flows: FlowSummaryRecord[]
}

export interface FlowMutationResponse {
  ok: boolean
  flow: FlowDetailRecord
  result?: {
    pending_sync?: boolean
    delivered?: boolean
    outbox?: FlowOutboxCommandRecord
    assignment_state?: FlowAssignmentStatusRecord
    ack?: {
      status?: string
      reason?: string
    }
  }
  run?: {
    command_id: string
    pending_sync: boolean
    reason?: string
  }
}

export interface FlowHistoryResponse {
  ok: boolean
  flow_id: string
  history: FlowRunSummaryRecord[]
}

export interface FlowStatusResponse {
  ok: boolean
  flow_id: string
  assignment_statuses: FlowAssignmentStatusRecord[]
  outbox: FlowOutboxCommandRecord[]
  history: FlowRunSummaryRecord[]
}

export interface FlowRunNowResponse {
  ok: boolean
  flow: FlowDetailRecord
  result?: {
    pending_sync?: boolean
    delivered?: boolean
    outbox?: FlowOutboxCommandRecord
    assignment_state?: FlowAssignmentStatusRecord
    ack?: {
      status?: string
      reason?: string
    }
  }
  run?: {
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
  const response = await requestJson<FlowListResponse>('/v3/flows?limit=200', {
    cache: 'no-store',
    signal,
  })
  return Array.isArray(response.flows) ? response.flows : []
}

export async function fetchFlow(flowID: string, signal?: AbortSignal): Promise<FlowDetailRecord> {
  return requestJson<FlowDetailResponse>(`/v3/flows/${encodeURIComponent(flowID)}`, {
    cache: 'no-store',
    signal,
  })
}

export async function fetchFlowSwarmTargets(): Promise<FlowSwarmTarget[]> {
  const response: SwarmTargetsResponse = await fetchSwarmTargets()
  return Array.isArray(response.targets) ? response.targets : []
}

export async function fetchFlowWorkspaces(): Promise<FlowWorkspaceEntry[]> {
  return listWorkspaces(200)
}

export async function createFlow(input: CreateFlowInput): Promise<FlowDetailRecord> {
  const response = await requestJson<FlowMutationResponse>('/v3/flows', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify(input),
  })
  return response.flow
}

export async function deleteFlow(flowID: string): Promise<void> {
  await requestJson(`/v3/flows/${encodeURIComponent(flowID)}`, {
    method: 'DELETE',
  })
}

export async function runFlowNow(flowID: string): Promise<FlowRunNowResponse> {
  return requestJson<FlowRunNowResponse>(`/v3/flows/${encodeURIComponent(flowID)}/run-now`, {
    method: 'POST',
  })
}

export const flowsQueryKey = ['flows'] as const
