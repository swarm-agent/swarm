import { ChildCardHydrationQueue } from './child-card-hydration-queue'
import type { SyncSnapshotResponse } from './desktop-v3-cache-types'
import { subscribeDesktopSessionReset } from '../../../app/api'
import { dispatchDesktopV3Cache, getDesktopV3CacheSnapshot } from './desktop-v3-cache-store'
import { buildDesktopV3ChildCardHydrateInput, buildDesktopV3SelectedSessionHydrateInput, postDesktopV3SyncHydrate } from './desktop-v3-sync-api'
import { assertDesktopV3SnapshotIdentities, hydrateResponseToAction, selectSession } from './desktop-v3-cache-wire'
import { isDesktopV3SessionTailReady, isDesktopV3SessionViewReady } from './desktop-v3-cache-selectors'

const inFlight = new Map<string, { promise: Promise<void>; abort: AbortController }>()
const childCardInFlight = new Map<string, Promise<void>>()
let selectedAbort: AbortController | null = null
let childAbort = new AbortController()
const childQueues = new Map<string, ChildCardHydrationQueue<void>>()
function childQueue(options: { activePlan?: boolean; permissionSummary?: boolean }) {
  const key = `${options.activePlan === true}:${options.permissionSummary === true}`
  let queue = childQueues.get(key)
  if (!queue) {
    queue = new ChildCardHydrationQueue(async (ids, signal, currentIds) => {
      const input = buildDesktopV3ChildCardHydrateInput(ids, options)
      // Cards require current questions/plans, not provider/media runtime setup.
      input.resources.session_view = false
      input.resources.permission_details = true
      const response = await postDesktopV3SyncHydrate(input, signal)
      for (const id of Object.keys(response.sessions_by_id)) {
        if (!Array.isArray(response.session_views_by_id?.[id]?.pending_permissions)) throw new Error('Pending request details unavailable; update the daemon and retry')
      }
      const current = currentIds()
      if (!signal.aborted && current.length) dispatchDesktopV3Cache(hydrateResponseToAction(retainCurrentChildHydration(response, ids, current), current))
    })
    childQueues.set(key, queue)
  }
  return queue
}
// Preserve the server's opaque scope/cursor while discarding canceled consumers'
// resources. Reject out-of-request identities before filtering, never mask them.
export function retainCurrentChildHydration(response: SyncSnapshotResponse, requested: string[], current: string[]): SyncSnapshotResponse {
  assertDesktopV3SnapshotIdentities(response)
  const allowed = new Set(requested)
  const retained = new Set(current)
  const result = { ...response }
  for (const field of ['session_order', 'active_session_ids'] as const) {
    const ids = response[field]
    if (ids?.some(id => !allowed.has(id))) throw new Error('Unexpected session in child hydration')
    if (ids) result[field] = ids.filter(id => retained.has(id))
  }
  for (const field of ['sessions_by_id', 'projections_by_session', 'messages_by_session', 'events_by_session', 'run_intents_by_session', 'current_run_state_by_session', 'permission_summaries_by_session', 'session_views_by_id', 'known_sessions', 'tombstones_by_session', 'history_manifests_by_session'] as const) {
    const values = response[field]
    if (!values) continue
    if (Object.keys(values).some(id => !allowed.has(id))) throw new Error('Unexpected session in child hydration')
    Object.assign(result, { [field]: Object.fromEntries(Object.entries(values).filter(([id]) => retained.has(id))) })
  }
  return result
}
subscribeDesktopSessionReset(() => {
  for (const request of inFlight.values()) request.abort.abort()
  inFlight.clear()
  selectedAbort = null
  childAbort.abort()
  childAbort = new AbortController()
  childCardInFlight.clear()
})

export function hydrateDesktopV3ChildCard(
  rawSessionId: string,
  options: { activePlan?: boolean; permissionSummary?: boolean; force?: boolean } = {},
  callerSignal?: AbortSignal,
): Promise<void> {
  const sessionId = rawSessionId.trim()
  if (!sessionId) return Promise.resolve()
  const state = getDesktopV3CacheSnapshot()
  const hasRequestedSummary = options.permissionSummary !== true || state.permissionSummaryBySessionId[sessionId] !== undefined
  const hasRequestedPlan = options.activePlan !== true || state.hasActivePlanBySession[sessionId] !== undefined
  if (!options.force && state.sessionsById[sessionId] && state.sessionViewsById[sessionId]?.pending_permissions !== undefined && hasRequestedSummary && hasRequestedPlan) {
    return Promise.resolve()
  }
  const key = `${sessionId}:${options.activePlan === true}:${options.permissionSummary === true}`
  const existing = childCardInFlight.get(key)
  if (existing && !callerSignal && !options.force) return existing

  dispatchDesktopV3Cache({
    type: 'desktopV3Cache.markHydrateInFlight',
    sessionIds: [sessionId],
    inFlight: true,
  })
  const signal = callerSignal ? AbortSignal.any([childAbort.signal, callerSignal]) : childAbort.signal
  const promise = childQueue(options).enqueue(sessionId, signal)
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

  // Cancel the previous selection even when the destination is already cached.
  for (const [id, request] of inFlight) {
    if (id !== sessionId) request.abort.abort()
  }
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
