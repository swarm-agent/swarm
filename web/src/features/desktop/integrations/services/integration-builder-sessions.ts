import { requestJson } from '../../../../app/api'
import type { DesktopSessionRecord } from '../../types/realtime'
import type { ResolvedSessionPreference } from '../../chat/types/chat'
import { mapDesktopSession } from '../../chat/queries/chat-queries'

interface IntegrationBuilderSessionsResponseWire {
  sessions?: unknown[]
}

interface IntegrationBuilderSessionResponseWire {
  session?: unknown
}

export const INTEGRATION_BUILDER_WORKSPACE_PATH = '__swarm_integrations__'
export const INTEGRATION_BUILDER_WORKSPACE_NAME = 'Integrations'
export const INTEGRATION_BUILDER_SOURCE = 'integration_builder'

export function isIntegrationBuilderSession(session: DesktopSessionRecord | null | undefined): boolean {
  const metadata = session?.metadata
  if (!metadata || typeof metadata !== 'object' || Array.isArray(metadata)) return false
  const source = typeof metadata.source === 'string' ? metadata.source.trim() : ''
  const sessionSource = typeof metadata.session_source === 'string' ? metadata.session_source.trim() : ''
  return source === INTEGRATION_BUILDER_SOURCE || sessionSource === INTEGRATION_BUILDER_SOURCE
}

export async function fetchIntegrationBuilderSessions(limit = 100): Promise<DesktopSessionRecord[]> {
  const search = new URLSearchParams({ limit: String(limit) })
  const response = await requestJson<IntegrationBuilderSessionsResponseWire>(`/v1/integrations/builder/sessions?${search.toString()}`)
  return Array.isArray(response.sessions)
    ? response.sessions
      .map((session) => mapDesktopSession(session))
      .filter((session): session is DesktopSessionRecord => Boolean(session?.id))
    : []
}

export async function createIntegrationBuilderSession(input: {
  title?: string
  mode: string
  agentName?: string
  metadata?: Record<string, unknown>
  preference: ResolvedSessionPreference['preference']
}): Promise<DesktopSessionRecord> {
  const response = await requestJson<IntegrationBuilderSessionResponseWire>('/v1/integrations/builder/sessions', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      title: input.title ?? '',
      mode: input.mode,
      agent_name: input.agentName?.trim() ?? '',
      metadata: {
        ...input.metadata,
        source: INTEGRATION_BUILDER_SOURCE,
        session_source: INTEGRATION_BUILDER_SOURCE,
        scope: 'swarm',
        workspace_scope: 'swarm',
      },
      preference: {
        provider: input.preference.provider,
        model: input.preference.model,
        thinking: input.preference.thinking,
        service_tier: input.preference.serviceTier,
        context_mode: input.preference.contextMode,
      },
    }),
  })
  return mapDesktopSession(response.session ?? {})
}
