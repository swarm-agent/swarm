import { useSyncExternalStore } from 'react'

import type {
  AgentModelPolicyRecord,
  ChatMessageRecord,
  DesktopSessionPlanRecord,
  DesktopSessionPlanRevisionRecord,
  ResolvedSessionPreference,
} from '../chat/types/chat'
import type {
  DesktopRunIntentRecord,
  DesktopSessionRecord,
  DesktopSessionUsageRecord,
} from '../types/realtime'
import {
  createEmptyDesktopState,
  desktopReducer,
  type DesktopDaemonEvent,
  type DesktopDaemonSnapshot,
  type DesktopSessionReadinessRecord,
  type DesktopState,
  type DesktopStateStatus,
} from './desktop-state'

type DesktopStoreListener = () => void

const listeners = new Set<DesktopStoreListener>()
let desktopState = createEmptyDesktopState()

const EMPTY_DESKTOP_MESSAGES: ChatMessageRecord[] = []
const EMPTY_DESKTOP_PLAN_REVISIONS: DesktopSessionPlanRevisionProjection[] = []

type DesktopWorkspaceScope = string | {
  workspacePath?: string | null
  workspacePaths?: Array<string | null | undefined>
}

export interface DesktopSessionPlanProjection {
  sessionId: string
  plan: DesktopSessionPlanRecord | null
  hasActivePlan: boolean
}

export interface DesktopSessionPlanRevisionProjection extends DesktopSessionPlanRevisionRecord {
  sessionId: string
}

export function useDesktopState(): DesktopState
export function useDesktopState<Selected>(selector: (state: DesktopState) => Selected): Selected
export function useDesktopState<Selected>(selector?: (state: DesktopState) => Selected): DesktopState | Selected {
  const state = useSyncExternalStore(subscribeDesktop, getDesktopSnapshot, getDesktopSnapshot)
  return selector ? selector(state) : state
}

export function getDesktopSnapshot(): DesktopState {
  return desktopState
}

export function useDesktopSession(sessionId: string | null | undefined): DesktopSessionRecord | null {
  const normalizedSessionId = sessionId?.trim() ?? ''
  return useDesktopState((state) => normalizedSessionId ? selectDesktopSession(state, normalizedSessionId) : null)
}

export function useDesktopWorkspaceSessions(workspaceScope: DesktopWorkspaceScope): DesktopSessionRecord[] {
  const workspaceKey = desktopWorkspaceScopeKey(workspaceScope)
  return useDesktopState((state) => selectDesktopWorkspaceSessions(state, workspaceKey))
}

export function useDesktopRouteReadiness(_workspaceScope: DesktopWorkspaceScope, sessionId: string | null | undefined): DesktopSessionReadinessRecord | null {
  const normalizedSessionId = sessionId?.trim() ?? ''
  return useDesktopState((state) => normalizedSessionId ? state.routeReadinessBySessionId[normalizedSessionId] ?? null : null)
}

export function useDesktopMessages(sessionId: string | null | undefined): ChatMessageRecord[] {
  const normalizedSessionId = sessionId?.trim() ?? ''
  return useDesktopState((state) => normalizedSessionId ? state.messagesBySessionId[normalizedSessionId] ?? EMPTY_DESKTOP_MESSAGES : EMPTY_DESKTOP_MESSAGES)
}

export function useDesktopPreference(sessionId: string | null | undefined): ResolvedSessionPreference | null {
  const normalizedSessionId = sessionId?.trim() ?? ''
  return useDesktopState((state) => normalizedSessionId ? state.preferencesBySessionId[normalizedSessionId] ?? null : null)
}

export function useDesktopActiveRun(sessionId: string | null | undefined): DesktopRunIntentRecord | null {
  const normalizedSessionId = sessionId?.trim() ?? ''
  return useDesktopState((state) => normalizedSessionId ? state.runIntentsBySessionId[normalizedSessionId] ?? null : null)
}

export function useDesktopAgentModelPolicy(sessionId: string | null | undefined): AgentModelPolicyRecord | null {
  const normalizedSessionId = sessionId?.trim() ?? ''
  return useDesktopState((state) => normalizedSessionId ? state.agentModelPolicyBySessionId[normalizedSessionId] ?? null : null)
}

export function useDesktopPlan(sessionId: string | null | undefined): DesktopSessionPlanProjection | null {
  const normalizedSessionId = sessionId?.trim() ?? ''
  return useDesktopState((state) => {
    if (!normalizedSessionId) {
      return null
    }
    const plan = state.plansBySessionId[normalizedSessionId] ?? null
    return {
      sessionId: normalizedSessionId,
      plan,
      hasActivePlan: Boolean(plan?.id || plan?.plan || plan?.title),
    }
  })
}

export function useDesktopPlanRevisions(sessionId: string | null | undefined): DesktopSessionPlanRevisionProjection[] {
  const normalizedSessionId = sessionId?.trim() ?? ''
  return useDesktopState((state) => normalizedSessionId
    ? (state.planRevisionsBySessionId[normalizedSessionId] ?? EMPTY_DESKTOP_PLAN_REVISIONS).map((revision) => ({
        ...revision,
        sessionId: normalizedSessionId,
      }))
    : EMPTY_DESKTOP_PLAN_REVISIONS)
}

export function subscribeDesktop(listener: DesktopStoreListener): () => void {
  listeners.add(listener)
  return () => {
    listeners.delete(listener)
  }
}

