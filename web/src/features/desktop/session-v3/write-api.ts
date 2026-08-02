import { apiFetch, readErrorMessage, requestJson } from '../../../app/api'
import type { ModelProfileChoice, ModelProfileSelectionRecord } from '../chat/types/chat'
import type { DesktopSessionMode } from '../settings/swarm/types/swarm-settings'

import type {
  DesktopV3MediaCapability,
  DesktopV3MediaReference,
  MessageMutationConflictResponse,
  MessageSnapshot,
  SessionCreateMutationResponse,
  SessionMessageMutationResponse,
  SessionMutationErrorResponse,
  SessionMutationResult,
  SessionSnapshot,
  V3SessionProjection,
} from '../state/desktop-v3-cache-types'

export interface DesktopV3CreateSessionRequest {
  session_id: string
  client_request_id: string
  title?: string
  workspace_path: string
  workspace_name?: string
  workspace_binding_id: string
  swarm_id: string
  target_kind: 'host' | 'self'
  target_relationship: 'self'
  host_workspace_path?: string
  runtime_workspace_path?: string
  mode?: DesktopSessionMode
  agent_name?: string
  metadata?: Record<string, unknown>
  preference?: {
    provider?: string
    model?: string
    thinking?: string
    service_tier?: string
    context_mode?: string
  }
  model_profile?: DesktopV3ModelProfileChoiceWire
  worktree_mode?: string
  worktree_use_current_branch?: boolean
  worktree_base_branch?: string
  worktree_branch_name?: string
  worktree_existing_path?: string
}

export interface DesktopV3ModelProfileSelectionWire {
  provider: string
  model: string
  thinking: string
  service_tier?: string
  context_mode?: string
}

export interface DesktopV3ModelProfileChoiceWire {
  use_account_default?: true
  saved_profile_id?: string
  temporary?: DesktopV3ModelProfileSelectionWire & {
    name?: string
  }
  use_agent_default?: true
}

function modelProfileSelectionWire(value: ModelProfileSelectionRecord): DesktopV3ModelProfileSelectionWire {
  return {
    provider: value.provider.trim(), model: value.model.trim(), thinking: value.thinking.trim(),
    service_tier: value.serviceTier.trim() || undefined, context_mode: value.contextMode.trim() || undefined,
  }
}

export function desktopV3ModelProfileChoiceWire(choice: ModelProfileChoice): DesktopV3ModelProfileChoiceWire {
  switch (choice.kind) {
    case 'account-default': return { use_account_default: true }
    case 'saved': return { saved_profile_id: choice.profileId.trim() }
    case 'agent-default': return { use_agent_default: true }
    case 'temporary': return {
      temporary: {
        name: choice.profile.name.trim() || 'Temporary',
        ...modelProfileSelectionWire(choice.profile),
      },
    }
  }
}

export interface DesktopV3RoutedSessionMediaRequest {
  staging_id: string
  modality?: string
  file_type?: string
}

export interface DesktopV3RoutedSessionStartRequest {
  input: string
  client_request_id: string
  idempotency_key?: string
  agent_name?: string
  metadata?: Record<string, unknown>
  media?: DesktopV3RoutedSessionMediaRequest[]
  staging_ids?: string[]
}

export interface DesktopV3RoutedSessionIdentity {
  session_id: string
  title: string
  workspace_id?: string
  workspace_binding_id?: string
  source_workspace_id?: string
  source_workspace_name: string
  source_workspace_path: string
  runtime_workspace_path: string
  runtime_swarm_id?: string
  authority_host_swarm_id?: string
  worktree_enabled: boolean
  requested_worktree_name?: string
  worktree_root_path?: string
  worktree_base_branch?: string
  worktree_branch?: string
}

