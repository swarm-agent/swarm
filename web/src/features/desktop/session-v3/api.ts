import { requestJson } from '../../../app/api'
import type { ModelProfileChoice, ResolvedSessionPreference } from '../chat/types/chat'
import { desktopV3ModelProfileChoiceWire } from './write-api'
import type { DesktopSessionMode, FollowupCheckpointPolicyDefault } from '../settings/swarm/types/swarm-settings'
import { normalizeFollowupCheckpointPolicyDefault, normalizeSessionMode } from '../settings/swarm/types/swarm-settings'
import type {
  SessionV3AgentMutationResponseWire,
  SessionV3MetadataMutationResponseWire,
  SessionV3ModeMutationResponseWire,
  SessionV3PermissionResolveRequestWire,
  SessionV3PermissionResolveResponseWire,
  SessionV3PermissionsResolveAllResponseWire,
  SessionV3PreferenceResponseWire,
  SessionV3RunStopResponseWire,
  SessionV3TitleMutationResponseWire,
} from './types'
import type { SessionSettingsMutationResponse } from '../state/desktop-v3-cache-types'

export const DESKTOP_V3_SIDEBAR_PINNED_METADATA_KEY = 'swarm_v3_desktop_sidebar_pinned' as const

function mapPreferenceResponse(response: SessionV3PreferenceResponseWire): ResolvedSessionPreference {
  return {
    preference: mapPreferenceWire(response.preference),
    contextWindow: typeof response.context_window === 'number' ? response.context_window : 0,
    maxOutputTokens: typeof response.max_output_tokens === 'number' ? response.max_output_tokens : 0,
  }
}

function mapPreferenceWire(value: unknown): ResolvedSessionPreference['preference'] {
  const record = value && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : {}
  return {
    provider: String(record.provider ?? '').trim(),
    model: String(record.model ?? '').trim(),
    thinking: String(record.thinking ?? '').trim(),
    serviceTier: String(record.serviceTier ?? record.service_tier ?? '').trim(),
    contextMode: String(record.contextMode ?? record.context_mode ?? '').trim(),
    updatedAt: typeof record.updatedAt === 'number' ? record.updatedAt : typeof record.updated_at === 'number' ? record.updated_at : 0,
  }
}

function mutationRecord(value: unknown): SessionSettingsMutationResponse['mutation'] {
  return value && typeof value === 'object' && !Array.isArray(value)
    ? value as SessionSettingsMutationResponse['mutation']
    : undefined
}

export function sessionV3ModeSettingsMutationResponse(
  response: SessionV3ModeMutationResponseWire,
  fallbackSessionId: string,
  fallbackMode: DesktopSessionMode,
): SessionSettingsMutationResponse {
  const mode = normalizeSessionMode(response.mode ?? fallbackMode)
  return {
    ok: response.ok ?? true,
    session_id: response.session_id ?? fallbackSessionId,
    mode,
    preference: response.preference,
    context_window: response.context_window,
    max_output_tokens: response.max_output_tokens,
    agent_model_policy: response.agent_model_policy,
    turn_usage: response.turn_usage,
    usage_summary: response.usage_summary,
    mutation: mutationRecord(response.mutation),
    realtime_outbox: response.realtime_outbox,
  }
}

export function sessionV3AgentSettingsMutationResponse(
  response: SessionV3AgentMutationResponseWire,
  fallbackSessionId: string,
): SessionSettingsMutationResponse {
  return {
    ok: response.ok ?? true,
    session_id: response.session_id ?? fallbackSessionId,
    metadata: response.metadata,
    agent: response.agent,
    agent_model_policy: response.agent_model_policy,
    turn_usage: response.turn_usage,
    usage_summary: response.usage_summary,
    mutation: mutationRecord(response.mutation),
    realtime_outbox: response.realtime_outbox,
  }
}

export function sessionV3PreferenceSettingsMutationResponse(
  response: SessionV3PreferenceResponseWire,
  fallbackSessionId: string,
): SessionSettingsMutationResponse {
  const resolved = mapPreferenceResponse(response)
  return {
    ok: response.ok ?? true,
    session_id: response.session_id ?? fallbackSessionId,
    preference: resolved.preference,
    context_window: resolved.contextWindow,
    max_output_tokens: resolved.maxOutputTokens,
    agent_model_policy: response.agent_model_policy,
    turn_usage: response.turn_usage,
    usage_summary: response.usage_summary,
    mutation: mutationRecord(response.mutation),
    realtime_outbox: response.realtime_outbox,
  }
}

