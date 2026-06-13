import { useSyncExternalStore } from 'react'

import type {
  ChatMessageRecord,
  ResolvedSessionPreference,
} from '../chat/types/chat'
import type {
  DesktopRunIntentRecord,
  DesktopSessionRecord,
} from '../types/realtime'
import type {
  DesktopDaemonEvent,
  DesktopDaemonSnapshot,
  DesktopSessionReadinessRecord,
  DesktopState,
  DesktopStateStatus,
} from './desktop-state'
import {
  applyV3RuntimeEnvelope,
  getV3RuntimeDesktopSnapshot,
  getV3RuntimeSnapshot,
  subscribeV3Runtime,
} from '../v3-runtime/v3-store'
import {
  createV3ConnectionStaleEnvelope,
  createV3ConnectionStatusEnvelope,
  createV3EventEnvelope,
  createV3SnapshotEnvelope,
  normalizeV3DurableEventEnvelope,
} from '../v3-runtime/v3-envelope'
import {
  selectV3ActiveRun,
  selectV3AgentModelPolicy,
  selectV3Messages,
  selectV3Plan,
  selectV3PlanRevisions,
  selectV3Preference,
  selectV3RouteReadiness,
  selectV3Session,
  selectV3WorkspaceSessions,
  type V3SessionPlanProjection,
  type V3SessionPlanRevisionProjection,
  type V3WorkspaceScope,
} from '../v3-runtime/v3-selectors'
import type { AgentModelPolicyRecord } from '../chat/types/chat'

export type DesktopStoreListener = () => void
export type DesktopWorkspaceScope = V3WorkspaceScope
export type DesktopSessionPlanProjection = V3SessionPlanProjection
export type DesktopSessionPlanRevisionProjection = V3SessionPlanRevisionProjection

const EMPTY_DESKTOP_MESSAGES: ChatMessageRecord[] = []
const EMPTY_DESKTOP_PLAN_REVISIONS: DesktopSessionPlanRevisionProjection[] = []

/**
 * Compatibility facade for older Desktop imports.
 *
 * The canonical V3 runtime projection is the Zustand vanilla store in
 * ../v3-runtime/v3-store. This module intentionally owns no mutable snapshot;
 * all reads subscribe to that store and all writes enter through V3 envelopes.
 */
export function useDesktopState(): DesktopState
export function useDesktopState<Selected>(selector: (state: DesktopState) => Selected): Selected
export function useDesktopState<Selected>(selector?: (state: DesktopState) => Selected): DesktopState | Selected {
  const runtime = useSyncExternalStore(subscribeV3Runtime, getV3RuntimeSnapshot, getV3RuntimeSnapshot)
  const state = runtime.desktop
  return selector ? selector(state) : state
}

export function getDesktopSnapshot(): DesktopState {
  return getV3RuntimeDesktopSnapshot()
}

export function useDesktopSession(sessionId: string | null | undefined): DesktopSessionRecord | null {
  return useSyncExternalStore(
    subscribeV3Runtime,
    () => selectV3Session(getV3RuntimeSnapshot(), sessionId),
    () => selectV3Session(getV3RuntimeSnapshot(), sessionId),
  )
}

export function useDesktopWorkspaceSessions(workspaceScope: DesktopWorkspaceScope): DesktopSessionRecord[] {
  return useSyncExternalStore(
    subscribeV3Runtime,
    () => selectV3WorkspaceSessions(getV3RuntimeSnapshot(), workspaceScope),
    () => selectV3WorkspaceSessions(getV3RuntimeSnapshot(), workspaceScope),
  )
}

export function useDesktopRouteReadiness(_workspaceScope: DesktopWorkspaceScope, sessionId: string | null | undefined): DesktopSessionReadinessRecord | null {
  return useSyncExternalStore(
    subscribeV3Runtime,
    () => selectV3RouteReadiness(getV3RuntimeSnapshot(), sessionId),
    () => selectV3RouteReadiness(getV3RuntimeSnapshot(), sessionId),
  )
}

export function useDesktopMessages(sessionId: string | null | undefined): ChatMessageRecord[] {
  return useSyncExternalStore(
    subscribeV3Runtime,
    () => selectV3Messages(getV3RuntimeSnapshot(), sessionId) ?? EMPTY_DESKTOP_MESSAGES,
    () => selectV3Messages(getV3RuntimeSnapshot(), sessionId) ?? EMPTY_DESKTOP_MESSAGES,
  )
}