export interface DesktopV3RoutedSessionView {
  identity: DesktopV3RoutedSessionIdentity
  agentic_settings: Record<string, unknown>
  media_capability: DesktopV3MediaCapability
  current_execution_epoch?: Record<string, unknown>
  pending_permissions: unknown[]
  usage_summary?: unknown
  current_run_state?: Record<string, unknown>
  has_active_plan?: boolean
  active_plan?: unknown
}

export interface DesktopV3RoutedSessionMutation extends SessionMutationResult {
  session_id: string
  projection: V3SessionProjection
  message: MessageSnapshot
  replayed?: boolean
}

export interface DesktopV3RoutedSessionStartResponse {
  ok: true
  session_id: string
  title: string
  starting_mode: DesktopSessionMode
  replayed: boolean
  session: SessionSnapshot
  session_view: DesktopV3RoutedSessionView
  first_message: MessageSnapshot
  projection: V3SessionProjection
  mutation: DesktopV3RoutedSessionMutation
}

export interface DesktopV3AppendMessageRequest {
  client_request_id: string
  message_id: string
  run_id: string
  role: 'user'
  content: string
  metadata?: Record<string, unknown>
  media?: DesktopV3MediaReference[]
  plan_checkpoint_context?: {
    plan_id: string
    checkpoint_id: string
    attempt_id?: string
  }
}

export interface DesktopV3MediaAssetWire {
  id: string
  modality: string
  detected_mime_type: string
  file_type?: string
  size: number
  digest_sha256: string
  contract_hash: string
}

export async function getDesktopV3MediaCapability(sessionId: string): Promise<DesktopV3MediaCapability> {
  const normalizedSessionId = sessionId.trim()
  if (!normalizedSessionId) throw new Error('Desktop V3 media capability requires session_id')
  const response = await requestJson<{ media_capability: DesktopV3MediaCapability }>(
    `/v3/sessions/${encodeURIComponent(normalizedSessionId)}/media-capability`,
    { method: 'GET' },
  )
  return response.media_capability
}

export async function uploadDesktopV3MediaAsset(input: {
  sessionId: string
  file: File
  mimeType: string
  modality: string
  fileType?: string
  contractToken: string
  signal?: AbortSignal
}): Promise<DesktopV3MediaReference> {
  const sessionId = input.sessionId.trim()
  if (!sessionId || !input.contractToken.trim()) throw new Error('Desktop V3 media upload requires session and current capability')
  const response = await apiFetch(`/v3/sessions/${encodeURIComponent(sessionId)}/media`, {
    method: 'POST',
    headers: {
      'Content-Type': input.mimeType.trim().toLowerCase(),
      'X-Swarm-Media-Modality': input.modality,
      'X-Swarm-Media-File-Type': input.fileType?.trim() ?? '',
      'X-Swarm-Media-Contract': input.contractToken,
    },
    body: input.file,
    signal: input.signal,
  })
  if (!response.ok) throw new Error(await readErrorMessage(response))
  const payload = await response.json() as { asset: DesktopV3MediaAssetWire }
  const asset = payload.asset
  return {
    asset_id: asset.id,
    modality: asset.modality,
    mime_type: asset.detected_mime_type,
    file_type: asset.file_type,
    size: asset.size,
    digest_sha256: asset.digest_sha256,
    contract_hash: asset.contract_hash,
  }
}

