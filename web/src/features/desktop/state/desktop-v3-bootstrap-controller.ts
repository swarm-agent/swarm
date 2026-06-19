import { loadDesktopV3CacheActiveOwnerKey } from './desktop-v3-cache-active-owner'
import { readDesktopV3MessageTail, readDesktopV3MessageTails, readDesktopV3Owner } from './desktop-v3-cache-db'
import { bootstrapResponseToAction } from './desktop-v3-cache-wire'
import { dispatchDesktopV3Cache, getDesktopV3CacheSnapshot } from './desktop-v3-cache-store'
import { hydrateDesktopV3InitialSessions, resetDesktopV3InitialHydrateControllerForTests } from './desktop-v3-initial-hydrate-controller'
import { postDesktopV3SyncBootstrap, postDesktopV3SyncHydrate, type DesktopV3HydrateInput } from './desktop-v3-sync-api'
import type { DesktopV3CacheAction, DesktopV3CacheState, SyncSnapshotResponse, V3SessionProjection } from './desktop-v3-cache-types'
import type { PersistedDesktopV3MessageTailV1, PersistedDesktopV3OwnerV1 } from './desktop-v3-cache-persisted-types'

interface BootstrapDesktopV3SidebarDeps {
  preferredSessionId?: string
  postBootstrap?: () => Promise<SyncSnapshotResponse>
  postHydrate?: (input: DesktopV3HydrateInput) => Promise<SyncSnapshotResponse>
  dispatch?: (action: DesktopV3CacheAction) => void
  getSnapshot?: () => DesktopV3CacheState
  loadActiveOwnerKey?: () => string | undefined
  readOwner?: (ownerKey: string) => Promise<PersistedDesktopV3OwnerV1 | undefined>
  readMessageTail?: (ownerKey: string, sessionId: string) => Promise<PersistedDesktopV3MessageTailV1 | undefined>
  readMessageTails?: (ownerKey: string, sessionIds: string[]) => Promise<PersistedDesktopV3MessageTailV1[]>
}

let bootstrapInFlight: Promise<void> | null = null

export function bootstrapDesktopV3Sidebar(deps: BootstrapDesktopV3SidebarDeps = {}): Promise<void> {
  if (bootstrapInFlight) {
    return bootstrapInFlight
  }

  const dispatch = deps.dispatch ?? dispatchDesktopV3Cache
  const getSnapshot = deps.getSnapshot ?? getDesktopV3CacheSnapshot
  const postBootstrap = deps.postBootstrap ?? postDesktopV3SyncBootstrap
  const postHydrate = deps.postHydrate ?? postDesktopV3SyncHydrate
  const loadActiveOwnerKey = deps.loadActiveOwnerKey ?? loadDesktopV3CacheActiveOwnerKey
  const readOwner = deps.readOwner ?? readDesktopV3Owner
  const readMessageTail = deps.readMessageTail ?? readDesktopV3MessageTail
  const readMessageTails = deps.readMessageTails ?? readDesktopV3MessageTails

  let restoredOwnerKey: string | undefined
  let restoredSelectedMessageTail: PersistedDesktopV3MessageTailV1 | undefined
  bootstrapInFlight = restoreDesktopV3CacheFromActiveOwner({
    preferredSessionId: deps.preferredSessionId,
    dispatch,
    loadActiveOwnerKey,
    readOwner,
    readMessageTail,
    onRestored: (result) => {
      restoredOwnerKey = result.ownerKey
      restoredSelectedMessageTail = result.selectedMessageTail
    },
  })
    .then((restored) => {
      if (!restored) {
        dispatch({
          type: 'desktopSidebarBootstrap.update',
          patch: {
            status: 'loading',
            error: undefined,
            stale: undefined,
            source: undefined,
          },
        })
      }
      return postBootstrap()
    })
    .then(async (response) => {
      const preBootstrapCachedProjections = cloneProjections(getSnapshot().projectionsBySession)
      dispatch(bootstrapResponseToAction(response))
      dispatch({
        type: 'desktopSidebarBootstrap.update',
        patch: {
          status: 'ready',
          scopeId: response.scope_id,
          error: undefined,
          stale: false,
          source: 'network',
        },
      })
      await hydrateDesktopV3InitialSessions({
        sessionIds: response.session_order ?? [],
        bootstrapResponse: response,
        preBootstrapCachedProjections,
        selectedMessageTail: restoredSelectedMessageTail,
        preferredSessionId: deps.preferredSessionId,
        currentSelectedSessionId: getSnapshot().selectedSessionId,
        ownerKey: restoredOwnerKey,
        readMessageTails,
        postHydrate,
        dispatch,
      })
    })
    .catch((error: unknown) => {
      dispatch({
        type: 'desktopSidebarBootstrap.update',
        patch: {
          status: 'error',
          error: error instanceof Error ? error.message : String(error),
        },
      })
    })
    .finally(() => {
      bootstrapInFlight = null
    })

  return bootstrapInFlight
}

export async function restoreDesktopV3CacheFromActiveOwner(input: {
  preferredSessionId?: string
  dispatch: (action: DesktopV3CacheAction) => void
  loadActiveOwnerKey?: () => string | undefined
  readOwner?: (ownerKey: string) => Promise<PersistedDesktopV3OwnerV1 | undefined>
  readMessageTail?: (ownerKey: string, sessionId: string) => Promise<PersistedDesktopV3MessageTailV1 | undefined>
  onRestored?: (result: { ownerKey?: string; selectedMessageTail?: PersistedDesktopV3MessageTailV1 }) => void
}): Promise<boolean> {
  try {
    const ownerKey = input.loadActiveOwnerKey?.() ?? loadDesktopV3CacheActiveOwnerKey()
    if (!ownerKey) return false

    const owner = await (input.readOwner ?? readDesktopV3Owner)(ownerKey)
    if (!owner) {
      input.onRestored?.({ ownerKey })
      return false
    }

    const effectiveSessionId = input.preferredSessionId?.trim() || owner.selectedSessionId
    const selectedMessageTail = effectiveSessionId
      ? await (input.readMessageTail ?? readDesktopV3MessageTail)(owner.ownerKey, effectiveSessionId)
      : undefined

    input.dispatch({
      type: 'desktopV3Cache.restore',
      owner,
      selectedMessageTail,
      preferredSessionId: input.preferredSessionId,
    })
    input.onRestored?.({ ownerKey: owner.ownerKey, selectedMessageTail })
    return true
  } catch {
    return false
  }
}

export function resetDesktopV3BootstrapControllerForTests(): void {
  bootstrapInFlight = null
  resetDesktopV3InitialHydrateControllerForTests()
}

function cloneProjections(projectionsBySession: Record<string, V3SessionProjection>): Record<string, V3SessionProjection> {
  const output: Record<string, V3SessionProjection> = {}
  for (const [sessionId, projection] of Object.entries(projectionsBySession)) {
    output[sessionId] = { ...projection }
  }
  return output
}
