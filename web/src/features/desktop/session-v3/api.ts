import { requestJson } from '../../../app/api'
import type { ResolvedSessionPreference } from '../chat/types/chat'
import type { DesktopSessionMode } from '../settings/swarm/types/swarm-settings'
import { normalizeSessionMode } from '../settings/swarm/types/swarm-settings'
import type {
  SessionV3PermissionResolveRequestWire,
  SessionV3PermissionResolveResponseWire,
  SessionV3PermissionsResolveAllResponseWire,
  SessionV3PreferenceResponseWire,
  SessionV3RunStopResponseWire,
} from './types'

function mapPreferenceResponse(response: SessionV3PreferenceResponseWire): ResolvedSessionPreference {
  return {
    preference: {
      provider: String(response.preference?.provider ?? '').trim(),
      model: String(response.preference?.model ?? '').trim(),
      thinking: String(response.preference?.thinking ?? '').trim(),
      serviceTier: String(response.preference?.service_tier ?? '').trim(),
      contextMode: String(response.preference?.context_mode ?? '').trim(),
      updatedAt: typeof response.preference?.updated_at === 'number' ? response.preference.updated_at : 0,
    },
    contextWindow: typeof response.context_window === 'number' ? response.context_window : 0,
    maxOutputTokens: typeof response.max_output_tokens === 'number' ? response.max_output_tokens : 0,
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
): Promise<ResolvedSessionPreference> {
  const normalizedSessionId = sessionId.trim()
  if (!normalizedSessionId) throw new Error('Desktop V3 preference update requires session_id')
  const response = await requestJson<SessionV3PreferenceResponseWire>(
    `/v3/sessions/${encodeURIComponent(normalizedSessionId)}/preference`,
    {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        provider: input.provider,
        model: input.model,
        thinking: input.thinking,
        service_tier: input.serviceTier,
        context_mode: input.contextMode,
      }),
    },
  )
  return mapPreferenceResponse(response)
}

export async function updateSessionV3Mode(sessionId: string, mode: DesktopSessionMode): Promise<DesktopSessionMode> {
  const normalizedSessionId = sessionId.trim()
  if (!normalizedSessionId) throw new Error('Desktop V3 mode update requires session_id')
  const normalizedMode = normalizeSessionMode(mode)
  const response = await requestJson<{ ok?: boolean; mode?: string }>(
    `/v3/sessions/${encodeURIComponent(normalizedSessionId)}/mode`,
    {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ mode: normalizedMode }),
    },
  )
  return normalizeSessionMode(response.mode ?? normalizedMode)
}

export async function updateSessionV3Agent(sessionId: string, agentName: string): Promise<void> {
  const normalizedSessionId = sessionId.trim()
  const normalizedAgent = agentName.trim()
  if (!normalizedSessionId) throw new Error('Desktop V3 agent update requires session_id')
  if (!normalizedAgent) throw new Error('Desktop V3 agent update requires agent_name')
  await requestJson(`/v3/sessions/${encodeURIComponent(normalizedSessionId)}/agent`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ agent_name: normalizedAgent }),
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
