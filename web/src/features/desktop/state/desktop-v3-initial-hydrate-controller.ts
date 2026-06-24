import { hydrateResponseToAction } from './desktop-v3-cache-wire'
import { dispatchDesktopV3Cache, getDesktopV3CacheSnapshot } from './desktop-v3-cache-store'
import { hydrateResponseCompletesSession } from './desktop-v3-cache-reducer'
import { isDesktopV3SessionTailReady } from './desktop-v3-cache-selectors'
import {
  buildDesktopV3SelectedSessionHydrateInput,
  postDesktopV3SyncHydrate,
  type DesktopV3HydrateInput,
} from './desktop-v3-sync-api'
import type { DesktopV3CacheAction, DesktopV3CacheState, SyncSnapshotResponse, V3SessionProjection } from './desktop-v3-cache-types'

interface HydrateDesktopV3InitialSessionsInput {
  sessionIds: string[]
  scopeId?: string
  forceNetworkHydrate?: boolean
  bootstrapResponse?: SyncSnapshotResponse
  preBootstrapCachedProjections?: Record<string, V3SessionProjection>
  preferredSessionId?: string | null
  currentSelectedSessionId?: string
  postHydrate?: (input: DesktopV3HydrateInput, signal?: AbortSignal) => Promise<SyncSnapshotResponse>
  dispatch?: (action: DesktopV3CacheAction) => void
  getSnapshot?: () => DesktopV3CacheState
}

export interface DesktopV3SelectiveHydrationPlan {
  reusedSessionIds: string[]
  hydrateSessionIds: string[]
}

const inFlightBySessionSet = new Map<string, Promise<void>>()

export function hydrateDesktopV3InitialSessions(input: HydrateDesktopV3InitialSessionsInput): Promise<void> {
  const sessionIds = normalizeOrderedSessionIds(input.sessionIds)
  const preferredSessionId = input.preferredSessionId === null
    ? undefined
    : normalizeOptionalSessionId(input.preferredSessionId) ?? normalizeOptionalSessionId(input.currentSelectedSessionId)
  const inFlightKey = normalizeSessionIdSetKey(preferredSessionId ? [preferredSessionId] : [])
  const existing = inFlightBySessionSet.get(inFlightKey)
  if (existing) {
    return existing
  }

  const dispatch = input.dispatch ?? dispatchDesktopV3Cache
  const postHydrate = input.postHydrate ?? postDesktopV3SyncHydrate
  const getSnapshot = input.getSnapshot ?? getDesktopV3CacheSnapshot

  const promise = hydrateDesktopV3InitialSessionsUncached({
    ...input,
    sessionIds,
    preferredSessionId,
    postHydrate,
    dispatch,
    getSnapshot,
  }).finally(() => {
    inFlightBySessionSet.delete(inFlightKey)
  })

  inFlightBySessionSet.set(inFlightKey, promise)
  return promise
}

export function planDesktopV3Hydration(
  state: DesktopV3CacheState,
  sessionIds: string[],
): DesktopV3SelectiveHydrationPlan {
  const reusedSessionIds: string[] = []
  const hydrateSessionIds: string[] = []

  for (const sessionId of normalizeOrderedSessionIds(sessionIds)) {
    if (isDesktopV3SessionTailReady(state, sessionId)) {
      reusedSessionIds.push(sessionId)
    } else {
      hydrateSessionIds.push(sessionId)
    }
  }

  return { reusedSessionIds, hydrateSessionIds }
}

export function planDesktopV3SelectiveHydration(input: {
  bootstrapResponse: SyncSnapshotResponse
  preBootstrapCachedProjections?: Record<string, V3SessionProjection>
  preferredSessionId?: string | null
  currentSelectedSessionId?: string
  sessionIds?: string[]
  state?: DesktopV3CacheState
}): DesktopV3SelectiveHydrationPlan {
  const preferredSessionId = input.preferredSessionId === null
    ? undefined
    : normalizeOptionalSessionId(input.preferredSessionId) ?? normalizeOptionalSessionId(input.currentSelectedSessionId)
  if (!preferredSessionId) return { reusedSessionIds: [], hydrateSessionIds: [] }
  return planDesktopV3Hydration(input.state ?? getDesktopV3CacheSnapshot(), [preferredSessionId])
}

export function buildPostRealtimeConnectHydrationSnapshot(
  base: SyncSnapshotResponse,
  cache: DesktopV3CacheState,
  scopeId: string,
): SyncSnapshotResponse {
  return buildPostReconnectHydrationSnapshot(base, cache, scopeId)
}

export function buildPostReconnectHydrationSnapshot(
  base: SyncSnapshotResponse,
  cache: DesktopV3CacheState,
  scopeId: string,
): SyncSnapshotResponse {
  const sessionIds = normalizeOrderedSessionIds(cache.sessionOrderByScope[scopeId] ?? [])
  return {
    ...base,
    scope_id: scopeId,
    session_order: sessionIds,
    sessions_by_id: Object.fromEntries(sessionIds.flatMap((sessionId) => {
      const session = cache.sessionsById[sessionId]
      return session?.kind === 'full' ? [[sessionId, session.session] as const] : []
    })),
    projections_by_session: Object.fromEntries(sessionIds.flatMap((sessionId) => {
      const projection = cache.projectionsBySession[sessionId]
      return projection ? [[sessionId, projection] as const] : []
    })),
    tombstones_by_session: { ...cache.tombstonesBySession },
  }
}

