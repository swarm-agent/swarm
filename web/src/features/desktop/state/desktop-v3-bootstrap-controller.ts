import { bootstrapResponseToAction } from './desktop-v3-cache-wire'
import { dispatchDesktopV3Cache, getDesktopV3CacheSnapshot } from './desktop-v3-cache-store'
import { hydrateResponseCompletesSession } from './desktop-v3-cache-reducer'
import { resetDesktopV3InitialHydrateControllerForTests } from './desktop-v3-initial-hydrate-controller'
import { postDesktopV3SyncBootstrap, type DesktopV3HydrateInput } from './desktop-v3-sync-api'
import type { DesktopV3CacheAction, DesktopV3CacheState, SyncSnapshotResponse, V3SessionProjection } from './desktop-v3-cache-types'

export interface BootstrapDesktopV3SidebarDeps {
  preferredSessionId?: string | null
  postBootstrap?: () => Promise<SyncSnapshotResponse>
  postHydrate?: (input: DesktopV3HydrateInput) => Promise<SyncSnapshotResponse>
  dispatch?: (action: DesktopV3CacheAction) => void
  getSnapshot?: () => DesktopV3CacheState
}

export interface DesktopV3BootstrapMetadataResult {
  response: SyncSnapshotResponse
  preBootstrapCachedProjections: Record<string, V3SessionProjection>
}

let bootstrapInFlight: Promise<DesktopV3BootstrapMetadataResult> | null = null

export function bootstrapDesktopV3Sidebar(deps: BootstrapDesktopV3SidebarDeps = {}): Promise<void> {
  const dispatch = deps.dispatch ?? dispatchDesktopV3Cache

  return bootstrapDesktopV3SidebarMetadataOnly(deps)
    .then((bootstrap) => {
      const sessionIds = bootstrap.response.session_order ?? []
      dispatch({
        type: 'desktopInitialHydrate.update',
        patch: {
          status: 'ready',
          requestedSessionIds: sessionIds,
          hydratedSessionIds: sessionIds.filter((sessionId) => hydrateResponseCompletesSession(bootstrap.response, sessionId)),
          scopeId: bootstrap.response.scope_id,
          error: undefined,
          stale: false,
          source: 'network',
        },
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
}

export function bootstrapDesktopV3SidebarMetadataOnly(
  deps: BootstrapDesktopV3SidebarDeps = {},
): Promise<DesktopV3BootstrapMetadataResult> {
  if (bootstrapInFlight) {
    return bootstrapInFlight
  }

  const dispatch = deps.dispatch ?? dispatchDesktopV3Cache
  const getSnapshot = deps.getSnapshot ?? getDesktopV3CacheSnapshot
  const postBootstrap = deps.postBootstrap ?? postDesktopV3SyncBootstrap

  dispatch({
    type: 'desktopSidebarBootstrap.update',
    patch: {
      status: 'loading',
      error: undefined,
      stale: undefined,
      source: undefined,
    },
  })

  bootstrapInFlight = postBootstrap()
    .then((response) => {
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
      return {
        response,
        preBootstrapCachedProjections,
      }
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

function cloneProjections(projectionsBySession: Record<string, V3SessionProjection>): Record<string, V3SessionProjection> {
  const output: Record<string, V3SessionProjection> = {}
  for (const [sessionId, projection] of Object.entries(projectionsBySession)) {
    output[sessionId] = { ...projection }
  }
  return output
}
