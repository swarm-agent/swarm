import { useSyncExternalStore } from 'react'

import type { DesktopState } from '../state/desktop-state'
import type {
  AgentModelPolicyRecord,
  ChatMessageRecord,
  ResolvedSessionPreference,
} from '../chat/types/chat'
import type {
  DesktopRunIntentRecord,
  DesktopSessionRecord,
} from '../types/realtime'
import type { DesktopSessionReadinessRecord } from '../state/desktop-state'
import {
  getV3RuntimeSnapshot,
  subscribeV3Runtime,
  type V3RuntimeState,
} from './v3-store'
import {
  selectV3ActiveRun,
  selectV3AgentModelPolicy,
  selectV3DesktopState,
  selectV3Messages,
  selectV3Plan,
  selectV3PlanRevisions,
  selectV3Preference,
  selectV3RouteReadiness,
  selectV3Session,
  selectV3Status,
  selectV3WorkspaceSessions,
  type V3SessionPlanProjection,
  type V3SessionPlanRevisionProjection,
  type V3WorkspaceScope,
} from './v3-selectors'

export function useV3RuntimeState(): V3RuntimeState
export function useV3RuntimeState<Selected>(selector: (state: V3RuntimeState) => Selected): Selected
export function useV3RuntimeState<Selected>(selector?: (state: V3RuntimeState) => Selected): V3RuntimeState | Selected {
  const state = useSyncExternalStore(subscribeV3Runtime, getV3RuntimeSnapshot, getV3RuntimeSnapshot)
  return selector ? selector(state) : state
}

export function useV3DesktopState(): DesktopState {
  return useV3RuntimeState(selectV3DesktopState)
}

export function useV3Status(): DesktopState['status'] {
  return useV3RuntimeState(selectV3Status)
}

export function useV3Session(sessionId: string | null | undefined): DesktopSessionRecord | null {
  return useV3RuntimeState((state) => selectV3Session(state, sessionId))
}

export function useV3WorkspaceSessions(workspaceScope: V3WorkspaceScope): DesktopSessionRecord[] {
  return useV3RuntimeState((state) => selectV3WorkspaceSessions(state, workspaceScope))
}

export function useV3RouteReadiness(sessionId: string | null | undefined): DesktopSessionReadinessRecord | null {
  return useV3RuntimeState((state) => selectV3RouteReadiness(state, sessionId))
}

export function useV3Messages(sessionId: string | null | undefined): ChatMessageRecord[] {
  return useV3RuntimeState((state) => selectV3Messages(state, sessionId))
}

export function useV3Preference(sessionId: string | null | undefined): ResolvedSessionPreference | null {
  return useV3RuntimeState((state) => selectV3Preference(state, sessionId))
}

export function useV3ActiveRun(sessionId: string | null | undefined): DesktopRunIntentRecord | null {
  return useV3RuntimeState((state) => selectV3ActiveRun(state, sessionId))
}

export function useV3AgentModelPolicy(sessionId: string | null | undefined): AgentModelPolicyRecord | null {
  return useV3RuntimeState((state) => selectV3AgentModelPolicy(state, sessionId))
}

export function useV3Plan(sessionId: string | null | undefined): V3SessionPlanProjection | null {
  return useV3RuntimeState((state) => selectV3Plan(state, sessionId))
}

export function useV3PlanRevisions(sessionId: string | null | undefined): V3SessionPlanRevisionProjection[] {
  return useV3RuntimeState((state) => selectV3PlanRevisions(state, sessionId))
}
