import { useEffect, useRef, type ReactNode } from 'react'

import { retainDesktopV3RealtimeController, type DesktopV3RealtimeLease } from '../realtime/v3-realtime-controller'
import { bootstrapDesktopV3SidebarMetadataOnly, type DesktopV3BootstrapMetadataResult } from '../state/desktop-v3-bootstrap-controller'
import { dispatchDesktopV3Cache } from '../state/desktop-v3-cache-store'
import { installDesktopV3StreamCacheTestHooksForTestbench } from '../state/desktop-v3-stream-cache-test-hooks'

const DESKTOP_V3_RUNTIME_PROVIDER_OWNER_KEY = 'desktop-v3-runtime-provider'

interface DesktopV3RuntimeProviderProps {
  children: ReactNode
  initialPreferredSessionId?: string | null
}

interface RetainedDesktopV3Runtime {
  bootstrapReady: Promise<DesktopV3BootstrapMetadataResult>
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
  await runtime.bootstrapReady
  if (isCancelled()) return

  // Snapshot rendering must not wait for the websocket. Realtime readiness only
  // closes the snapshot-to-live gap after the bounded hydrated bootstrap renders.
  void runtime.realtimeLease.ready.catch((error: unknown) => {
    console.error('[desktop-v3] realtime startup failed after bootstrap render', error)
  })
}

function normalizePreferredSessionId(sessionId: string | null | undefined): string | null | undefined {
  if (sessionId === null) return null
  const normalized = sessionId?.trim()
  return normalized || undefined
}
