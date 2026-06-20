import { hydrateResponseToAction } from './desktop-v3-cache-wire'
import { dispatchDesktopV3Cache } from './desktop-v3-cache-store'
import { hydrateResponseCompletesSession } from './desktop-v3-cache-reducer'
import {
  buildDesktopV3InitialHydrateInput,
  postDesktopV3SyncHydrate,
  type DesktopV3HydrateInput,
} from './desktop-v3-sync-api'
import type { DesktopV3CacheAction, SyncSnapshotResponse, V3SessionProjection } from './desktop-v3-cache-types'
import type { PersistedDesktopV3MessageTailV1 } from './desktop-v3-cache-persisted-types'

interface HydrateDesktopV3InitialSessionsInput {
  sessionIds: string[]
  bootstrapResponse?: SyncSnapshotResponse
  preBootstrapCachedProjections?: Record<string, V3SessionProjection>
  selectedMessageTail?: PersistedDesktopV3MessageTailV1
  preferredSessionId?: string | null
  currentSelectedSessionId?: string
  ownerKey?: string
  readMessageTails?: (ownerKey: string, sessionIds: string[]) => Promise<PersistedDesktopV3MessageTailV1[]>
  postHydrate?: (input: DesktopV3HydrateInput) => Promise<SyncSnapshotResponse>
  dispatch?: (action: DesktopV3CacheAction) => void
}

export interface DesktopV3SelectiveHydrationPlan {
  reusedSessionIds: string[]
  hydrateSessionIds: string[]
}

const MAX_BACKGROUND_HYDRATE_BATCH_SIZE = 10
const MAX_BACKGROUND_HYDRATE_CONCURRENCY = 2

const inFlightBySessionSet = new Map<string, Promise<void>>()

export function hydrateDesktopV3InitialSessions(input: HydrateDesktopV3InitialSessionsInput): Promise<void> {
  const sessionIds = normalizeOrderedSessionIds(input.sessionIds)
  const preferredSessionId = input.preferredSessionId === null
    ? undefined
    : normalizeOptionalSessionId(input.preferredSessionId) ?? normalizeOptionalSessionId(input.currentSelectedSessionId)
  const inFlightKey = normalizeSessionIdSetKey(preferredSessionId ? [preferredSessionId, ...sessionIds] : sessionIds)
  const existing = inFlightBySessionSet.get(inFlightKey)
  if (existing) {
    return existing
  }

  const dispatch = input.dispatch ?? dispatchDesktopV3Cache
  const postHydrate = input.postHydrate ?? postDesktopV3SyncHydrate

  const promise = hydrateDesktopV3InitialSessionsUncached({
    ...input,
    sessionIds,
    preferredSessionId,
    postHydrate,
    dispatch,
  }).finally(() => {
    inFlightBySessionSet.delete(inFlightKey)
  })

  inFlightBySessionSet.set(inFlightKey, promise)
  return promise
}

export function planDesktopV3SelectiveHydration(input: {
  bootstrapResponse: SyncSnapshotResponse
  preBootstrapCachedProjections?: Record<string, V3SessionProjection>
  persistedTailsBySession?: Record<string, PersistedDesktopV3MessageTailV1 | undefined>
  preferredSessionId?: string | null
  currentSelectedSessionId?: string
  sessionIds?: string[]
}): DesktopV3SelectiveHydrationPlan {
  const response = input.bootstrapResponse
  const preferredSessionId = input.preferredSessionId === null
    ? undefined
    : normalizeOptionalSessionId(input.preferredSessionId) ?? normalizeOptionalSessionId(input.currentSelectedSessionId)
  const bootstrapOrder = normalizeOrderedSessionIds(input.sessionIds ?? response.session_order ?? [])
  const bootstrapOrderSet = new Set(bootstrapOrder)
  const candidateSessionIds = preferredSessionId && !bootstrapOrderSet.has(preferredSessionId)
    ? [preferredSessionId, ...bootstrapOrder]
    : bootstrapOrder
  const reusedSessionIds: string[] = []
  const hydrateSessionIds: string[] = []

  for (const sessionId of candidateSessionIds) {
    if (isTombstoned(response, sessionId)) continue
    if (isReusableCachedSession({
      sessionId,
      response,
      cachedProjection: input.preBootstrapCachedProjections?.[sessionId],
      tail: input.persistedTailsBySession?.[sessionId],
    })) {
      reusedSessionIds.push(sessionId)
    } else {
      hydrateSessionIds.push(sessionId)
    }
  }

  return { reusedSessionIds, hydrateSessionIds }
}

