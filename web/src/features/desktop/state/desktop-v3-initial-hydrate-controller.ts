import { hydrateResponseToAction } from './desktop-v3-cache-wire'
import { dispatchDesktopV3Cache } from './desktop-v3-cache-store'
import {
  buildDesktopV3InitialHydrateInput,
  postDesktopV3SyncHydrate,
  type DesktopV3HydrateInput,
} from './desktop-v3-sync-api'
import type { DesktopV3CacheAction, SyncSnapshotResponse } from './desktop-v3-cache-types'

interface HydrateDesktopV3InitialSessionsInput {
  sessionIds: string[]
  postHydrate?: (input: DesktopV3HydrateInput) => Promise<SyncSnapshotResponse>
  dispatch?: (action: DesktopV3CacheAction) => void
}

const inFlightBySessionSet = new Map<string, Promise<void>>()

export function hydrateDesktopV3InitialSessions(input: HydrateDesktopV3InitialSessionsInput): Promise<void> {
  const sessionIds = normalizeOrderedSessionIds(input.sessionIds)
  const inFlightKey = sessionIds.join('\u0000')
  const existing = inFlightBySessionSet.get(inFlightKey)
  if (existing) {
    return existing
  }

  const dispatch = input.dispatch ?? dispatchDesktopV3Cache
  const postHydrate = input.postHydrate ?? postDesktopV3SyncHydrate

  if (sessionIds.length === 0) {
    dispatch({
      type: 'desktopInitialHydrate.update',
      patch: {
        status: 'ready',
        requestedSessionIds: [],
        hydratedSessionIds: [],
        scopeId: undefined,
        error: undefined,
      },
    })
    return Promise.resolve()
  }

  dispatch({
    type: 'desktopInitialHydrate.update',
    patch: {
      status: 'loading',
      requestedSessionIds: sessionIds,
      hydratedSessionIds: [],
      error: undefined,
    },
  })

  const promise = postHydrate(buildDesktopV3InitialHydrateInput(sessionIds))
    .then((response) => {
      dispatch(hydrateResponseToAction(response, sessionIds))
      dispatch({
        type: 'desktopInitialHydrate.update',
        patch: {
          status: 'ready',
          requestedSessionIds: sessionIds,
          hydratedSessionIds: Object.keys(response.messages_by_session ?? {}),
          scopeId: response.scope_id,
          error: undefined,
        },
      })
    })
    .catch((error: unknown) => {
      dispatch({
        type: 'desktopInitialHydrate.update',
        patch: {
          status: 'error',
          requestedSessionIds: sessionIds,
          error: error instanceof Error ? error.message : String(error),
        },
      })
    })
    .finally(() => {
      inFlightBySessionSet.delete(inFlightKey)
    })

  inFlightBySessionSet.set(inFlightKey, promise)
  return promise
}

export function resetDesktopV3InitialHydrateControllerForTests(): void {
  inFlightBySessionSet.clear()
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
