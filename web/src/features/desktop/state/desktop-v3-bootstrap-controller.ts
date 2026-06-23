import { bootstrapResponseToAction } from './desktop-v3-cache-wire'
import { dispatchDesktopV3Cache, dispatchDesktopV3CacheBatch, getDesktopV3CacheSnapshot } from './desktop-v3-cache-store'
import { hydrateResponseCompletesSession } from './desktop-v3-cache-reducer'
import { resetDesktopV3InitialHydrateControllerForTests } from './desktop-v3-initial-hydrate-controller'
import { buildDesktopV3BootstrapInput, countArrayMapItems, logDesktopV3BootstrapTiming, postDesktopV3SyncBootstrap, type DesktopV3HydrateInput } from './desktop-v3-sync-api'
import { selectDesktopV3HydratedTranscriptDiagnostics } from './desktop-v3-cache-selectors'
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
  const getSnapshot = deps.dispatch || deps.dispatchBatch ? undefined : getDesktopV3CacheSnapshot
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
  const startedAt = desktopV3BootstrapNow()
  bootstrapInFlight = postBootstrap(bootstrapInput)
    .then((response) => {
      const beforeApplyAt = desktopV3BootstrapNow()
      dispatchBatch([
        bootstrapResponseToAction(response),
        desktopSidebarBootstrapReadyAction(response),
        desktopInitialHydrateReadyAction(response),
      ])
      const afterApplyAt = desktopV3BootstrapNow()
      const transcriptDiagnostics = getSnapshot
        ? selectDesktopV3HydratedTranscriptDiagnostics(getSnapshot())
        : {
          hydratedSessionCount: Object.keys(response.messages_by_session ?? {}).length,
          hydratedMessageCount: countArrayMapItems(response.messages_by_session),
          retainedBackgroundHydratedSessionCount: 0,
          inFlightHydrateSessionCount: 0,
          evictedTranscriptCount: 0,
        }
      logDesktopV3BootstrapTiming('zustand_apply', {
        post_to_apply_start_ms: roundBootstrapTiming(beforeApplyAt - startedAt),
        apply_ms: roundBootstrapTiming(afterApplyAt - beforeApplyAt),
        total_to_applied_ms: roundBootstrapTiming(afterApplyAt - startedAt),
        sessions: Object.keys(response.sessions_by_id ?? {}).length,
        session_order: response.session_order?.length ?? 0,
        messages: countArrayMapItems(response.messages_by_session),
        run_intents: countArrayMapItems(response.run_intents_by_session),
        ...transcriptDiagnostics,
      })
      scheduleDesktopV3BootstrapPaintTiming(afterApplyAt)
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

function scheduleDesktopV3BootstrapPaintTiming(appliedAt: number): void {
  const requestFrame = globalThis.requestAnimationFrame
  if (typeof requestFrame !== 'function') return
  requestFrame(() => {
    logDesktopV3BootstrapTiming('next_frame_after_apply', {
      frame_delay_ms: roundBootstrapTiming(desktopV3BootstrapNow() - appliedAt),
    })
  })
}

function desktopV3BootstrapNow(): number {
  return globalThis.performance?.now?.() ?? Date.now()
}

function roundBootstrapTiming(value: number): number {
  return Math.round(value * 1000) / 1000
}