export function resetDesktopV3InitialHydrateControllerForTests(): void {
  inFlightBySessionSet.clear()
}

async function hydrateDesktopV3InitialSessionsUncached(
  input: HydrateDesktopV3InitialSessionsInput & {
    sessionIds: string[]
    preferredSessionId?: string | null
    postHydrate: (input: DesktopV3HydrateInput) => Promise<SyncSnapshotResponse>
    dispatch: (action: DesktopV3CacheAction) => void
  },
): Promise<void> {
  const { sessionIds, dispatch, postHydrate } = input
  const selectedSessionId = input.preferredSessionId
  const bootstrapResponse = input.bootstrapResponse

  if (sessionIds.length === 0 && !selectedSessionId) {
    dispatchInitialHydrateReady(dispatch, [], [], undefined)
    return
  }

  dispatch({
    type: 'desktopInitialHydrate.update',
    patch: {
      status: 'loading',
      requestedSessionIds: selectedSessionId ? normalizeOrderedSessionIds([selectedSessionId, ...sessionIds]) : sessionIds,
      hydratedSessionIds: [],
      error: undefined,
      stale: undefined,
      source: undefined,
    },
  })

  const tailsBySession: Record<string, PersistedDesktopV3MessageTailV1> = {}
  if (input.selectedMessageTail) {
    tailsBySession[input.selectedMessageTail.sessionId] = input.selectedMessageTail
  }

  const completedSessionIds: string[] = []
  const failedErrors: string[] = []
  const selectedPlan = bootstrapResponse && selectedSessionId
    ? planDesktopV3SelectiveHydration({
      bootstrapResponse,
      preBootstrapCachedProjections: input.preBootstrapCachedProjections,
      persistedTailsBySession: tailsBySession,
      preferredSessionId: selectedSessionId,
      sessionIds: [selectedSessionId],
    })
    : { reusedSessionIds: [], hydrateSessionIds: selectedSessionId && !sessionIds.includes(selectedSessionId) ? [selectedSessionId] : [] }

  if (selectedPlan.reusedSessionIds.length > 0 || selectedPlan.hydrateSessionIds.length > 0) {
    dispatch({ type: 'desktopV3Cache.applyHydrationPlan', reusedSessionIds: selectedPlan.reusedSessionIds, hydrateSessionIds: selectedPlan.hydrateSessionIds })
  }

  const selectedHydrateId = selectedPlan.hydrateSessionIds[0]
  if (selectedHydrateId) {
    await hydrateBatch({ sessionIds: [selectedHydrateId], postHydrate, dispatch, completedSessionIds, failedErrors })
  }

  const backgroundSessionIds = sessionIds.filter((sessionId) => sessionId !== selectedHydrateId)
  if (input.ownerKey && input.readMessageTails && backgroundSessionIds.length > 0) {
    const tails = await input.readMessageTails(input.ownerKey, backgroundSessionIds)
    for (const tail of tails) tailsBySession[tail.sessionId] = tail
    if (tails.length > 0) {
      dispatch({ type: 'desktopV3Cache.restoreMessageTails', tails })
    }
  }

  const plan = bootstrapResponse
    ? planDesktopV3SelectiveHydration({
      bootstrapResponse,
      preBootstrapCachedProjections: input.preBootstrapCachedProjections,
      persistedTailsBySession: tailsBySession,
      preferredSessionId: input.preferredSessionId,
      currentSelectedSessionId: input.currentSelectedSessionId,
      sessionIds,
    })
    : { reusedSessionIds: [], hydrateSessionIds: sessionIds }

  const remainingHydrateIds = plan.hydrateSessionIds.filter((sessionId) => sessionId !== selectedHydrateId)
  const reusedSessionIds = plan.reusedSessionIds.filter((sessionId) => sessionId !== selectedHydrateId)
  if (reusedSessionIds.length > 0 || remainingHydrateIds.length > 0) {
    dispatch({ type: 'desktopV3Cache.applyHydrationPlan', reusedSessionIds, hydrateSessionIds: remainingHydrateIds })
  }

  await hydrateBatches({
    sessionIds: remainingHydrateIds,
    postHydrate,
    dispatch,
    completedSessionIds,
    failedErrors,
  })

  if (failedErrors.length > 0) {
    dispatch({
      type: 'desktopInitialHydrate.update',
      patch: {
        status: 'error',
        requestedSessionIds: normalizeOrderedSessionIds([...(selectedHydrateId ? [selectedHydrateId] : []), ...remainingHydrateIds]),
        hydratedSessionIds: normalizeOrderedSessionIds(completedSessionIds),
        error: failedErrors.join('; '),
      },
    })
    return
  }

  dispatchInitialHydrateReady(
    dispatch,
    normalizeOrderedSessionIds([...(selectedHydrateId ? [selectedHydrateId] : []), ...remainingHydrateIds]),
    normalizeOrderedSessionIds([...completedSessionIds, ...plan.reusedSessionIds]),
    bootstrapResponse?.scope_id,
  )
}