export async function postDesktopV3RoutedSessionStart(
  input: DesktopV3RoutedSessionStartRequest,
): Promise<DesktopV3RoutedSessionStartResponse> {
  const userInput = input.input.trim()
  const clientRequestId = input.client_request_id.trim()
  const idempotencyKey = input.idempotency_key?.trim() || clientRequestId
  if (!userInput) throw new Error('Desktop V3 routed start requires input')
  if (!clientRequestId) throw new Error('Desktop V3 routed start requires client_request_id')
  if (idempotencyKey !== clientRequestId) {
    throw new Error('Desktop V3 routed start requires one stable client_request_id/idempotency identity')
  }
  if ((input.media?.length ?? 0) > 0 && (input.staging_ids?.length ?? 0) > 0) {
    throw new Error('Desktop V3 routed start accepts media or staging_ids, not both')
  }

  const request: DesktopV3RoutedSessionStartRequest = {
    input: userInput,
    client_request_id: clientRequestId,
    idempotency_key: clientRequestId,
    ...(input.agent_name?.trim() ? { agent_name: input.agent_name.trim() } : {}),
    ...(input.metadata ? { metadata: input.metadata } : {}),
    ...(input.media?.length ? { media: input.media } : {}),
    ...(input.staging_ids?.length ? { staging_ids: input.staging_ids } : {}),
  }
  const payload = await requestJson<unknown>('/v3/sessions:routed', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'Idempotency-Key': clientRequestId,
    },
    body: JSON.stringify(request),
  })
  return normalizeDesktopV3RoutedSessionStartResponse(payload)
}

export function normalizeDesktopV3RoutedSessionStartResponse(payload: unknown): DesktopV3RoutedSessionStartResponse {
  if (!isRecord(payload) || payload.ok !== true) throw new Error('Desktop V3 routed start returned an invalid response')
  const sessionId = requiredString(payload.session_id, 'session_id')
  const title = requiredString(payload.title, 'title')
  const startingMode = requiredString(payload.starting_mode, 'starting_mode')
  if (startingMode !== 'auto' && startingMode !== 'plan') {
    throw new Error('Desktop V3 routed start returned an invalid starting_mode')
  }
  if (typeof payload.replayed !== 'boolean') throw new Error('Desktop V3 routed start returned an invalid replayed value')
  if (!isRecord(payload.session) || requiredString(payload.session.id, 'session.id') !== sessionId) {
    throw new Error('Desktop V3 routed start session does not match session_id')
  }
  if (requiredString(payload.session.title, 'session.title') !== title || payload.session.mode !== startingMode) {
    throw new Error('Desktop V3 routed start session does not match the routed title/mode')
  }
  if (!isRecord(payload.session_view) || !isRecord(payload.session_view.identity)
    || requiredString(payload.session_view.identity.session_id, 'session_view.identity.session_id') !== sessionId) {
    throw new Error('Desktop V3 routed start session_view does not match session_id')
  }
  if (!isRecord(payload.first_message)
    || requiredString(payload.first_message.session_id, 'first_message.session_id') !== sessionId
    || payload.first_message.role !== 'user') {
    throw new Error('Desktop V3 routed start returned an invalid first_message')
  }
  if (!isRecord(payload.projection) || requiredString(payload.projection.session_id, 'projection.session_id') !== sessionId) {
    throw new Error('Desktop V3 routed start projection does not match session_id')
  }
  if (!isRecord(payload.mutation) || requiredString(payload.mutation.session_id, 'mutation.session_id') !== sessionId) {
    throw new Error('Desktop V3 routed start mutation does not match session_id')
  }
  return payload as unknown as DesktopV3RoutedSessionStartResponse
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function requiredString(value: unknown, field: string): string {
  if (typeof value !== 'string' || !value.trim()) throw new Error(`Desktop V3 routed start requires ${field}`)
  return value.trim()
}

export async function postDesktopV3CreateSession(
  input: DesktopV3CreateSessionRequest,
): Promise<SessionCreateMutationResponse | SessionMutationErrorResponse> {
  return requestJson('/v3/sessions', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(input),
  })
}

export async function postDesktopV3AppendMessage(
  sessionId: string,
  input: DesktopV3AppendMessageRequest,
): Promise<SessionMessageMutationResponse | MessageMutationConflictResponse> {
  const normalizedSessionId = sessionId.trim()
  if (!normalizedSessionId) {
    throw new Error('Desktop V3 append message requires session_id')
  }

  return requestJson(
    `/v3/sessions/${encodeURIComponent(normalizedSessionId)}/messages`,
    {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(input),
    },
  )
}
