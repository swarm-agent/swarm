import { requestJson } from '../../../../app/api'
import type { DesktopSessionRecord } from '../../types/realtime'
import type { ResolvedSessionPreference } from '../../chat/types/chat'
import { mapDesktopSession } from '../../chat/queries/chat-queries'

interface IntegrationWorkspaceSessionResponseWire {
  session?: unknown
}

interface IntegrationWorkspaceWire {
  workspace_id?: unknown
  display_name?: unknown
  pack_id?: unknown
  draft_version_id?: unknown
  latest_child_session_id?: unknown
  latest_child_session_at?: unknown
  metadata?: unknown
  created_at?: unknown
  updated_at?: unknown
}

interface IntegrationWorkspaceChildWire {
  workspace_session?: unknown
  session?: unknown
}

interface IntegrationWorkspaceSnapshotWire {
  workspace?: IntegrationWorkspaceWire
  session?: unknown
  sessions?: IntegrationWorkspaceChildWire[]
}

interface IntegrationWorkspaceListWire {
  workspaces?: IntegrationWorkspaceWire[]
}

export interface IntegrationWorkspaceRecord {
  workspaceId: string
  displayName: string
  packId: string
  draftVersionId: string
  latestChildSessionId: string
  latestChildSessionAt: string
  metadata: Record<string, string>
  createdAt: string
  updatedAt: string
}

export interface IntegrationWorkspaceChildSession {
  session: DesktopSessionRecord
  title: string
  createdAt: string
  updatedAt: string
}

export interface IntegrationWorkspaceSnapshot {
  workspace: IntegrationWorkspaceRecord | null
  session: DesktopSessionRecord | null
  sessions: IntegrationWorkspaceChildSession[]
}

export const INTEGRATION_WORKSPACE_SOURCE = 'integration_workspace'

function stringField(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}

function stringMap(value: unknown): Record<string, string> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return {}
  const out: Record<string, string> = {}
  for (const [key, raw] of Object.entries(value)) {
    const normalizedKey = key.trim()
    const normalizedValue = typeof raw === 'string' ? raw.trim() : ''
    if (normalizedKey && normalizedValue) out[normalizedKey] = normalizedValue
  }
  return out
}

function mapWorkspace(value: IntegrationWorkspaceWire | undefined): IntegrationWorkspaceRecord | null {
  if (!value || typeof value !== 'object') return null
  const workspaceId = stringField(value.workspace_id)
  if (!workspaceId) return null
  return {
    workspaceId,
    displayName: stringField(value.display_name) || 'New integration',
    packId: stringField(value.pack_id),
    draftVersionId: stringField(value.draft_version_id),
    latestChildSessionId: stringField(value.latest_child_session_id),
    latestChildSessionAt: stringField(value.latest_child_session_at),
    metadata: stringMap(value.metadata),
    createdAt: stringField(value.created_at),
    updatedAt: stringField(value.updated_at),
  }
}

function mapWorkspaceChild(value: IntegrationWorkspaceChildWire): IntegrationWorkspaceChildSession | null {
  const session = mapDesktopSession(value.session ?? {})
  if (!session?.id) return null
  const join = value.workspace_session && typeof value.workspace_session === 'object' ? value.workspace_session as Record<string, unknown> : {}
  return {
    session,
    title: stringField(join.title) || session.title || 'Builder chat',
    createdAt: stringField(join.created_at),
    updatedAt: stringField(join.updated_at),
  }
}

function mapWorkspaceSnapshot(response: IntegrationWorkspaceSnapshotWire): IntegrationWorkspaceSnapshot {
  const workspace = mapWorkspace(response.workspace)
  const session = response.session ? mapDesktopSession(response.session) : null
  return {
    workspace,
    session: session?.id ? session : null,
    sessions: Array.isArray(response.sessions)
      ? response.sessions.map(mapWorkspaceChild).filter((child): child is IntegrationWorkspaceChildSession => Boolean(child))
      : [],
  }
}

function integrationSessionMetadata(input?: Record<string, unknown>): Record<string, unknown> {
  return {
    ...input,
    source: INTEGRATION_WORKSPACE_SOURCE,
    session_source: INTEGRATION_WORKSPACE_SOURCE,
    scope: 'swarm',
    workspace_scope: 'swarm',
  }
}

function preferenceWire(preference: ResolvedSessionPreference['preference']) {
  return {
    provider: preference.provider,
    model: preference.model,
    thinking: preference.thinking,
    service_tier: preference.serviceTier,
    context_mode: preference.contextMode,
  }
}

export function workspaceIdFromName(name: string): string {
  const normalized = name.trim().toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '')
  return normalized || `integration-${Date.now().toString(36)}`
}

export async function fetchIntegrationWorkspaces(limit = 100): Promise<IntegrationWorkspaceRecord[]> {
  const search = new URLSearchParams({ limit: String(limit) })
  const response = await requestJson<IntegrationWorkspaceListWire>(`/v1/integrations/workspaces?${search.toString()}`)
  return Array.isArray(response.workspaces)
    ? response.workspaces.map(mapWorkspace).filter((workspace): workspace is IntegrationWorkspaceRecord => Boolean(workspace))
    : []
}

export async function openIntegrationWorkspace(input: {
  workspaceId: string
  displayName: string
  packId?: string
  draftVersionId?: string
  title?: string
  mode: string
  createChild?: boolean
  newChild?: boolean
  metadata?: Record<string, unknown>
  preference: ResolvedSessionPreference['preference']
}): Promise<IntegrationWorkspaceSnapshot> {
  const response = await requestJson<IntegrationWorkspaceSnapshotWire>('/v1/integrations/workspaces', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      workspace_id: input.workspaceId,
      display_name: input.displayName,
      pack_id: input.packId ?? '',
      draft_version_id: input.draftVersionId ?? '',
      title: input.title ?? input.displayName,
      mode: input.mode,
      create_child: Boolean(input.createChild),
      new_child: Boolean(input.newChild),
      metadata: input.metadata ?? {},
      preference: preferenceWire(input.preference),
    }),
  })
  return mapWorkspaceSnapshot(response)
}

export async function fetchIntegrationWorkspace(workspaceId: string): Promise<IntegrationWorkspaceSnapshot> {
  const response = await requestJson<IntegrationWorkspaceSnapshotWire>(`/v1/integrations/workspaces/${encodeURIComponent(workspaceId)}`)
  return mapWorkspaceSnapshot(response)
}

export async function createIntegrationWorkspaceChildSession(input: {
  workspaceId: string
  title?: string
  mode: string
  metadata?: Record<string, unknown>
  preference: ResolvedSessionPreference['preference']
}): Promise<DesktopSessionRecord> {
  const response = await requestJson<IntegrationWorkspaceSessionResponseWire>(`/v1/integrations/workspaces/${encodeURIComponent(input.workspaceId)}/sessions`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      action: 'new',
      title: input.title ?? '',
      mode: input.mode,
      metadata: integrationSessionMetadata(input.metadata),
      preference: preferenceWire(input.preference),
    }),
  })
  return mapDesktopSession(response.session ?? {})
}

export async function switchIntegrationWorkspaceSession(workspaceId: string, sessionId: string): Promise<DesktopSessionRecord> {
  const response = await requestJson<IntegrationWorkspaceSessionResponseWire>(`/v1/integrations/workspaces/${encodeURIComponent(workspaceId)}/sessions/${encodeURIComponent(sessionId)}`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({}),
  })
  return mapDesktopSession(response.session ?? {})
}
