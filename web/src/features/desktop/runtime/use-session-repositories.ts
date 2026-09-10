import { useEffect, useMemo, useSyncExternalStore } from 'react'
import { fetchSessionRepositories } from '../git/api'
import { subscribeDesktopV3Cache } from '../state/desktop-v3-cache-store'
import { repositoryOwnerIds, repositoryEventInvalidates, scheduleRepositoryRefresh, SessionRepositoryInventory } from '../state/session-repositories'

export function useSessionRepositories(sessionId: string) {
  const inventory = useMemo(() => new SessionRepositoryInventory((cursor, signal) => fetchSessionRepositories(sessionId, cursor, signal)), [sessionId])
  const state = useSyncExternalStore(inventory.subscribe, inventory.snapshot, inventory.snapshot)
  useEffect(() => {
    if (!sessionId) return
    const scheduler = scheduleRepositoryRefresh(inventory, () => document.visibilityState !== 'hidden')
    const unsubscribe = subscribeDesktopV3Cache(mutation => {
      const owners = mutation ? repositoryOwnerIds(sessionId, inventory.state.items, mutation.nextState) : new Set([sessionId])
      if (!mutation || repositoryEventInvalidates(mutation.action, owners)) scheduler.invalidate()
    })
    document.addEventListener('visibilitychange', scheduler.visibilityChanged)
    if (document.visibilityState !== 'hidden') void inventory.refresh()
    return () => {
      unsubscribe(); scheduler.dispose()
      document.removeEventListener('visibilitychange', scheduler.visibilityChanged)
    }
  }, [inventory, sessionId])
  return { ...state, refresh: inventory.refresh, loadMore: inventory.loadMore, select: inventory.select }
}
