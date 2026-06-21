import { loadDesktopV3CacheActiveOwnerKey } from './desktop-v3-cache-active-owner'
import { readDesktopV3Owner } from './desktop-v3-cache-db'
import { desktopV3CachePersistenceCoordinator } from './desktop-v3-cache-persistence-coordinator'
import { getDesktopV3CacheSnapshot } from './desktop-v3-cache-store'
import type { PersistedDesktopV3OwnerV1 } from './desktop-v3-cache-persisted-types'
import type { DesktopV3CacheState } from './desktop-v3-cache-types'

export interface DesktopV3StreamCacheTestHooks {
  awaitDurableIdle(): Promise<void>
  readPersistedOwner(): Promise<PersistedDesktopV3OwnerV1 | undefined>
  getMemoryLiveRuns(): DesktopV3CacheState['liveRunsBySession']
  pauseRealtime(): void
  resumeRealtime(): void
}

declare global {
  interface Window {
    __SWARM_DESKTOP_V3_TESTBENCH__?: true
    __desktopV3StreamCacheTest?: DesktopV3StreamCacheTestHooks
    __desktopV3StreamCacheRealtimeControl__?: {
      pauseRealtime?: () => void
      resumeRealtime?: () => void
    }
  }
}

export function installDesktopV3StreamCacheTestHooksForTestbench(): () => void {
  if (typeof window === 'undefined') return () => undefined
  if (window.__SWARM_DESKTOP_V3_TESTBENCH__ !== true) return () => undefined

  const hooks: DesktopV3StreamCacheTestHooks = {
    async awaitDurableIdle() {
      await desktopV3CachePersistenceCoordinator.idle()
    },
    async readPersistedOwner() {
      const ownerKey = loadDesktopV3CacheActiveOwnerKey()
      return ownerKey ? readDesktopV3Owner(ownerKey) : undefined
    },
    getMemoryLiveRuns() {
      return structuredClone(getDesktopV3CacheSnapshot().liveRunsBySession)
    },
    pauseRealtime() {
      window.__desktopV3StreamCacheRealtimeControl__?.pauseRealtime?.()
    },
    resumeRealtime() {
      window.__desktopV3StreamCacheRealtimeControl__?.resumeRealtime?.()
    },
  }

  window.__desktopV3StreamCacheTest = hooks

  return () => {
    if (window.__desktopV3StreamCacheTest === hooks) {
      delete window.__desktopV3StreamCacheTest
    }
  }
}