export function sessionV3MetadataSettingsMutationResponse(
  response: SessionV3MetadataMutationResponseWire,
  fallbackSessionId: string,
): SessionSettingsMutationResponse {
  return {
    ok: response.ok ?? true,
    session_id: response.session_id ?? fallbackSessionId,
    metadata: response.metadata,
    turn_usage: response.turn_usage,
    usage_summary: response.usage_summary,
    mutation: mutationRecord(response.mutation),
    realtime_outbox: response.realtime_outbox,
  }
}

export async function fetchSessionV3Preference(sessionId: string, signal?: AbortSignal): Promise<ResolvedSessionPreference> {
  const normalizedSessionId = sessionId.trim()
  if (!normalizedSessionId) throw new Error('Desktop V3 preference fetch requires session_id')
  const response = await requestJson<SessionV3PreferenceResponseWire>(
    `/v3/sessions/${encodeURIComponent(normalizedSessionId)}/preference`,
    { method: 'GET', signal },
  )
  return mapPreferenceResponse(response)
}

export async function updateSessionV3Preference(
  sessionId: string,
  input: Partial<ResolvedSessionPreference['preference']>,
): Promise<SessionV3PreferenceResponseWire> {
  const normalizedSessionId = sessionId.trim()
  if (!normalizedSessionId) throw new Error('Desktop V3 preference update requires session_id')

  const body: Record<string, string> = {}
  const provider = input.provider?.trim()
  const model = input.model?.trim()
  const thinking = input.thinking?.trim()
  if (provider) body.provider = provider
  if (model) body.model = model
  if (thinking) body.thinking = thinking
  if (input.serviceTier !== undefined) body.service_tier = input.serviceTier.trim()
  if (input.contextMode !== undefined) body.context_mode = input.contextMode.trim()
  if (Object.keys(body).length === 0) {
    throw new Error('Desktop V3 preference update requires a non-empty preference change')
  }

  const response = await requestJson<SessionV3PreferenceResponseWire>(
    `/v3/sessions/${encodeURIComponent(normalizedSessionId)}/preference`,
    {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    },
  )
  return response
}

export interface SessionV3ModelProfileMutationResponseWire {
  ok?: boolean
  session_id?: string
  metadata?: Record<string, unknown>
  model_profile?: unknown
  agent_model_policy?: unknown
  mutation?: unknown
  realtime_outbox?: unknown
}

export function sessionV3ModelProfileSettingsMutationResponse(
  response: SessionV3ModelProfileMutationResponseWire,
  fallbackSessionId: string,
): SessionSettingsMutationResponse {
  return {
    ok: response.ok ?? true,
    session_id: response.session_id ?? fallbackSessionId,
    agent_model_policy: response.agent_model_policy,
    metadata: response.metadata,
    mutation: mutationRecord(response.mutation),
    realtime_outbox: response.realtime_outbox,
    model_profile: response.model_profile,
  }
}

export async function updateSessionV3ModelProfile(
  sessionId: string,
  choice: ModelProfileChoice,
): Promise<SessionV3ModelProfileMutationResponseWire> {
  const normalizedSessionId = sessionId.trim()
  if (!normalizedSessionId) throw new Error('Desktop V3 model profile update requires session_id')
  return requestJson(`/v3/sessions/${encodeURIComponent(normalizedSessionId)}/model-profile`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ client_request_id: `desktop-model-profile:${crypto.randomUUID()}`, choice: desktopV3ModelProfileChoiceWire(choice) }),
  })
}

export async function updateSessionV3Title(
  sessionId: string,
  title: string,
  clientRequestId: string,
): Promise<SessionV3TitleMutationResponseWire> {
  const normalizedSessionId = sessionId.trim()
  const normalizedTitle = title.trim()
  const normalizedRequestId = clientRequestId.trim()
  if (!normalizedSessionId) throw new Error('Desktop V3 title update requires session_id')
  if (!normalizedTitle) throw new Error('Desktop V3 title update requires title')
  if (!normalizedRequestId) throw new Error('Desktop V3 title update requires client_request_id')
  return requestJson<SessionV3TitleMutationResponseWire>(`/v3/sessions/${encodeURIComponent(normalizedSessionId)}/title`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ title: normalizedTitle, client_request_id: normalizedRequestId }),
  })
}

export async function updateSessionV3Mode(sessionId: string, mode: DesktopSessionMode): Promise<SessionV3ModeMutationResponseWire> {
  const normalizedSessionId = sessionId.trim()
  if (!normalizedSessionId) throw new Error('Desktop V3 mode update requires session_id')
  const normalizedMode = normalizeSessionMode(mode)
  const response = await requestJson<SessionV3ModeMutationResponseWire>(
    `/v3/sessions/${encodeURIComponent(normalizedSessionId)}/mode`,
    {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ mode: normalizedMode }),
    },
  )
  return response
}

