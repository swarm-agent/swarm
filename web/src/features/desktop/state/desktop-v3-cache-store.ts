import { useStore } from 'zustand'
import { useShallow } from 'zustand/shallow'
import { createStore } from 'zustand/vanilla'

import { createEmptyDesktopV3CacheState, desktopV3CacheReducer } from './desktop-v3-cache-reducer'
import type { DesktopV3CacheAction, DesktopV3CacheState } from './desktop-v3-cache-types'

type DesktopV3CacheListener = () => void

export const desktopV3Store = createStore<DesktopV3CacheState>(() =>
  createEmptyDesktopV3CacheState(),
)

export function getDesktopV3CacheSnapshot(): DesktopV3CacheState {
  return desktopV3Store.getState()
}

export function subscribeDesktopV3Cache(listener: DesktopV3CacheListener): () => void {
  return desktopV3Store.subscribe(() => {
    listener()
  })
}

export function dispatchDesktopV3Cache(action: DesktopV3CacheAction): void {
  desktopV3Store.setState(
    (current) => desktopV3CacheReducer({ ...current }, action),
    true,
  )
}

export function useDesktopV3CacheSelector<T>(selector: (state: DesktopV3CacheState) => T): T {
  return useStore(desktopV3Store, useShallow(selector))
}

export function resetDesktopV3CacheForTests(state: DesktopV3CacheState = createEmptyDesktopV3CacheState()): void {
  desktopV3Store.setState(state, true)
}
