import { dispatchDesktopV3Cache, getDesktopV3CacheSnapshot } from './desktop-v3-cache-store'
import { buildDesktopV3ChildCardHydrateInput, buildDesktopV3SelectedSessionHydrateInput, postDesktopV3SyncHydrate } from './desktop-v3-sync-api'
import { hydrateResponseToAction, selectSession } from './desktop-v3-cache-wire'
import { isDesktopV3SessionTailReady, isDesktopV3SessionViewReady } from './desktop-v3-cache-selectors'

const inFlight = new Map<string, Promise<void>>()
const childCardInFlight = new Map<string, Promise<void>>()
let selectedAbort: AbortController | null = null

export function hydrateDesktopV3ChildCard(
  rawSessionId: string,
  options: { activePlan?: boolean; permissionSummary?: boolean } = {},
): Promise<void> {
  const sessionId = rawSessionId.trim()
  if (!sessionId) return Promise.resolve()
  const state = getDesktopV3CacheSnapshot()
  const hasRequestedSummary = options.permissionSummary !== true || state.permissionSummaryBySessionId[sessionId] !== undefined
  const hasRequestedPlan = options.activePlan !== true || state.hasActivePlanBySession[sessionId] !== undefined
  if (state.sessionsById[sessionId] && isDesktopV3SessionViewReady(state, sessionId) && hasRequestedSummary && hasRequestedPlan) {
    return Promise.resolve()
  }
  const key = `${sessionId}:${options.activePlan === true}:${options.permissionSummary === true}`
  const existing = childCardInFlight.get(key)
  if (existing) return existing

  dispatchDesktopV3Cache({
    type: 'desktopV3Cache.markHydrateInFlight',
    sessionIds: [sessionId],
    inFlight: true,
  })
  const promise = postDesktopV3SyncHydrate(buildDesktopV3ChildCardHydrateInput([sessionId], options))
    .then((response) => {
      dispatchDesktopV3Cache(hydrateResponseToAction(response, [sessionId]))
    })
    .finally(() => {
      dispatchDesktopV3Cache({
        type: 'desktopV3Cache.markHydrateInFlight',
        sessionIds: [sessionId],
        inFlight: false,
      })
      childCardInFlight.delete(key)
    })
  childCardInFlight.set(key, promise)
  return promise
}

export function selectAndHydrateDesktopV3Session(rawSessionId: string): Promise<void> {
  const sessionId = rawSessionId.trim()
  if (!sessionId) return Promise.resolve()

  dispatchDesktopV3Cache(selectSession(sessionId))

  const state = getDesktopV3CacheSnapshot()
  if (isDesktopV3SessionTailReady(state, sessionId)
    && isDesktopV3SessionViewReady(state, sessionId)) {
    return Promise.resolve()
  }

  const existing = inFlight.get(sessionId)
  if (existing) return existing

  selectedAbort?.abort()
  const abort = new AbortController()
  selectedAbort = abort

  dispatchDesktopV3Cache({
    type: 'desktopV3Cache.markHydrateInFlight',
    sessionIds: [sessionId],
    inFlight: true,
  })

  const promise = postDesktopV3SyncHydrate(
    buildDesktopV3SelectedSessionHydrateInput(sessionId),
    abort.signal,
  )
    .then((response) => {
      dispatchDesktopV3Cache(hydrateResponseToAction(response, [sessionId]))
    })
    .catch((error) => {
      if (isAbortError(error)) return
      throw error
    })
    .finally(() => {
      dispatchDesktopV3Cache({
        type: 'desktopV3Cache.markHydrateInFlight',
        sessionIds: [sessionId],
        inFlight: false,
      })
      inFlight.delete(sessionId)
      if (selectedAbort === abort) selectedAbort = null
    })

  inFlight.set(sessionId, promise)
  return promise
}

function isAbortError(error: unknown): boolean {
  return error instanceof DOMException && error.name === 'AbortError'
}