export async function updateSessionV3Agent(sessionId: string, agentName: string): Promise<SessionV3AgentMutationResponseWire> {
  const normalizedSessionId = sessionId.trim()
  const normalizedAgent = agentName.trim()
  if (!normalizedSessionId) throw new Error('Desktop V3 agent update requires session_id')
  if (!normalizedAgent) throw new Error('Desktop V3 agent update requires agent_name')
  return requestJson<SessionV3AgentMutationResponseWire>(`/v3/sessions/${encodeURIComponent(normalizedSessionId)}/agent`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ agent_name: normalizedAgent }),
  })
}

export async function updateSessionV3DesktopSidebarPinned(
  sessionId: string,
  pinned: boolean,
  currentMetadata: Record<string, unknown> = {},
): Promise<SessionV3MetadataMutationResponseWire> {
  const normalizedSessionId = sessionId.trim()
  if (!normalizedSessionId) throw new Error('Desktop V3 sidebar pin update requires session_id')
  return requestJson<SessionV3MetadataMutationResponseWire>(`/v3/sessions/${encodeURIComponent(normalizedSessionId)}/metadata`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      metadata: {
        ...currentMetadata,
        [DESKTOP_V3_SIDEBAR_PINNED_METADATA_KEY]: pinned,
      },
    }),
  })
}

export async function updateAndApplySessionV3DesktopSidebarPinned(
  sessionId: string,
  pinned: boolean,
  currentMetadata: Record<string, unknown> = {},
): Promise<SessionSettingsMutationResponse> {
  const normalizedSessionId = sessionId.trim()
  if (!normalizedSessionId) throw new Error('Desktop V3 sidebar pin update requires session_id')
  const response = await updateSessionV3DesktopSidebarPinned(normalizedSessionId, pinned, currentMetadata)
  const settingsResponse = sessionV3MetadataSettingsMutationResponse(response, normalizedSessionId)
  const { dispatchDesktopV3Cache } = await import('../state/desktop-v3-cache-store')
  dispatchDesktopV3Cache({
    type: 'mutation.sessionSettingsResult',
    raw: settingsResponse,
  })
  return settingsResponse
}

export async function requestSessionV3PlanFollowupCheckpoint(
  sessionId: string,
  input: {
    planId?: string
    changeRequest: string
    checkpointTitle?: string
    tasks?: string[]
    acceptanceCriteria?: string[]
    sourceMessageId?: string
    followupCheckpointPolicy?: string
  },
): Promise<unknown> {
  const normalizedSessionId = sessionId.trim()
  const changeRequest = input.changeRequest.trim()
  if (!normalizedSessionId) throw new Error('Desktop V3 plan follow-up requires session_id')
  if (!changeRequest) throw new Error('Desktop V3 plan follow-up requires change_request')
  return requestSessionV3PlanLifecycle(normalizedSessionId, 'request-followup-checkpoint', {
    plan_id: input.planId?.trim() || undefined,
    change_request: changeRequest,
    checkpoint_title: input.checkpointTitle?.trim() || undefined,
    tasks: input.tasks,
    acceptance_criteria: input.acceptanceCriteria,
    source_message_id: input.sourceMessageId?.trim() || undefined,
    followup_checkpoint_policy: input.followupCheckpointPolicy?.trim() || undefined,
  })
}

export async function requestSessionV3PlanRevision(
  sessionId: string,
  input: { planId?: string; title?: string; plan?: string; document?: unknown; reason?: string },
): Promise<unknown> {
  const normalizedSessionId = sessionId.trim()
  if (!normalizedSessionId) throw new Error('Desktop V3 plan revision requires session_id')
  return requestSessionV3PlanLifecycle(normalizedSessionId, 'request-plan-revision', {
    plan_id: input.planId?.trim() || undefined,
    title: input.title?.trim() || undefined,
    plan: input.plan?.trim() || undefined,
    document: input.document,
    reason: input.reason?.trim() || undefined,
  })
}

export async function requestSessionV3NewPlan(
  sessionId: string,
  input: { title?: string; plan?: string; document?: unknown; reason?: string },
): Promise<unknown> {
  const normalizedSessionId = sessionId.trim()
  if (!normalizedSessionId) throw new Error('Desktop V3 new plan request requires session_id')
  return requestSessionV3PlanLifecycle(normalizedSessionId, 'request-new-plan', {
    title: input.title?.trim() || undefined,
    plan: input.plan?.trim() || undefined,
    document: input.document,
    reason: input.reason?.trim() || undefined,
  })
}

