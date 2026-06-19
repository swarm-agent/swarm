import { useSyncExternalStore } from 'react'

import { createEmptyDesktopV3CacheState, desktopV3CacheReducer } from './desktop-v3-cache-reducer'
import type { DesktopV3CacheAction, DesktopV3CacheState } from './desktop-v3-cache-types'

type DesktopV3CacheListener = () => void

let desktopV3CacheState = createEmptyDesktopV3CacheState()
const desktopV3CacheListeners = new Set<DesktopV3CacheListener>()

export function getDesktopV3CacheSnapshot(): DesktopV3CacheState {
  return desktopV3CacheState
}

export function subscribeDesktopV3Cache(listener: DesktopV3CacheListener): () => void {
  desktopV3CacheListeners.add(listener)
  return () => {
    desktopV3CacheListeners.delete(listener)
  }
}

export function dispatchDesktopV3Cache(action: DesktopV3CacheAction): void {
  desktopV3CacheState = desktopV3CacheReducer({ ...desktopV3CacheState }, action)
  for (const listener of desktopV3CacheListeners) {
    listener()
  }
}

export function useDesktopV3CacheSelector<T>(selector: (state: DesktopV3CacheState) => T): T {
  const snapshot = useSyncExternalStore(
    subscribeDesktopV3Cache,
    getDesktopV3CacheSnapshot,
    getDesktopV3CacheSnapshot,
  )
  return selector(snapshot)
}

export function resetDesktopV3CacheForTests(state: DesktopV3CacheState = createEmptyDesktopV3CacheState()): void {
  desktopV3CacheState = state
  for (const listener of desktopV3CacheListeners) {
    listener()
  }
}
