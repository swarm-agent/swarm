import { requestJson } from '../../../app/api'
import { fetchSessionMessages, type FetchSessionMessagesResult } from '../chat/queries/chat-queries'
import { dedupeAndTrimMessages } from '../chat/services/message-cache'
import { normalizeDesktopSessionPlan, normalizeDesktopSessionPlanRevisions, type DesktopSessionPlanWire } from '../chat/services/session-plan-record'
import type {
  AgentModelPolicyRecord,
  ChatMessageRecord,
  DesktopSessionPlanRecord,
  DesktopSessionPlanRevisionRecord,
  ResolvedSessionPreference,
} from '../chat/types/chat'
import type { DesktopSessionRecord, DesktopSessionUsageRecord } from '../types/realtime'
import type { DesktopState } from './desktop-state'
import { mergeDesktopStateSnapshot } from './desktop-state-snapshot'
import { applyV3RuntimeEnvelope, createV3EventEnvelope, createV3SnapshotEnvelope } from '../v3-runtime'
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

export const DESKTOP_V3_MESSAGE_PAGE_LIMIT = 200

const EMPTY_PREFERENCE: ResolvedSessionPreference = {
  preference: { provider: '', model: '', thinking: '', serviceTier: '', contextMode: '', updatedAt: 0 },
  contextWindow: 0,
  maxOutputTokens: 0,
}

interface HydratedSessionResponseWire {
  session?: unknown
}

interface PreferenceWire {
  provider?: string
  model?: string
  thinking?: string
  service_tier?: string
  context_mode?: string
  updated_at?: number
}

interface PreferenceResponseWire {
  preference?: PreferenceWire
  context_window?: number
  max_output_tokens?: number
}

interface UsageWire {
  session_id?: string
  provider?: string
  model?: string
  source?: string
  context_window?: number
  total_tokens?: number
  remaining_tokens?: number
  updated_at?: number
}

interface UsageResponseWire {
  usage_summary?: UsageWire | null
}

interface ActivePlanResponseWire {
  has_active?: boolean
  active_plan?: DesktopSessionPlanWire | null
}

interface PlanSaveResponseWire {
  plan?: DesktopSessionPlanWire | null
}

