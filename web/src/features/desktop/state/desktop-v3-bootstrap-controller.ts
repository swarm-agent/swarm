import { loadDesktopV3CacheActiveOwnerKey } from './desktop-v3-cache-active-owner'
import { readDesktopV3MessageTail, readDesktopV3Owner } from './desktop-v3-cache-db'
import { bootstrapResponseToAction } from './desktop-v3-cache-wire'
import { dispatchDesktopV3Cache } from './desktop-v3-cache-store'
import { hydrateDesktopV3InitialSessions, resetDesktopV3InitialHydrateControllerForTests } from './desktop-v3-initial-hydrate-controller'
import { postDesktopV3SyncBootstrap, postDesktopV3SyncHydrate, type DesktopV3HydrateInput } from './desktop-v3-sync-api'
import type { DesktopV3CacheAction, SyncSnapshotResponse } from './desktop-v3-cache-types'
import type { PersistedDesktopV3MessageTailV1, PersistedDesktopV3OwnerV1 } from './desktop-v3-cache-persisted-types'

interface BootstrapDesktopV3SidebarDeps {
  postBootstrap?: () => Promise<SyncSnapshotResponse>
  postHydrate?: (input: DesktopV3HydrateInput) => Promise<SyncSnapshotResponse>
  dispatch?: (action: DesktopV3CacheAction) => void
  loadActiveOwnerKey?: () => string | undefined
  readOwner?: (ownerKey: string) => Promise<PersistedDesktopV3OwnerV1 | undefined>
  readMessageTail?: (ownerKey: string, sessionId: string) => Promise<PersistedDesktopV3MessageTailV1 | undefined>
}

let bootstrapInFlight: Promise<void> | null = null

export function bootstrapDesktopV3Sidebar(deps: BootstrapDesktopV3SidebarDeps = {}): Promise<void> {
  if (bootstrapInFlight) {
    return bootstrapInFlight
  }

  const dispatch = deps.dispatch ?? dispatchDesktopV3Cache
  const postBootstrap = deps.postBootstrap ?? postDesktopV3SyncBootstrap
  const postHydrate = deps.postHydrate ?? postDesktopV3SyncHydrate
  const loadActiveOwnerKey = deps.loadActiveOwnerKey ?? loadDesktopV3CacheActiveOwnerKey
  const readOwner = deps.readOwner ?? readDesktopV3Owner
  const readMessageTail = deps.readMessageTail ?? readDesktopV3MessageTail

  bootstrapInFlight = restoreDesktopV3CacheFromActiveOwner({
    dispatch,
    loadActiveOwnerKey,
    readOwner,
    readMessageTail,
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
  dispatch: (action: DesktopV3CacheAction) => void
  loadActiveOwnerKey?: () => string | undefined
  readOwner?: (ownerKey: string) => Promise<PersistedDesktopV3OwnerV1 | undefined>
  readMessageTail?: (ownerKey: string, sessionId: string) => Promise<PersistedDesktopV3MessageTailV1 | undefined>
}): Promise<boolean> {
  try {
    const ownerKey = input.loadActiveOwnerKey?.() ?? loadDesktopV3CacheActiveOwnerKey()
    if (!ownerKey) return false

    const owner = await (input.readOwner ?? readDesktopV3Owner)(ownerKey)
    if (!owner) return false

    const selectedSessionId = owner.selectedSessionId
    const selectedMessageTail = selectedSessionId
      ? await (input.readMessageTail ?? readDesktopV3MessageTail)(owner.ownerKey, selectedSessionId)
      : undefined

    input.dispatch({
      type: 'desktopV3Cache.restore',
      owner,
      selectedMessageTail,
    })
    return true
  } catch {
    return false
  }
}

export function resetDesktopV3BootstrapControllerForTests(): void {
  bootstrapInFlight = null
  resetDesktopV3InitialHydrateControllerForTests()
}