export function replaceDesktopFromSnapshot(snapshot: DesktopDaemonSnapshot): DesktopState {
  return dispatchDesktopState({ type: 'snapshot/replace', snapshot })
}

export function mergeDesktopSnapshot(snapshot: DesktopDaemonSnapshot): DesktopState {
  return dispatchDesktopState({ type: 'snapshot/merge', snapshot })
}

export function applyDesktopDaemonEvent(event: DesktopDaemonEvent): DesktopState {
  return dispatchDesktopState({ type: 'daemon/event', event })
}

export function applyDesktopDurableEventEnvelope(event: unknown): DesktopState {
  const envelope = event && typeof event === 'object' ? event as Record<string, unknown> : null
  const eventType = typeof envelope?.event_type === 'string' ? envelope.event_type.trim() : ''
  if (!envelope || !eventType) {
    return markDesktopStale('durable event missing event_type')
  }

  const payload = envelope.payload && typeof envelope.payload === 'object'
    ? {
        ...envelope.payload as Record<string, unknown>,
        event_type: eventType,
        source_seq: (envelope.payload as Record<string, unknown>).source_seq ?? envelope.source_seq,
        global_seq: (envelope.payload as Record<string, unknown>).global_seq ?? envelope.global_seq,
        ts_unix_ms: (envelope.payload as Record<string, unknown>).ts_unix_ms ?? envelope.ts_unix_ms,
      }
    : envelope
  const currentRev = desktopState.rev
  const explicitRev = positiveNumber(envelope.rev)
  const rev = explicitRev || currentRev + 1
  const prevRev = explicitRev ? finiteNumber(envelope.prevRev) ?? Math.max(0, explicitRev - 1) : currentRev

  return applyDesktopDaemonEvent({
    rev,
    prevRev,
    type: eventType,
    payload,
    stream: typeof envelope.stream === 'string' ? envelope.stream : undefined,
    entityId: typeof envelope.entity_id === 'string' ? envelope.entity_id : undefined,
    globalSeq: positiveNumber(envelope.global_seq) || undefined,
    sourceSeq: positiveNumber(envelope.source_seq) || undefined,
    tsUnixMs: positiveNumber(envelope.ts_unix_ms) || undefined,
  })
}

export function markDesktopStale(reason: string): DesktopState {
  return dispatchDesktopState({ type: 'connection/stale', reason })
}

export function setDesktopConnectionStatus(status: DesktopStateStatus, error?: string | null): DesktopState {
  return dispatchDesktopState({ type: 'connection/status', status, error })
}

function positiveNumber(value: unknown): number {
  return typeof value === 'number' && Number.isFinite(value) && value > 0 ? Math.floor(value) : 0
}

function finiteNumber(value: unknown): number | null {
  return typeof value === 'number' && Number.isFinite(value) && value >= 0 ? Math.floor(value) : null
}

function desktopWorkspaceScopeKey(workspaceScope: DesktopWorkspaceScope): string {
  return desktopWorkspacePaths(workspaceScope).join('\u0000')
}

function desktopWorkspacePaths(workspaceScope: DesktopWorkspaceScope): string[] {
  if (typeof workspaceScope === 'string') {
    const path = workspaceScope.trim()
    return path ? [path] : []
  }
  const paths = new Set<string>()
  const workspacePath = workspaceScope.workspacePath?.trim() ?? ''
  if (workspacePath) {
    paths.add(workspacePath)
  }
  for (const path of workspaceScope.workspacePaths ?? []) {
    const normalized = path?.trim() ?? ''
    if (normalized) {
      paths.add(normalized)
    }
  }
  return Array.from(paths).sort()
}

function selectDesktopWorkspaceSessions(state: DesktopState, workspaceKey: string): DesktopSessionRecord[] {
  const workspacePaths = new Set(workspaceKey ? workspaceKey.split('\u0000') : [])
  const sessions = state.sessionOrder
    .map((sessionId) => selectDesktopSession(state, sessionId))
    .filter((session): session is DesktopSessionRecord => Boolean(session))
    .filter((session) => workspacePaths.size === 0 || workspacePaths.has(session.workspacePath?.trim() ?? ''))
  return [...sessions].sort((left, right) => right.updatedAt - left.updatedAt || left.id.localeCompare(right.id))
}

function selectDesktopSession(state: DesktopState, sessionId: string): DesktopSessionRecord | null {
  const session = state.sessionsById[sessionId]
  if (!session) {
    return null
  }
  const usage = state.usageBySessionId[sessionId] as DesktopSessionUsageRecord | undefined
  const runIntent = state.runIntentsBySessionId[sessionId]
  const changed = Boolean((usage && usage !== session.usage) || (runIntent && runIntent !== session.runIntent))
  return changed
    ? {
        ...session,
        usage: usage ?? session.usage,
        runIntent: runIntent ?? session.runIntent,
      }
    : session
}

function dispatchDesktopState(action: Parameters<typeof desktopReducer>[1]): DesktopState {
  const nextState = desktopReducer(desktopState, action)
  if (Object.is(nextState, desktopState)) {
    return desktopState
  }

  desktopState = nextState
  emitDesktopStateChange()
  return desktopState
}

function emitDesktopStateChange(): void {
  for (const listener of listeners) {
    listener()
  }
}
