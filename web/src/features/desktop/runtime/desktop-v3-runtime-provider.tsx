import { useEffect, useRef, type ReactNode } from 'react'

import { requireDesktopV3RealtimeControllerReady, retainDesktopV3RealtimeController, type DesktopV3RealtimeLease } from '../realtime/v3-realtime-controller'
import { bootstrapDesktopV3SidebarMetadataOnly, type DesktopV3BootstrapMetadataResult } from '../state/desktop-v3-bootstrap-controller'
import { dispatchDesktopV3Cache, getDesktopV3CacheSnapshot } from '../state/desktop-v3-cache-store'
import { readDesktopV3MessageTails } from '../state/desktop-v3-cache-db'
import { buildPostRealtimeConnectHydrationSnapshot, hydrateDesktopV3InitialSessions } from '../state/desktop-v3-initial-hydrate-controller'
import { startDesktopV3PersistenceController } from '../state/desktop-v3-persistence-controller'
import { installDesktopV3StreamCacheTestHooksForTestbench } from '../state/desktop-v3-stream-cache-test-hooks'

const DESKTOP_V3_RUNTIME_PROVIDER_OWNER_KEY = 'desktop-v3-runtime-provider'

interface DesktopV3RuntimeProviderProps {
  children: ReactNode
  initialPreferredSessionId?: string | null
}

interface RetainedDesktopV3Runtime {
  bootstrapReady: Promise<DesktopV3BootstrapMetadataResult>
  initialPreferredSessionId?: string | null
  realtimeLease: DesktopV3RealtimeLease
  release: () => void
}

export function DesktopV3RuntimeProvider({ children, initialPreferredSessionId }: DesktopV3RuntimeProviderProps) {
  const runtimeRef = useRef<RetainedDesktopV3Runtime | null>(null)
  const ensureRuntime = () => {
    if (!runtimeRef.current) {
      runtimeRef.current = retainDesktopV3Runtime(initialPreferredSessionId)
    }
    return runtimeRef.current
  }
  ensureRuntime()

  useEffect(() => {
    const runtime = ensureRuntime()

    let cancelled = false
    const stopPersistence = startDesktopV3PersistenceController()
    const uninstallTestHooks = installDesktopV3StreamCacheTestHooksForTestbench()

    void startDesktopV3RuntimeHydration(runtime, () => cancelled).catch((error: unknown) => {
      if (cancelled) return
      dispatchDesktopV3Cache({
        type: 'desktopSidebarBootstrap.update',
        patch: {
          status: 'error',
          error: error instanceof Error ? error.message : String(error),
        },
      })
    })

    return () => {
      cancelled = true
      runtime.release()
      stopPersistence()
      uninstallTestHooks()
      if (runtimeRef.current === runtime) {
        runtimeRef.current = null
      }
    }
  }, [])

  return <>{children}</>
}

function retainDesktopV3Runtime(initialPreferredSessionId?: string | null): RetainedDesktopV3Runtime {
  const normalizedPreferredSessionId = normalizePreferredSessionId(initialPreferredSessionId)
  const bootstrapReady = bootstrapDesktopV3SidebarMetadataOnly({
    preferredSessionId: normalizedPreferredSessionId,
  })
  const realtimeLease = retainDesktopV3RealtimeController({
    ownerKey: DESKTOP_V3_RUNTIME_PROVIDER_OWNER_KEY,
    preferredSessionId: normalizedPreferredSessionId,
    bootstrap: bootstrapReady,
  })

  let released = false
  return {
    bootstrapReady,
    initialPreferredSessionId: normalizedPreferredSessionId,
    realtimeLease,
    release: () => {
      if (released) return
      released = true
      realtimeLease.release()
    },
  }
}

async function startDesktopV3RuntimeHydration(
  runtime: RetainedDesktopV3Runtime,
  isCancelled: () => boolean,
): Promise<void> {
  const bootstrap = await runtime.bootstrapReady
  if (isCancelled()) return

  await runtime.realtimeLease.ready
  if (isCancelled()) return

  const afterRealtimeConnect = getDesktopV3CacheSnapshot()
  const scopeId = afterRealtimeConnect.desktopSidebarBootstrap.scopeId?.trim()
  if (!scopeId) {
    throw new Error('Desktop V3 initial hydrate requires bootstrapped sidebar scope')
  }

  const preferred = runtime.initialPreferredSessionId
  const selectedSessionId = preferred === null
    ? undefined
    : preferred?.trim() || afterRealtimeConnect.selectedSessionId?.trim()
  const sidebarSessionIds = afterRealtimeConnect.sessionOrderByScope[scopeId] ?? []
  const controller = await requireDesktopV3RealtimeControllerReady()
  if (selectedSessionId) {
    await controller.ensureSessionHistory(selectedSessionId)
  }
  if (isCancelled()) return

  await hydrateDesktopV3InitialSessions({
    scopeId,
    sessionIds: sidebarSessionIds.filter((sessionId) => sessionId !== selectedSessionId),
    bootstrapResponse: buildPostRealtimeConnectHydrationSnapshot(
      bootstrap.response,
      getDesktopV3CacheSnapshot(),
      scopeId,
    ),
    preBootstrapCachedProjections: bootstrap.preBootstrapCachedProjections,
    selectedMessageTail: bootstrap.restoredSelectedMessageTail,
    preferredSessionId: null,
    currentSelectedSessionId: undefined,
    ownerKey: bootstrap.restoredOwnerKey,
    readMessageTails: readDesktopV3MessageTails,
  })
}

function normalizePreferredSessionId(sessionId: string | null | undefined): string | null | undefined {
  if (sessionId === null) return null
  const normalized = sessionId?.trim()
  return normalized || undefined
}
