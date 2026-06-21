import { useSyncExternalStore } from 'react'
import { createStore } from 'zustand/vanilla'

import { createEmptyDesktopV3CacheState, desktopV3CacheReducer } from './desktop-v3-cache-reducer'
import type { DesktopV3CacheAction, DesktopV3CacheState } from './desktop-v3-cache-types'

export interface DesktopV3CacheMutation {
  action: DesktopV3CacheAction
  previousState: DesktopV3CacheState
  nextState: DesktopV3CacheState
}

type DesktopV3CacheListener = (mutation?: DesktopV3CacheMutation) => void

const store = createStore<DesktopV3CacheState>(() => createEmptyDesktopV3CacheState())
const mutationListeners = new Set<DesktopV3CacheListener>()

export function getDesktopV3CacheSnapshot(): DesktopV3CacheState {
  return store.getState()
}

export function subscribeDesktopV3Cache(listener: DesktopV3CacheListener): () => void {
  mutationListeners.add(listener)
  return () => {
    mutationListeners.delete(listener)
  }
}

export function dispatchDesktopV3Cache(action: DesktopV3CacheAction): void {
  const previousState = store.getState()
  const nextState = desktopV3CacheReducer({ ...previousState }, action)
  replaceDesktopV3CacheSnapshotAfterDurableCommit(previousState, nextState, [action])
}

export function replaceDesktopV3CacheSnapshotAfterDurableCommit(
  previousState: DesktopV3CacheState,
  nextState: DesktopV3CacheState,
  actions: DesktopV3CacheAction[],
): void {
  store.setState(nextState, true)
  for (const action of actions) {
    const mutation: DesktopV3CacheMutation = { action, previousState, nextState }
    for (const listener of mutationListeners) {
      listener(mutation)
    }
  }
}

export function useDesktopV3CacheSelector<T>(selector: (state: DesktopV3CacheState) => T): T {
  const snapshot = useSyncExternalStore(store.subscribe, store.getState, store.getState)
  return selector(snapshot)
}

export function resetDesktopV3CacheForTests(state: DesktopV3CacheState = createEmptyDesktopV3CacheState()): void {
  store.setState(state, true)
  for (const listener of mutationListeners) {
    listener()
  }
}
