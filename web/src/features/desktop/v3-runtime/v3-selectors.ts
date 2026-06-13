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
import type { DesktopSessionReadinessRecord, DesktopState } from '../state/desktop-state'
import type { V3RuntimeState } from './v3-store'

const EMPTY_MESSAGES: ChatMessageRecord[] = []
const EMPTY_PLAN_REVISIONS: V3SessionPlanRevisionProjection[] = []

export type V3WorkspaceScope = string | {
  workspacePath?: string | null
  workspacePaths?: Array<string | null | undefined>
}

export interface V3SessionPlanProjection {
  sessionId: string
  plan: DesktopSessionPlanRecord | null
  hasActivePlan: boolean
}

export interface V3SessionPlanRevisionProjection extends DesktopSessionPlanRevisionRecord {
  sessionId: string
}

export function selectV3DesktopState(runtime: V3RuntimeState): DesktopState {
  return runtime.desktop
}

export function selectV3Status(runtime: V3RuntimeState): DesktopState['status'] {
  return runtime.desktop.status
}

export function selectV3Session(runtime: V3RuntimeState, sessionId: string | null | undefined): DesktopSessionRecord | null {
  const normalizedSessionId = sessionId?.trim() ?? ''
  if (!normalizedSessionId) {
    return null
  }
  return selectDesktopSession(runtime.desktop, normalizedSessionId)
}

export function selectV3WorkspaceSessions(runtime: V3RuntimeState, workspaceScope: V3WorkspaceScope): DesktopSessionRecord[] {
  const workspaceKey = v3WorkspaceScopeKey(workspaceScope)
  const workspacePaths = new Set(workspaceKey ? workspaceKey.split('\u0000') : [])
  return runtime.desktop.sessionOrder
    .map((sessionId) => selectDesktopSession(runtime.desktop, sessionId))
    .filter((session): session is DesktopSessionRecord => Boolean(session))
    .filter((session) => workspacePaths.size === 0 || workspacePaths.has(session.workspacePath?.trim() ?? ''))
    .sort((left, right) => right.updatedAt - left.updatedAt || left.id.localeCompare(right.id))
}

export function selectV3RouteReadiness(runtime: V3RuntimeState, sessionId: string | null | undefined): DesktopSessionReadinessRecord | null {
  const normalizedSessionId = sessionId?.trim() ?? ''
  return normalizedSessionId ? runtime.desktop.routeReadinessBySessionId[normalizedSessionId] ?? null : null
}

export function selectV3Messages(runtime: V3RuntimeState, sessionId: string | null | undefined): ChatMessageRecord[] {
  const normalizedSessionId = sessionId?.trim() ?? ''
  return normalizedSessionId ? runtime.desktop.messagesBySessionId[normalizedSessionId] ?? EMPTY_MESSAGES : EMPTY_MESSAGES
}

export function selectV3Preference(runtime: V3RuntimeState, sessionId: string | null | undefined): ResolvedSessionPreference | null {
  const normalizedSessionId = sessionId?.trim() ?? ''
  return normalizedSessionId ? runtime.desktop.preferencesBySessionId[normalizedSessionId] ?? null : null
}

export function selectV3ActiveRun(runtime: V3RuntimeState, sessionId: string | null | undefined): DesktopRunIntentRecord | null {
  const normalizedSessionId = sessionId?.trim() ?? ''
  return normalizedSessionId ? runtime.desktop.runIntentsBySessionId[normalizedSessionId] ?? null : null
}

export function selectV3AgentModelPolicy(runtime: V3RuntimeState, sessionId: string | null | undefined): AgentModelPolicyRecord | null {
  const normalizedSessionId = sessionId?.trim() ?? ''
  return normalizedSessionId ? runtime.desktop.agentModelPolicyBySessionId[normalizedSessionId] ?? null : null
}

export function selectV3Plan(runtime: V3RuntimeState, sessionId: string | null | undefined): V3SessionPlanProjection | null {
  const normalizedSessionId = sessionId?.trim() ?? ''
  if (!normalizedSessionId) {
    return null
  }
  const plan = runtime.desktop.plansBySessionId[normalizedSessionId] ?? null
  return {
    sessionId: normalizedSessionId,
    plan,
    hasActivePlan: Boolean(plan?.id || plan?.plan || plan?.title),
  }
}

export function selectV3PlanRevisions(runtime: V3RuntimeState, sessionId: string | null | undefined): V3SessionPlanRevisionProjection[] {
  const normalizedSessionId = sessionId?.trim() ?? ''
  return normalizedSessionId
    ? (runtime.desktop.planRevisionsBySessionId[normalizedSessionId] ?? EMPTY_PLAN_REVISIONS).map((revision) => ({
        ...revision,
        sessionId: normalizedSessionId,
      }))
    : EMPTY_PLAN_REVISIONS
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

function v3WorkspaceScopeKey(workspaceScope: V3WorkspaceScope): string {
  return v3WorkspacePaths(workspaceScope).join('\u0000')
}

function v3WorkspacePaths(workspaceScope: V3WorkspaceScope): string[] {
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