export function resetDesktopV3InitialHydrateControllerForTests(): void {
  inFlightBySessionSet.clear()
}

async function hydrateDesktopV3InitialSessionsUncached(
  input: HydrateDesktopV3InitialSessionsInput & {
    sessionIds: string[]
    preferredSessionId?: string | null
    postHydrate: (input: DesktopV3HydrateInput, signal?: AbortSignal) => Promise<SyncSnapshotResponse>
    dispatch: (action: DesktopV3CacheAction) => void
    getSnapshot: () => DesktopV3CacheState
  },
): Promise<void> {
  const { dispatch, postHydrate, getSnapshot } = input
  const selectedSessionId = normalizeOptionalSessionId(input.preferredSessionId)

  if (!selectedSessionId) {
    dispatchInitialHydrateReady(dispatch, [], [], input.scopeId ?? input.bootstrapResponse?.scope_id)
    return
  }

  dispatch({
    type: 'desktopInitialHydrate.update',
    patch: {
      status: 'loading',
      requestedSessionIds: [selectedSessionId],
      hydratedSessionIds: [],
      error: undefined,
      stale: undefined,
      source: undefined,
    },
  })

  const completedSessionIds: string[] = []
  const failedErrors: string[] = []
  const selectedPlan = input.forceNetworkHydrate
    ? { reusedSessionIds: [], hydrateSessionIds: [selectedSessionId] }
    : planDesktopV3Hydration(getSnapshot(), [selectedSessionId])

  if (selectedPlan.reusedSessionIds.length > 0 || selectedPlan.hydrateSessionIds.length > 0) {
    dispatch({
      type: 'desktopV3Cache.applyHydrationPlan',
      reusedSessionIds: selectedPlan.reusedSessionIds,
      hydrateSessionIds: selectedPlan.hydrateSessionIds,
    })
  }

  const selectedHydrateId = selectedPlan.hydrateSessionIds[0]
  if (selectedHydrateId) {
    await hydrateSelectedSession({ sessionId: selectedHydrateId, postHydrate, dispatch, completedSessionIds, failedErrors })
  } else {
    completedSessionIds.push(...selectedPlan.reusedSessionIds)
  }

  if (failedErrors.length > 0) {
    dispatch({
      type: 'desktopInitialHydrate.update',
      patch: {
        status: 'error',
        requestedSessionIds: [selectedSessionId],
        hydratedSessionIds: normalizeOrderedSessionIds(completedSessionIds),
        error: failedErrors.join('; '),
      },
    })
    return
  }

  dispatchInitialHydrateReady(
    dispatch,
    [selectedSessionId],
    normalizeOrderedSessionIds(completedSessionIds),
    input.scopeId ?? input.bootstrapResponse?.scope_id,
  )
}

async function hydrateSelectedSession(input: {
  sessionId: string
  postHydrate: (input: DesktopV3HydrateInput, signal?: AbortSignal) => Promise<SyncSnapshotResponse>
  dispatch: (action: DesktopV3CacheAction) => void
  completedSessionIds: string[]
  failedErrors: string[]
}): Promise<void> {
  try {
    input.dispatch({ type: 'desktopV3Cache.markHydrateInFlight', sessionIds: [input.sessionId], inFlight: true })
    const response = await input.postHydrate(buildDesktopV3SelectedSessionHydrateInput(input.sessionId))
    input.dispatch(hydrateResponseToAction(response, [input.sessionId]))
    input.completedSessionIds.push(...completedHydrateSessionIds(response, [input.sessionId]))
  } catch (error) {
    input.failedErrors.push(error instanceof Error ? error.message : String(error))
  } finally {
    input.dispatch({ type: 'desktopV3Cache.markHydrateInFlight', sessionIds: [input.sessionId], inFlight: false })
  }
}

function completedHydrateSessionIds(response: SyncSnapshotResponse, requestedSessionIds: string[]): string[] {
  return normalizeOrderedSessionIds(requestedSessionIds)
    .filter((sessionId) => hydrateResponseCompletesSession(response, sessionId))
}

function dispatchInitialHydrateReady(
  dispatch: (action: DesktopV3CacheAction) => void,
  requestedSessionIds: string[],
  hydratedSessionIds: string[],
  scopeId: string | undefined,
): void {
  dispatch({
    type: 'desktopInitialHydrate.update',
    patch: {
      status: 'ready',
      requestedSessionIds,
      hydratedSessionIds,
      scopeId,
      error: undefined,
      stale: false,
      source: 'network',
    },
  })
}

function normalizeOrderedSessionIds(sessionIds: string[]): string[] {
  const seen = new Set<string>()
  const normalized: string[] = []
  for (const rawSessionId of sessionIds) {
    const sessionId = rawSessionId.trim()
    if (!sessionId || seen.has(sessionId)) continue
    seen.add(sessionId)
    normalized.push(sessionId)
  }
  return normalized
}

function normalizeOptionalSessionId(sessionId: string | null | undefined): string | undefined {
  const normalized = sessionId?.trim()
  return normalized || undefined
}

function normalizeSessionIdSetKey(sessionIds: string[]): string {
  return [...normalizeOrderedSessionIds(sessionIds)].sort().join('\u0000')
}