export function useDesktopPreference(sessionId: string | null | undefined): ResolvedSessionPreference | null {
  return useSyncExternalStore(
    subscribeV3Runtime,
    () => selectV3Preference(getV3RuntimeSnapshot(), sessionId),
    () => selectV3Preference(getV3RuntimeSnapshot(), sessionId),
  )
}

export function useDesktopActiveRun(sessionId: string | null | undefined): DesktopRunIntentRecord | null {
  return useSyncExternalStore(
    subscribeV3Runtime,
    () => selectV3ActiveRun(getV3RuntimeSnapshot(), sessionId),
    () => selectV3ActiveRun(getV3RuntimeSnapshot(), sessionId),
  )
}

export function useDesktopAgentModelPolicy(sessionId: string | null | undefined): AgentModelPolicyRecord | null {
  return useSyncExternalStore(
    subscribeV3Runtime,
    () => selectV3AgentModelPolicy(getV3RuntimeSnapshot(), sessionId),
    () => selectV3AgentModelPolicy(getV3RuntimeSnapshot(), sessionId),
  )
}

export function useDesktopPlan(sessionId: string | null | undefined): DesktopSessionPlanProjection | null {
  return useSyncExternalStore(
    subscribeV3Runtime,
    () => selectV3Plan(getV3RuntimeSnapshot(), sessionId),
    () => selectV3Plan(getV3RuntimeSnapshot(), sessionId),
  )
}

export function useDesktopPlanRevisions(sessionId: string | null | undefined): DesktopSessionPlanRevisionProjection[] {
  return useSyncExternalStore(
    subscribeV3Runtime,
    () => selectV3PlanRevisions(getV3RuntimeSnapshot(), sessionId) ?? EMPTY_DESKTOP_PLAN_REVISIONS,
    () => selectV3PlanRevisions(getV3RuntimeSnapshot(), sessionId) ?? EMPTY_DESKTOP_PLAN_REVISIONS,
  )
}

export function subscribeDesktop(listener: DesktopStoreListener): () => void {
  return subscribeV3Runtime(listener)
}

export function replaceDesktopFromSnapshot(snapshot: DesktopDaemonSnapshot): DesktopState {
  return applyV3RuntimeEnvelope(createV3SnapshotEnvelope(snapshot, { mode: 'replace', receivedAt: Date.now() })).snapshot.desktop
}

export function mergeDesktopSnapshot(snapshot: DesktopDaemonSnapshot): DesktopState {
  return applyV3RuntimeEnvelope(createV3SnapshotEnvelope(snapshot, { mode: 'merge', receivedAt: Date.now() })).snapshot.desktop
}

export function applyDesktopDaemonEvent(event: DesktopDaemonEvent): DesktopState {
  return applyV3RuntimeEnvelope(createV3EventEnvelope({
    type: event.type,
    payload: event.payload,
    stream: event.stream,
    entityId: event.entityId,
    rev: event.rev,
    prevRev: event.prevRev,
    globalSeq: event.globalSeq,
    sourceSeq: event.sourceSeq,
    tsUnixMs: event.tsUnixMs,
  }, {
    receivedAt: Date.now(),
    source: { kind: 'runtime', transport: 'memory' },
  })).snapshot.desktop
}

export function applyDesktopDurableEventEnvelope(event: unknown): DesktopState {
  try {
    return applyV3RuntimeEnvelope(normalizeV3DurableEventEnvelope(event, {
      receivedAt: Date.now(),
      sourceKind: 'runtime',
      source: { kind: 'runtime', transport: 'memory' },
    })).snapshot.desktop
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error)
    return markDesktopStale(`durable event rejected: ${message}`)
  }
}

export function markDesktopStale(reason: string): DesktopState {
  return applyV3RuntimeEnvelope(createV3ConnectionStaleEnvelope(reason, { receivedAt: Date.now() })).snapshot.desktop
}

export function setDesktopConnectionStatus(status: DesktopStateStatus, error?: string | null): DesktopState {
  return applyV3RuntimeEnvelope(createV3ConnectionStatusEnvelope(status, { error, receivedAt: Date.now() })).snapshot.desktop
}
