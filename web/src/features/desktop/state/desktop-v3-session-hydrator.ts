import { dispatchDesktopV3Cache, getDesktopV3CacheSnapshot } from './desktop-v3-cache-store'
import { buildDesktopV3SelectedSessionHydrateInput, postDesktopV3SyncHydrate } from './desktop-v3-sync-api'
import { hydrateResponseToAction, selectSession } from './desktop-v3-cache-wire'
import { isDesktopV3SessionTailReady, isDesktopV3SessionViewReady } from './desktop-v3-cache-selectors'

const inFlight = new Map<string, Promise<void>>()
let selectedAbort: AbortController | null = null

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
