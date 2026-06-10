import { useEffect } from 'react'
import { useMatchRoute } from '@tanstack/react-router'
import { debugLog } from '../../../lib/debug-log'
import { useDesktopUiStore } from '../state/desktop-ui-store'

export function DesktopRealtimeBootstrap() {
  const hydrate = useDesktopUiStore((state) => state.hydrate)
  const disconnect = useDesktopUiStore((state) => state.disconnect)
  const reconnectIfStale = useDesktopUiStore((state) => state.reconnectIfStale)
  const vault = useDesktopUiStore((state) => state.vault)
  const refreshNotifications = useDesktopUiStore((state) => state.refreshNotifications)
  const matchRoute = useMatchRoute()
  const inDesktopApp = Boolean(matchRoute({ to: '/', fuzzy: false }))
    || Boolean(matchRoute({ to: '/$workspaceSlug', fuzzy: false }))
    || Boolean(matchRoute({ to: '/$workspaceSlug/$sessionId', fuzzy: false }))

  useEffect(() => {
    debugLog('desktop-realtime-bootstrap', 'effect:hydrate-check', {
      inDesktopApp,
      vaultEnabled: vault.enabled,
      vaultUnlocked: vault.unlocked,
    })
    if (!inDesktopApp || (vault.enabled && !vault.unlocked)) {
      debugLog('desktop-realtime-bootstrap', 'effect:disconnect-before-hydrate', {
        reason: !inDesktopApp ? 'outside-desktop-app' : 'vault-locked',
      })
      disconnect()
      return
    }
    debugLog('desktop-realtime-bootstrap', 'effect:hydrate-dispatch')
    void hydrate()
    void refreshNotifications()
    return () => {
      debugLog('desktop-realtime-bootstrap', 'effect:cleanup-disconnect')
      disconnect()
    }
  }, [disconnect, hydrate, inDesktopApp, refreshNotifications, vault.enabled, vault.unlocked])

  useEffect(() => {
    debugLog('desktop-realtime-bootstrap', 'effect:online-listener-check', {
      inDesktopApp,
      vaultEnabled: vault.enabled,
      vaultUnlocked: vault.unlocked,
    })
    if (!inDesktopApp || (vault.enabled && !vault.unlocked)) {
      return
    }
    const refreshRealtime = (reason: string) => {
      debugLog('desktop-realtime-bootstrap', 'browser:realtime-resume-check', { reason })
      void reconnectIfStale(reason)
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
      debugLog('desktop-realtime-bootstrap', 'effect:remove-online-listener')
      window.removeEventListener('online', handleOnline)
      window.removeEventListener('focus', handleFocus)
      window.removeEventListener('pageshow', handlePageShow)
      document.removeEventListener('visibilitychange', handleVisible)
    }
  }, [inDesktopApp, reconnectIfStale, vault.enabled, vault.unlocked])

  return null
}