async function hydrateBatches(input: {
  sessionIds: string[]
  postHydrate: (input: DesktopV3HydrateInput) => Promise<SyncSnapshotResponse>
  dispatch: (action: DesktopV3CacheAction) => void
  completedSessionIds: string[]
  failedErrors: string[]
}): Promise<void> {
  const batches = chunk(input.sessionIds, MAX_BACKGROUND_HYDRATE_BATCH_SIZE)
  let nextBatchIndex = 0
  const worker = async () => {
    while (nextBatchIndex < batches.length) {
      const batch = batches[nextBatchIndex++]
      await hydrateBatch({ ...input, sessionIds: batch })
    }
  }
  await Promise.all(Array.from({ length: Math.min(MAX_BACKGROUND_HYDRATE_CONCURRENCY, batches.length) }, worker))
}

async function hydrateBatch(input: {
  sessionIds: string[]
  postHydrate: (input: DesktopV3HydrateInput) => Promise<SyncSnapshotResponse>
  dispatch: (action: DesktopV3CacheAction) => void
  completedSessionIds: string[]
  failedErrors: string[]
}): Promise<void> {
  if (input.sessionIds.length === 0) return
  try {
    const response = await input.postHydrate(buildDesktopV3InitialHydrateInput(input.sessionIds))
    input.dispatch(hydrateResponseToAction(response, input.sessionIds))
    input.completedSessionIds.push(...completedHydrateSessionIds(response, input.sessionIds))
  } catch (error) {
    input.failedErrors.push(error instanceof Error ? error.message : String(error))
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

function isReusableCachedSession(input: {
  sessionId: string
  response: SyncSnapshotResponse
  cachedProjection?: V3SessionProjection
  tail?: PersistedDesktopV3MessageTailV1
}): boolean {
  const remoteSession = input.response.sessions_by_id?.[input.sessionId]
  const remoteProjection = input.response.projections_by_session?.[input.sessionId]
  const tail = input.tail
  const cachedProjection = input.cachedProjection
  if (!tail || !cachedProjection || !remoteSession || !remoteProjection) return false
  if (isTombstoned(input.response, input.sessionId)) return false
  if (!isSafeNumber(tail.sourceMessageCount) || !isSafeNumber(tail.sourceLastMessageAt) || !isSafeNumber(tail.sourceProjectionHighWatermarkSeq)) return false
  return tail.sourceMessageCount === remoteSession.message_count
    && tail.sourceLastMessageAt === remoteSession.last_message_at
    && tail.sourceProjectionHighWatermarkSeq === remoteProjection.projection_high_watermark_seq
    && cachedProjection.last_event_seq === remoteProjection.last_event_seq
    && cachedProjection.projection_high_watermark_seq === remoteProjection.projection_high_watermark_seq
}

function isTombstoned(response: SyncSnapshotResponse, sessionId: string): boolean {
  return response.tombstones_by_session?.[sessionId] !== undefined
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

function isSafeNumber(value: unknown): value is number {
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= 0
}

function chunk<T>(values: T[], size: number): T[][] {
  const output: T[][] = []
  for (let index = 0; index < values.length; index += size) {
    output.push(values.slice(index, index + size))
  }
  return output
}
