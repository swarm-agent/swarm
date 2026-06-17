import { useEffect } from 'react'
import { useMatchRoute } from '@tanstack/react-router'
import { useDesktopUiStore } from '../state/desktop-ui-store'

export function DesktopRealtimeBootstrap() {
  const hydrate = useDesktopUiStore((state) => state.hydrate)
  const disconnect = useDesktopUiStore((state) => state.disconnect)
  const refreshRealtimeIfStale = useDesktopUiStore((state) => state.refreshRealtimeIfStale)
  const vault = useDesktopUiStore((state) => state.vault)
  const refreshNotifications = useDesktopUiStore((state) => state.refreshNotifications)
  const matchRoute = useMatchRoute()
  const inDesktopApp = Boolean(matchRoute({ to: '/', fuzzy: false }))
    || Boolean(matchRoute({ to: '/$workspaceSlug', fuzzy: false }))
    || Boolean(matchRoute({ to: '/$workspaceSlug/$sessionId', fuzzy: false }))

  useEffect(() => {
    if (!inDesktopApp || (vault.enabled && !vault.unlocked)) {
      disconnect()
      return
    }
    void hydrate()
    void refreshNotifications()
    return () => {
      disconnect()
    }
  }, [disconnect, hydrate, inDesktopApp, refreshNotifications, vault.enabled, vault.unlocked])

  useEffect(() => {
    if (!inDesktopApp || (vault.enabled && !vault.unlocked)) {
      return
    }
    const refreshRealtime = (reason: string) => {
      void refreshRealtimeIfStale(reason)
    }
    const handleOnline = () => {
      refreshRealtime('browser online')
    }
    const handleVisible = () => {
      if (document.visibilityState !== 'visible') {
        return
      }
      refreshRealtime('visibility restored')
    }
    const handleFocus = () => {
      refreshRealtime('window focus')
    }
    const handlePageShow = (event: PageTransitionEvent) => {
      refreshRealtime(event.persisted ? 'pageshow persisted' : 'pageshow')
    }
    window.addEventListener('online', handleOnline)
    window.addEventListener('focus', handleFocus)
    window.addEventListener('pageshow', handlePageShow)
    document.addEventListener('visibilitychange', handleVisible)
    return () => {
      window.removeEventListener('online', handleOnline)
      window.removeEventListener('focus', handleFocus)
      window.removeEventListener('pageshow', handlePageShow)
      document.removeEventListener('visibilitychange', handleVisible)
    }
  }, [inDesktopApp, refreshRealtimeIfStale, vault.enabled, vault.unlocked])

  return null
}