export interface SessionV3PlanFollowupPolicyResponseWire {
  ok?: boolean
  session_id?: string
  plan_id?: string
  plan?: unknown
  transition?: string
  change_type?: string
  policy_effective?: string
  approval_required?: boolean
  run_queued?: boolean
  execution_summary?: unknown
  session?: unknown
}

export async function setSessionV3PlanFollowupPolicy(
  sessionId: string,
  input: { planId?: string; followupCheckpointPolicy: string; reason?: string },
): Promise<SessionV3PlanFollowupPolicyResponseWire> {
  const normalizedSessionId = sessionId.trim()
  const followupCheckpointPolicy = normalizePlanFollowupPolicyOverride(input.followupCheckpointPolicy)
  if (!normalizedSessionId) throw new Error('Desktop V3 follow-up policy requires session_id')
  return requestSessionV3PlanLifecycle<SessionV3PlanFollowupPolicyResponseWire>(normalizedSessionId, 'followup-policy', {
    plan_id: input.planId?.trim() || undefined,
    followup_checkpoint_policy: followupCheckpointPolicy,
    reason: input.reason?.trim() || undefined,
  })
}

function normalizePlanFollowupPolicyOverride(value: string): '' | FollowupCheckpointPolicyDefault {
  const trimmed = value.trim()
  return trimmed ? normalizeFollowupCheckpointPolicyDefault(trimmed) : ''
}

export function desktopPlanFollowupPolicyResponsePlan(response: SessionV3PlanFollowupPolicyResponseWire): unknown {
  const plan = objectRecord(response.plan)
  if (plan) return plan
  const session = objectRecord(response.session)
  return objectRecord(session?.active_plan) ?? objectRecord(session?.activePlan) ?? null
}

function requestSessionV3PlanLifecycle<T = unknown>(sessionId: string, action: string, body: Record<string, unknown>): Promise<T> {
  return requestJson<T>(`/v3/sessions/${encodeURIComponent(sessionId)}/plan-mode/lifecycle/${action}`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
}

function objectRecord(value: unknown): Record<string, unknown> | null {
  return value && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : null
}

export async function stopSubagentSessionV3Run(sessionId: string): Promise<SessionV3RunStopResponseWire> {
  const normalizedSessionId = sessionId.trim()
  if (!normalizedSessionId) throw new Error('Desktop V3 subagent stop requires session_id')
  return requestJson('/v3/subagents:stop', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ session_id: normalizedSessionId }),
  })
}

export async function stopSessionV3Run(
  sessionId: string,
  input: { runId: string; targetSwarmId: string },
): Promise<SessionV3RunStopResponseWire> {
  const normalizedSessionId = sessionId.trim()
  const runId = input.runId.trim()
  const targetSwarmId = input.targetSwarmId.trim()
  if (!normalizedSessionId) throw new Error('Desktop V3 stop requires session_id')
  if (!runId) throw new Error('Desktop V3 stop requires run_id')
  if (!targetSwarmId) throw new Error('Desktop V3 stop requires target_swarm_id')
  return requestJson(`/v3/sessions/${encodeURIComponent(normalizedSessionId)}/run/stop`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      type: 'run.stop',
      run_id: runId,
      target_swarm_id: targetSwarmId,
    }),
  })
}

export async function resolveSessionV3Permission(
  sessionId: string,
  permissionId: string,
  input: SessionV3PermissionResolveRequestWire,
): Promise<SessionV3PermissionResolveResponseWire> {
  const normalizedSessionId = sessionId.trim()
  const normalizedPermissionId = permissionId.trim()
  if (!normalizedSessionId) throw new Error('Desktop V3 permission resolve requires session_id')
  if (!normalizedPermissionId) throw new Error('Desktop V3 permission resolve requires permission_id')
  return requestJson(`/v3/sessions/${encodeURIComponent(normalizedSessionId)}/permissions/${encodeURIComponent(normalizedPermissionId)}/resolve`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(input),
  })
}

export async function resolveAllSessionV3Permissions(
  sessionId: string,
  input: SessionV3PermissionResolveRequestWire,
): Promise<SessionV3PermissionsResolveAllResponseWire> {
  const normalizedSessionId = sessionId.trim()
  if (!normalizedSessionId) throw new Error('Desktop V3 permission resolve-all requires session_id')
  return requestJson(`/v3/sessions/${encodeURIComponent(normalizedSessionId)}/permissions/resolve_all`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(input),
  })
}
