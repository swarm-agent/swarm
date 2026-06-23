import { useStoreWithEqualityFn } from 'zustand/traditional'
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
  dispatchDesktopV3CacheBatch([action])
}

export function dispatchDesktopV3CacheBatch(actions: DesktopV3CacheAction[]): void {
  if (actions.length === 0) return
  const previousState = store.getState()
  let nextState: DesktopV3CacheState = { ...previousState }
  for (const action of actions) {
    nextState = desktopV3CacheReducer(nextState, action)
  }
  commitDesktopV3CacheSnapshot(previousState, nextState, actions)
}

export function commitDesktopV3CacheSnapshot(
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

export function useDesktopV3CacheSelector<T>(
  selector: (state: DesktopV3CacheState) => T,
  equalityFn?: (left: T, right: T) => boolean,
): T {
  return useStoreWithEqualityFn(store, selector, equalityFn)
}

export function resetDesktopV3CacheForTests(state: DesktopV3CacheState = createEmptyDesktopV3CacheState()): void {
  store.setState(state, true)
  for (const listener of mutationListeners) {
    listener()
  }
}