interface PlanHistoryResponseWire {
  revisions?: DesktopSessionPlanWire[]
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

export async function fetchAndApplyDesktopV3PlanSnapshot(
  sessionId: string,
  options: { signal?: AbortSignal; includeHistory?: boolean } = {},
): Promise<Pick<DesktopV3SessionSnapshot, 'hasActivePlan' | 'activePlan' | 'planRevisions'>> {
  const normalizedSessionId = assertRawCanonicalDesktopV3SessionId(sessionId)
  const active = await requestJson<ActivePlanResponseWire>(
    `/v3/sessions/${encodeURIComponent(normalizedSessionId)}/plans/active`,
    { signal: options.signal },
  )
  const activePlan = active.active_plan ? normalizeDesktopSessionPlan(active.active_plan) : null
  let planRevisions: DesktopSessionPlanRevisionRecord[] = []
  if (options.includeHistory !== false && activePlan?.id) {
    const history = await requestJson<PlanHistoryResponseWire>(
      `/v3/sessions/${encodeURIComponent(normalizedSessionId)}/plans/${encodeURIComponent(activePlan.id)}/history?limit=100`,
      { signal: options.signal },
    )
    planRevisions = normalizeDesktopSessionPlanRevisions(history.revisions)
  }
  const snapshot = getV3RuntimeDesktopSnapshot()
  const receivedAt = Date.now()
  applyV3RuntimeEnvelope(createV3SnapshotEnvelope({
    rev: snapshot.rev,
    plansBySessionId: { [normalizedSessionId]: activePlan },
    planRevisionsBySessionId: { [normalizedSessionId]: planRevisions },
  }, {
    mode: 'merge',
    receivedAt,
    sessionId: normalizedSessionId,
    source: { kind: 'http', transport: 'http', name: 'v3-session-plan' },
    id: `plans:${normalizedSessionId}:${activePlan?.id ?? 'none'}:${planRevisions.length}:${receivedAt}`,
  }))
  return {
    hasActivePlan: Boolean(active.has_active || activePlan?.id || activePlan?.plan || activePlan?.title),
    activePlan,
    planRevisions,
  }
}

export async function fetchAndApplyDesktopV3SessionSnapshot(
  sessionId: string,
  options: { signal?: AbortSignal } = {},
): Promise<DesktopV3SessionSnapshot | null> {
  const normalizedSessionId = assertRawCanonicalDesktopV3SessionId(sessionId)
  await mergeDesktopStateSnapshot({
    sessionIds: [normalizedSessionId],
    history: { mode: 'none', maxEventsPerSession: 200, manifestPolicy: 'manifest', includeEvents: true },
    resources: { events: true, runIntents: true },
  }, options.signal)
  return desktopV3SessionSnapshotFromState(getV3RuntimeDesktopSnapshot(), normalizedSessionId)
}

export async function fetchAndApplyDesktopV3SessionPreference(
  sessionId: string,
  options: { signal?: AbortSignal } = {},
): Promise<ResolvedSessionPreference> {
  const normalizedSessionId = assertRawCanonicalDesktopV3SessionId(sessionId)
  const response = await requestJson<PreferenceResponseWire>(
    `/v3/sessions/${encodeURIComponent(normalizedSessionId)}/preference`,
    { signal: options.signal },
  )
  return applyDesktopV3SessionPreference(normalizedSessionId, response, 'v3-session-preference')
}

export async function fetchAndApplyDesktopV3SessionUsage(
  sessionId: string,
  options: { signal?: AbortSignal } = {},
): Promise<DesktopSessionUsageRecord | null> {
  const normalizedSessionId = assertRawCanonicalDesktopV3SessionId(sessionId)
  const response = await requestJson<UsageResponseWire>(
    `/v3/sessions/${encodeURIComponent(normalizedSessionId)}/usage`,
    { signal: options.signal },
  )
  return applyDesktopV3SessionUsage(normalizedSessionId, response.usage_summary ?? null, 'v3-session-usage')
}

export async function fetchAndApplyDesktopV3SessionMessagesTail(
  sessionId: string,
  options: { signal?: AbortSignal } = {},
): Promise<FetchSessionMessagesResult> {
  const normalizedSessionId = assertRawCanonicalDesktopV3SessionId(sessionId)
  const page = await fetchSessionMessages(normalizedSessionId, options.signal, 0, {
    sessionApi: 'v3',
    limit: DESKTOP_V3_MESSAGE_PAGE_LIMIT,
    tail: true,
  })
  const snapshot = getV3RuntimeDesktopSnapshot()
  const messages = mergeTailPageWithHotMessages(snapshot.messagesBySessionId[normalizedSessionId] ?? [], page)
  const receivedAt = Date.now()
  applyV3RuntimeEnvelope(createV3SnapshotEnvelope({
    rev: snapshot.rev,
    messagesBySessionId: { [normalizedSessionId]: messages },
  }, {
    mode: 'merge',
    receivedAt,
    sessionId: normalizedSessionId,
    highWatermarkSeq: page.highWatermark,
    source: { kind: 'http', transport: 'http', name: 'v3-session-messages-tail' },
    id: `messages:tail:${normalizedSessionId}:${page.oldestSeq}:${page.newestSeq}:${page.messages.length}:${receivedAt}`,
  }))
  return page
}

function mergeTailPageWithHotMessages(existing: ChatMessageRecord[], page: FetchSessionMessagesResult): ChatMessageRecord[] {
  const newerHotMessages = existing.filter((message) => message.globalSeq > page.newestSeq)
  return dedupeAndTrimMessages([...page.messages, ...newerHotMessages], DESKTOP_V3_MESSAGE_PAGE_LIMIT)
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
  const response = await requestJson<PreferenceResponseWire>(
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
  return applyDesktopV3SessionPreference(normalizedSessionId, response, 'v3-session-preference-update')
}

function applyDesktopV3SessionPreference(
  sessionId: string,
  response: PreferenceResponseWire,
  sourceName: string,
): ResolvedSessionPreference {
  const preference = normalizeDesktopV3Preference(response)
  const snapshot = getV3RuntimeDesktopSnapshot()
  const receivedAt = Date.now()
  applyV3RuntimeEnvelope(createV3SnapshotEnvelope({
    rev: snapshot.rev,
    preferencesBySessionId: { [sessionId]: preference },
  }, {
    mode: 'merge',
    receivedAt,
    sessionId,
    source: { kind: 'http', transport: 'http', name: sourceName },
    id: `preference:${sessionId}:${preference.preference.updatedAt}:${receivedAt}`,
  }))
  return preference
}

function normalizeDesktopV3Preference(response: PreferenceResponseWire): ResolvedSessionPreference {
  const preference = response.preference ?? {}
  return {
    preference: {
      provider: String(preference.provider ?? '').trim(),
      model: String(preference.model ?? '').trim(),
      thinking: String(preference.thinking ?? '').trim(),
      serviceTier: String(preference.service_tier ?? '').trim(),
      contextMode: String(preference.context_mode ?? '').trim(),
      updatedAt: numberValue(preference.updated_at),
    },
    contextWindow: numberValue(response.context_window),
    maxOutputTokens: numberValue(response.max_output_tokens),
  }
}

function applyDesktopV3SessionUsage(
  sessionId: string,
  usage: UsageWire | null,
  sourceName: string,
): DesktopSessionUsageRecord | null {
  const normalizedUsage = normalizeDesktopV3Usage(usage, sessionId)
  const snapshot = getV3RuntimeDesktopSnapshot()
  const receivedAt = Date.now()
  if (!normalizedUsage) {
    applyV3RuntimeEnvelope(createV3EventEnvelope({
      type: 'desktop/usage/set',
      payload: { sessionId, usage: null },
    }, {
      receivedAt,
      sessionId,
      source: { kind: 'http', transport: 'http', name: sourceName },
    }))
    return null
  }
  applyV3RuntimeEnvelope(createV3SnapshotEnvelope({
    rev: snapshot.rev,
    usageBySessionId: { [sessionId]: normalizedUsage },
  }, {
    mode: 'merge',
    receivedAt,
    sessionId,
    source: { kind: 'http', transport: 'http', name: sourceName },
    id: `usage:${sessionId}:${normalizedUsage.updatedAt}:${receivedAt}`,
  }))
  return normalizedUsage
}

function normalizeDesktopV3Usage(usage: UsageWire | null, fallbackSessionId: string): DesktopSessionUsageRecord | null {
  if (!usage || typeof usage !== 'object') {
    return null
  }
  const sessionId = String(usage.session_id ?? fallbackSessionId).trim()
  const contextWindow = numberValue(usage.context_window)
  const totalTokens = numberValue(usage.total_tokens)
  const remainingTokens = numberValue(usage.remaining_tokens)
  const updatedAt = numberValue(usage.updated_at)
  if (!sessionId && contextWindow <= 0 && totalTokens <= 0 && remainingTokens <= 0 && updatedAt <= 0) {
    return null
  }
  return {
    sessionId,
    provider: String(usage.provider ?? '').trim(),
    model: String(usage.model ?? '').trim(),
    source: String(usage.source ?? '').trim(),
    contextWindow,
    totalTokens,
    remainingTokens,
    updatedAt,
  }
}

function numberValue(value: unknown): number {
  return typeof value === 'number' && Number.isFinite(value) ? value : 0
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
): Promise<Pick<DesktopV3SessionSnapshot, 'hasActivePlan' | 'activePlan' | 'planRevisions'>> {
  const normalizedSessionId = assertRawCanonicalDesktopV3SessionId(sessionId)
  const response = await requestJson<PlanSaveResponseWire>(
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
  const savedPlan = response.plan ? normalizeDesktopSessionPlan(response.plan) : null
  const snapshot = getV3RuntimeDesktopSnapshot()
  const receivedAt = Date.now()
  applyV3RuntimeEnvelope(createV3SnapshotEnvelope({
    rev: snapshot.rev,
    plansBySessionId: { [normalizedSessionId]: savedPlan },
  }, {
    mode: 'merge',
    receivedAt,
    sessionId: normalizedSessionId,
    source: { kind: 'http', transport: 'http', name: 'v3-session-plan-save' },
    id: `plans:save:${normalizedSessionId}:${savedPlan?.id ?? 'none'}:${receivedAt}`,
  }))
  return fetchAndApplyDesktopV3PlanSnapshot(normalizedSessionId)
}
