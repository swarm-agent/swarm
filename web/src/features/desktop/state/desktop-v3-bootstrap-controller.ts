import { bootstrapResponseToAction } from './desktop-v3-cache-wire'
import { dispatchDesktopV3Cache, dispatchDesktopV3CacheBatch } from './desktop-v3-cache-store'
import { hydrateResponseCompletesSession } from './desktop-v3-cache-reducer'
import { resetDesktopV3InitialHydrateControllerForTests } from './desktop-v3-initial-hydrate-controller'
import { buildDesktopV3BootstrapInput, postDesktopV3SyncBootstrap, type DesktopV3HydrateInput } from './desktop-v3-sync-api'
import type { DesktopV3BootstrapInput } from './desktop-v3-sync-api'
import type { DesktopV3CacheAction, SyncSnapshotResponse } from './desktop-v3-cache-types'

export interface BootstrapDesktopV3SidebarDeps {
  preferredSessionId?: string | null
  postBootstrap?: (input?: Partial<DesktopV3BootstrapInput>) => Promise<SyncSnapshotResponse>
  postHydrate?: (input: DesktopV3HydrateInput) => Promise<SyncSnapshotResponse>
  dispatch?: (action: DesktopV3CacheAction) => void
  dispatchBatch?: (actions: DesktopV3CacheAction[]) => void
}

export interface DesktopV3BootstrapMetadataResult {
  response: SyncSnapshotResponse
}

let bootstrapInFlight: Promise<DesktopV3BootstrapMetadataResult> | null = null

export function bootstrapDesktopV3Sidebar(deps: BootstrapDesktopV3SidebarDeps = {}): Promise<void> {
  const dispatch = deps.dispatch ?? dispatchDesktopV3Cache

  return bootstrapDesktopV3SidebarMetadataOnly(deps)
    .then(() => undefined)
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
  const dispatchBatch = desktopV3BootstrapDispatchBatch(deps)
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

  const bootstrapInput = buildDesktopV3BootstrapInput({}, deps.preferredSessionId)
  bootstrapInFlight = postBootstrap(bootstrapInput)
    .then((response) => {
      dispatchBatch([
        bootstrapResponseToAction(response),
        desktopSidebarBootstrapReadyAction(response),
        desktopInitialHydrateReadyAction(response),
      ])
      return {
        response,
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

function desktopV3BootstrapDispatchBatch(deps: BootstrapDesktopV3SidebarDeps): (actions: DesktopV3CacheAction[]) => void {
  if (deps.dispatchBatch) return deps.dispatchBatch
  if (deps.dispatch) {
    return (actions) => {
      for (const action of actions) deps.dispatch?.(action)
    }
  }
  return dispatchDesktopV3CacheBatch
}

export function desktopSidebarBootstrapReadyAction(response: SyncSnapshotResponse): DesktopV3CacheAction {
  return {
    type: 'desktopSidebarBootstrap.update',
    patch: {
      status: 'ready',
      scopeId: response.scope_id,
      error: undefined,
      stale: false,
      source: 'network',
    },
  }
}

export function desktopInitialHydrateReadyAction(
  response: SyncSnapshotResponse,
  sessionIds = response.session_order ?? [],
): DesktopV3CacheAction {
  return {
    type: 'desktopInitialHydrate.update',
    patch: {
      status: 'ready',
      requestedSessionIds: sessionIds,
      hydratedSessionIds: sessionIds.filter((sessionId) => hydrateResponseCompletesSession(response, sessionId)),
      scopeId: response.scope_id,
      error: undefined,
      stale: false,
      source: 'network',
    },
  }
}
