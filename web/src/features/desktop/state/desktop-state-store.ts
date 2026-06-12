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
  return useDesktopState((state) => normalizedSessionId ? state.sessionsById[normalizedSessionId] ?? null : null)
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

export function applyDesktopDaemonEvent(event: DesktopDaemonEvent): DesktopState {
  return dispatchDesktopState({ type: 'daemon/event', event })
}

export function markDesktopStale(reason: string): DesktopState {
  return dispatchDesktopState({ type: 'connection/stale', reason })
}

export function setDesktopConnectionStatus(status: DesktopStateStatus, error?: string | null): DesktopState {
  return dispatchDesktopState({ type: 'connection/status', status, error })
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
    .map((sessionId) => state.sessionsById[sessionId])
    .filter((session): session is DesktopSessionRecord => Boolean(session))
    .filter((session) => workspacePaths.size === 0 || workspacePaths.has(session.workspacePath?.trim() ?? ''))
  return [...sessions].sort((left, right) => right.updatedAt - left.updatedAt || left.id.localeCompare(right.id))
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
