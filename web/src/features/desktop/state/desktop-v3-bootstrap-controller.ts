import { bootstrapResponseToAction } from './desktop-v3-cache-wire'
import { dispatchDesktopV3Cache } from './desktop-v3-cache-store'
import { hydrateDesktopV3InitialSessions, resetDesktopV3InitialHydrateControllerForTests } from './desktop-v3-initial-hydrate-controller'
import { postDesktopV3SyncBootstrap, postDesktopV3SyncHydrate, type DesktopV3HydrateInput } from './desktop-v3-sync-api'
import type { DesktopV3CacheAction, SyncSnapshotResponse } from './desktop-v3-cache-types'

interface BootstrapDesktopV3SidebarDeps {
  postBootstrap?: () => Promise<SyncSnapshotResponse>
  postHydrate?: (input: DesktopV3HydrateInput) => Promise<SyncSnapshotResponse>
  dispatch?: (action: DesktopV3CacheAction) => void
}

let bootstrapInFlight: Promise<void> | null = null

export function bootstrapDesktopV3Sidebar(deps: BootstrapDesktopV3SidebarDeps = {}): Promise<void> {
  if (bootstrapInFlight) {
    return bootstrapInFlight
  }

  const dispatch = deps.dispatch ?? dispatchDesktopV3Cache
  const postBootstrap = deps.postBootstrap ?? postDesktopV3SyncBootstrap
  const postHydrate = deps.postHydrate ?? postDesktopV3SyncHydrate

  dispatch({
    type: 'desktopSidebarBootstrap.update',
    patch: {
      status: 'loading',
      error: undefined,
    },
  })

  bootstrapInFlight = postBootstrap()
    .then(async (response) => {
      dispatch(bootstrapResponseToAction(response))
      dispatch({
        type: 'desktopSidebarBootstrap.update',
        patch: {
          status: 'ready',
          scopeId: response.scope_id,
          error: undefined,
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

export function resetDesktopV3BootstrapControllerForTests(): void {
  bootstrapInFlight = null
  resetDesktopV3InitialHydrateControllerForTests()
}
