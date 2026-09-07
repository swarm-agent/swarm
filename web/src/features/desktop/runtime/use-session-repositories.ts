import { useEffect, useMemo, useSyncExternalStore } from 'react'
import { fetchSessionRepositories } from '../git/api'
import { subscribeDesktopV3Cache } from '../state/desktop-v3-cache-store'
import { repositoryEventInvalidates, SessionRepositoryInventory } from '../state/session-repositories'

export function useSessionRepositories(sessionId: string) {
  const inventory = useMemo(() => new SessionRepositoryInventory((cursor, signal) => fetchSessionRepositories(sessionId, cursor, signal)), [sessionId])
  const state = useSyncExternalStore(inventory.subscribe, inventory.snapshot, inventory.snapshot)
  useEffect(() => {
    if (!sessionId) return
    let timer: ReturnType<typeof setTimeout> | undefined
    const refresh = () => {
      if (document.visibilityState === 'hidden') return
      if (inventory.state.loading) { schedule(); return }
      void inventory.refresh()
    }
    const schedule = () => {
      if (timer) return
      timer = setTimeout(() => { timer = undefined; refresh() }, 1_000)
    }
    const invalidate = () => { inventory.invalidate(); schedule() }
    const unsubscribe = subscribeDesktopV3Cache(mutation => {
      if (!mutation || repositoryEventInvalidates(mutation.action.type)) invalidate()
    })
    // Disk edits have no session event. One bounded page refresh, not N Git watchers.
    const polling = setInterval(invalidate, 30_000)
    window.addEventListener('focus', invalidate)
    window.addEventListener('online', invalidate)
    document.addEventListener('visibilitychange', invalidate)
    refresh()
    return () => {
      unsubscribe(); inventory.dispose(); clearInterval(polling)
      if (timer) clearTimeout(timer)
      window.removeEventListener('focus', invalidate)
      window.removeEventListener('online', invalidate)
      document.removeEventListener('visibilitychange', invalidate)
    }
  }, [inventory, sessionId])
  return { ...state, refresh: inventory.refresh, loadMore: inventory.loadMore, select: inventory.select }
}
