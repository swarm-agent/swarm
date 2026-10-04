import { backgroundRead } from '../../../app/background-read'
import { subscribeDesktopSessionReset } from '../../../app/api'
import { dispatchDesktopV3Cache, getDesktopV3CacheSnapshot } from './desktop-v3-cache-store'
import { buildDesktopV3ChildCardHydrateInput, buildDesktopV3SelectedSessionHydrateInput, postDesktopV3SyncHydrate } from './desktop-v3-sync-api'
import { hydrateResponseToAction, selectSession } from './desktop-v3-cache-wire'
import { isDesktopV3SessionTailReady, isDesktopV3SessionViewReady } from './desktop-v3-cache-selectors'

const inFlight = new Map<string, { promise: Promise<void>; abort: AbortController }>()
const childCardInFlight = new Map<string, Promise<void>>()
let selectedAbort: AbortController | null = null
let childAbort = new AbortController()
subscribeDesktopSessionReset(() => {
  childAbort.abort()
  childAbort = new AbortController()
  childCardInFlight.clear()
})

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
  const signal = childAbort.signal
  const promise = backgroundRead(() => postDesktopV3SyncHydrate(buildDesktopV3ChildCardHydrateInput([sessionId], options), signal), signal)
    .then((response) => {
      if (!signal.aborted) dispatchDesktopV3Cache(hydrateResponseToAction(response, [sessionId]))
    })
    .finally(() => {
      if (childCardInFlight.get(key) !== promise) return
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
  if (existing && !existing.abort.signal.aborted) return existing.promise

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
      if (!abort.signal.aborted) dispatchDesktopV3Cache(hydrateResponseToAction(response, [sessionId]))
    })
    .catch((error) => {
      if (isAbortError(error)) return
      throw error
    })
    .finally(() => {
      // A rapid A → B → A switch may have replaced this aborted request.
      if (inFlight.get(sessionId)?.abort === abort) {
        dispatchDesktopV3Cache({
          type: 'desktopV3Cache.markHydrateInFlight',
          sessionIds: [sessionId],
          inFlight: false,
        })
        inFlight.delete(sessionId)
      }
      if (selectedAbort === abort) selectedAbort = null
    })

  inFlight.set(sessionId, { promise, abort })
  return promise
}

function isAbortError(error: unknown): boolean {
  return error instanceof DOMException && error.name === 'AbortError'
}
