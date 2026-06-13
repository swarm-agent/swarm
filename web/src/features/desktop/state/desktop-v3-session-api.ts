import { requestJson } from '../../../app/api'
import type {
  AgentModelPolicyRecord,
  ChatMessageRecord,
  DesktopSessionPlanRecord,
  DesktopSessionPlanRevisionRecord,
  ResolvedSessionPreference,
} from '../chat/types/chat'
import type { DesktopSessionRecord } from '../types/realtime'
import type { DesktopState } from './desktop-state'
import { mergeDesktopStateSnapshot } from './desktop-state-snapshot'
import { getV3RuntimeDesktopSnapshot } from '../v3-runtime/v3-store'

export interface DesktopV3SessionSnapshot {
  source: 'v3'
  session: DesktopSessionRecord
  messages: ChatMessageRecord[]
  events: unknown[]
  projection: null
  preference: ResolvedSessionPreference
  agentModelPolicy: AgentModelPolicyRecord | null
  hasActivePlan: boolean
  activePlan: DesktopSessionPlanRecord | null
  planRevisions: DesktopSessionPlanRevisionRecord[]
  appliedSeq: number
  highWatermark: number
  hydratedAt: number
}

const EMPTY_PREFERENCE: ResolvedSessionPreference = {
  preference: { provider: '', model: '', thinking: '', serviceTier: '', contextMode: '', updatedAt: 0 },
  contextWindow: 0,
  maxOutputTokens: 0,
}

interface HydratedSessionResponseWire {
  session?: unknown
}

export function assertRawCanonicalDesktopV3SessionId(sessionId: string): string {
  const normalizedSessionId = sessionId.trim()
  if (!normalizedSessionId) {
    throw new Error('Desktop V3 requires a raw canonical session id.')
  }
  return normalizedSessionId
}

export function desktopV3SessionSnapshotFromState(state: DesktopState, sessionId: string): DesktopV3SessionSnapshot | null {
  const normalizedSessionId = sessionId.trim()
  const session = state.sessionsById[normalizedSessionId]
  if (!normalizedSessionId || !session) {
    return null
  }
  const activePlan = state.plansBySessionId[normalizedSessionId] ?? null
  const appliedSeq = Math.max(0, session.lastEventSeq ?? 0)
  const highWatermark = Math.max(0, session.projectionHighWatermarkSeq ?? appliedSeq)
  return {
    source: 'v3',
    session,
    messages: state.messagesBySessionId[normalizedSessionId] ?? [],
    events: [],
    projection: null,
    preference: state.preferencesBySessionId[normalizedSessionId] ?? EMPTY_PREFERENCE,
    agentModelPolicy: state.agentModelPolicyBySessionId[normalizedSessionId] ?? null,
    hasActivePlan: Boolean(activePlan?.id || activePlan?.plan || activePlan?.title),
    activePlan,
    planRevisions: state.planRevisionsBySessionId[normalizedSessionId] ?? [],
    appliedSeq,
    highWatermark,
    hydratedAt: Date.now(),
  }
}

export async function fetchAndApplyDesktopV3SessionSnapshot(
  sessionId: string,
  options: { signal?: AbortSignal } = {},
): Promise<DesktopV3SessionSnapshot | null> {
  const normalizedSessionId = assertRawCanonicalDesktopV3SessionId(sessionId)
  await mergeDesktopStateSnapshot({ sessionIds: [normalizedSessionId] }, options.signal)
  return desktopV3SessionSnapshotFromState(getV3RuntimeDesktopSnapshot(), normalizedSessionId)
}

async function refreshSessionAfterMutation(sessionId: string): Promise<DesktopV3SessionSnapshot> {
  const snapshot = await fetchAndApplyDesktopV3SessionSnapshot(sessionId)
  if (!snapshot) {
    throw new Error('Desktop V3 mutation did not return a hydrated canonical session snapshot.')
  }
  return snapshot
}

export async function updateDesktopV3SessionMode(
  sessionId: string,
  mode: string,
): Promise<DesktopV3SessionSnapshot> {
  const normalizedSessionId = assertRawCanonicalDesktopV3SessionId(sessionId)
  await requestJson<HydratedSessionResponseWire>(
    `/v3/sessions/${encodeURIComponent(normalizedSessionId)}/mode`,
    {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ mode }),
    },
  )
  return refreshSessionAfterMutation(normalizedSessionId)
}

export async function updateDesktopV3SessionPreference(
  sessionId: string,
  input: Partial<ResolvedSessionPreference['preference']>,
): Promise<ResolvedSessionPreference> {
  const normalizedSessionId = assertRawCanonicalDesktopV3SessionId(sessionId)
  await requestJson<HydratedSessionResponseWire>(
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
  return (await refreshSessionAfterMutation(normalizedSessionId)).preference
}

export async function updateDesktopV3SessionAgent(
  sessionId: string,
  agentName: string,
): Promise<DesktopV3SessionSnapshot> {
  const normalizedSessionId = assertRawCanonicalDesktopV3SessionId(sessionId)
  await requestJson<HydratedSessionResponseWire>(
    `/v3/sessions/${encodeURIComponent(normalizedSessionId)}/agent`,
    {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ agent_name: agentName.trim() }),
    },
  )
  return refreshSessionAfterMutation(normalizedSessionId)
}

export async function updateDesktopV3SessionMetadata(
  sessionId: string,
  metadata: Record<string, unknown>,
): Promise<DesktopV3SessionSnapshot> {
  const normalizedSessionId = assertRawCanonicalDesktopV3SessionId(sessionId)
  await requestJson<HydratedSessionResponseWire>(
    `/v3/sessions/${encodeURIComponent(normalizedSessionId)}/metadata`,
    {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ metadata }),
    },
  )
  return refreshSessionAfterMutation(normalizedSessionId)
}

export async function saveDesktopV3SessionPlan(
  sessionId: string,
  input: {
    id?: string
    title?: string
    plan?: string
    document?: unknown
    documentPatch?: unknown
    status?: string
    approvalState?: string
  },
): Promise<DesktopV3SessionSnapshot> {
  const normalizedSessionId = assertRawCanonicalDesktopV3SessionId(sessionId)
  await requestJson<HydratedSessionResponseWire>(
    `/v3/sessions/${encodeURIComponent(normalizedSessionId)}/plans`,
    {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        id: input.id?.trim() || undefined,
        plan_id: input.id?.trim() || undefined,
        title: input.title?.trim() || undefined,
        plan: input.plan,
        document: input.document ?? undefined,
        document_patch: input.documentPatch ?? undefined,
        status: input.status?.trim() || undefined,
        approval_state: input.approvalState?.trim() || undefined,
      }),
    },
  )
  return refreshSessionAfterMutation(normalizedSessionId)
}
