import { useSyncExternalStore } from 'react'

import {
  createEmptyDesktopState,
  desktopReducer,
  type DesktopDaemonEvent,
  type DesktopDaemonSnapshot,
  type DesktopState,
  type DesktopStateStatus,
} from './desktop-state'

type DesktopStoreListener = () => void

const listeners = new Set<DesktopStoreListener>()
let desktopState = createEmptyDesktopState()

export function useDesktopState(): DesktopState
export function useDesktopState<Selected>(selector: (state: DesktopState) => Selected): Selected
export function useDesktopState<Selected>(selector?: (state: DesktopState) => Selected): DesktopState | Selected {
  const state = useSyncExternalStore(subscribeDesktop, getDesktopSnapshot, getDesktopSnapshot)
  return selector ? selector(state) : state
}

export function getDesktopSnapshot(): DesktopState {
  return desktopState
}

export function subscribeDesktop(listener: DesktopStoreListener): () => void {
  listeners.add(listener)
  return () => {
    listeners.delete(listener)
  }
}

export function replaceDesktopFromSnapshot(snapshot: DesktopDaemonSnapshot): DesktopState {
  return dispatchDesktopState({ type: 'snapshot/replace', snapshot })
}

export function applyDesktopDaemonEvent(event: DesktopDaemonEvent): DesktopState {
  return dispatchDesktopState({ type: 'daemon/event', event })
}

export function markDesktopStale(reason: string): DesktopState {
  return dispatchDesktopState({ type: 'connection/stale', reason })
}

export function setDesktopConnectionStatus(status: DesktopStateStatus, error?: string | null): DesktopState {
  return dispatchDesktopState({ type: 'connection/status', status, error })
}

function dispatchDesktopState(action: Parameters<typeof desktopReducer>[1]): DesktopState {
  const nextState = desktopReducer(desktopState, action)
  if (Object.is(nextState, desktopState)) {
    return desktopState
  }

  desktopState = nextState
  emitDesktopStateChange()
  return desktopState
}

function emitDesktopStateChange(): void {
  for (const listener of listeners) {
    listener()
  }
}
