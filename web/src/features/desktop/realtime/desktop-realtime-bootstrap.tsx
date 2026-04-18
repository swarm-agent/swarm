import { useEffect } from 'react'
import { useMatchRoute } from '@tanstack/react-router'
import { debugLog } from '../../../lib/debug-log'
import { useDesktopStore } from '../state/use-desktop-store'

export function DesktopRealtimeBootstrap() {
  const hydrate = useDesktopStore((state) => state.hydrate)
  const disconnect = useDesktopStore((state) => state.disconnect)
  const connect = useDesktopStore((state) => state.connect)
  const vault = useDesktopStore((state) => state.vault)
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
    return () => {
      debugLog('desktop-realtime-bootstrap', 'effect:cleanup-disconnect')
      disconnect()
    }
  }, [disconnect, hydrate, inDesktopApp, vault.enabled, vault.unlocked])

  useEffect(() => {
    debugLog('desktop-realtime-bootstrap', 'effect:online-listener-check', {
      inDesktopApp,
      vaultEnabled: vault.enabled,
      vaultUnlocked: vault.unlocked,
    })
    if (!inDesktopApp || (vault.enabled && !vault.unlocked)) {
      return
    }
    const handleOnline = () => {
      debugLog('desktop-realtime-bootstrap', 'browser:online-event')
      void connect()
    }
    window.addEventListener('online', handleOnline)
    return () => {
      debugLog('desktop-realtime-bootstrap', 'effect:remove-online-listener')
      window.removeEventListener('online', handleOnline)
    }
  }, [connect, inDesktopApp, vault.enabled, vault.unlocked])

  return null
}
