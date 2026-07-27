import { requestJson } from '../../../app/api'
import type { ModelProfileChoice, ModelProfileSelectionRecord } from '../chat/types/chat'
import type { DesktopSessionMode } from '../settings/swarm/types/swarm-settings'

import type {
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
  plan_checkpoint_context?: {
    plan_id: string
    checkpoint_id: string
    attempt_id?: string
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
