import { apiFetch, readErrorMessage, requestJson } from '../../../app/api'
import type { ModelProfileChoice, ModelProfileSelectionRecord } from '../chat/types/chat'
import type { DesktopSessionMode } from '../settings/swarm/types/swarm-settings'

import type {
  DesktopV3MediaCapability,
  DesktopV3MediaReference,
  MessageMutationConflictResponse,
  SessionCreateMutationResponse,
  SessionMessageMutationResponse,
  SessionMutationErrorResponse,
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
  temporary?: {
    name?: string
    model_mode: 'single' | 'split'
    single?: DesktopV3ModelProfileSelectionWire
    plan?: DesktopV3ModelProfileSelectionWire
    auto?: DesktopV3ModelProfileSelectionWire
  }
  use_agent_default?: true
}

function modelProfileSelectionWire(value: ModelProfileSelectionRecord | null): DesktopV3ModelProfileSelectionWire | undefined {
  return value ? {
    provider: value.provider.trim(), model: value.model.trim(), thinking: value.thinking.trim(),
    service_tier: value.serviceTier.trim() || undefined, context_mode: value.contextMode.trim() || undefined,
  } : undefined
}

export function desktopV3ModelProfileChoiceWire(choice: ModelProfileChoice): DesktopV3ModelProfileChoiceWire {
  switch (choice.kind) {
    case 'account-default': return { use_account_default: true }
    case 'saved': return { saved_profile_id: choice.profileId.trim() }
    case 'agent-default': return { use_agent_default: true }
    case 'temporary': return {
      temporary: {
        name: choice.profile.name.trim() || 'Temporary', model_mode: choice.profile.modelMode,
        single: modelProfileSelectionWire(choice.profile.single), plan: modelProfileSelectionWire(choice.profile.plan), auto: modelProfileSelectionWire(choice.profile.auto),
      },
    }
  }
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
      'Content-Type': input.file.type,
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
