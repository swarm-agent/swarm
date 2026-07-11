import { useCallback, useEffect, useState } from 'react'
import { Button } from '../../../../../components/ui/button'
import {
  deleteWebPushSubscription, deriveWebPushCapability, fetchWebPushStatus, isIOSDevice,
  listWebPushSubscriptions, saveWebPushSubscription, sendTestWebPush, urlBase64ToUint8Array,
  type WebPushCapability, type WebPushStatus,
} from '../../../notifications/web-push'

const descriptions: Record<WebPushCapability, string> = {
  unsupported: 'This browser does not support Web Push notifications.',
  insecure: 'Web Push requires a secure HTTPS connection.',
  'ios-home-screen-required': 'On iPhone or iPad, add Swarm to your Home Screen, then open it there to enable notifications.',
  denied: 'Notification permission is blocked. Allow notifications for this site in your browser settings, then refresh.',
  available: 'Notifications are available but not enabled on this device.',
  enabled: 'Notifications are enabled on this device.',
  stale: 'The browser and daemon subscription state differ. Enable or disable notifications to reconcile them.',
}

function standaloneMode(): boolean {
  const navigatorWithStandalone = navigator as Navigator & { standalone?: boolean }
  return navigatorWithStandalone.standalone === true || window.matchMedia('(display-mode: standalone)').matches
}

export function NotificationsSettingsPage() {
  const [status, setStatus] = useState<WebPushStatus | null>(null)
  const [localSubscription, setLocalSubscription] = useState<PushSubscription | null>(null)
  const [capability, setCapability] = useState<WebPushCapability>('available')
  const [busy, setBusy] = useState(false)
  const [message, setMessage] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)

  const refresh = useCallback(async () => {
    setError(null)
    try {
      const remoteStatus = await fetchWebPushStatus()
      const registration = 'serviceWorker' in navigator ? await navigator.serviceWorker.getRegistration('/') : undefined
      const subscription = registration && 'pushManager' in registration ? await registration.pushManager.getSubscription() : null
      setStatus(remoteStatus)
      setLocalSubscription(subscription)
      setCapability(deriveWebPushCapability({
        secure: window.isSecureContext,
        serviceWorker: 'serviceWorker' in navigator,
        pushManager: 'PushManager' in window,
        notification: 'Notification' in window,
        permission: 'Notification' in window ? Notification.permission : undefined,
        ios: isIOSDevice(navigator.userAgent, navigator.platform, navigator.maxTouchPoints),
        standalone: standaloneMode(), localSubscription: Boolean(subscription), serverEnabled: remoteStatus.enabled,
      }))
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to refresh notification status')
    }
  }, [])

  useEffect(() => { void refresh() }, [refresh])

  const run = async (action: () => Promise<string>) => {
    setBusy(true); setError(null); setMessage(null)
    try { setMessage(await action()); await refresh() }
    catch (err) { setError(err instanceof Error ? err.message : 'Notification operation failed') }
    finally { setBusy(false) }
  }

  const enable = () => run(async () => {
    if (!window.isSecureContext || !('serviceWorker' in navigator) || !('PushManager' in window) || !('Notification' in window)) {
      throw new Error(descriptions[!window.isSecureContext ? 'insecure' : 'unsupported'])
    }
    if (isIOSDevice(navigator.userAgent, navigator.platform, navigator.maxTouchPoints) && !standaloneMode()) {
      throw new Error(descriptions['ios-home-screen-required'])
    }
    // Permission is deliberately requested only in this direct click handler.
    const permission = Notification.permission === 'default' ? await Notification.requestPermission() : Notification.permission
    if (permission !== 'granted') throw new Error(descriptions.denied)
    const existingRegistration = await navigator.serviceWorker.getRegistration('/')
    if (!existingRegistration) await navigator.serviceWorker.register('/sw.js', { scope: '/' })
    const registration = await navigator.serviceWorker.ready
    const remoteStatus = await fetchWebPushStatus()
    let subscription = await registration.pushManager.getSubscription()
    if (!subscription) subscription = await registration.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: urlBase64ToUint8Array(remoteStatus.public_key) })
    await saveWebPushSubscription(subscription)
    return 'Notifications enabled.'
  })

  const disable = () => run(async () => {
    if (localSubscription) {
      const record = await saveWebPushSubscription(localSubscription)
      await deleteWebPushSubscription(record.id)
      const unsubscribed = await localSubscription.unsubscribe()
      if (!unsubscribed) throw new Error('The browser did not remove its local subscription')
    } else {
      const records = await listWebPushSubscriptions()
      await Promise.all(records.map((record) => deleteWebPushSubscription(record.id)))
    }
    return 'Notifications disabled.'
  })

  const test = () => run(async () => {
    const result = await sendTestWebPush()
    if (result.delivered < 1) throw new Error(result.errors?.join('; ') || 'No test notification was delivered')
    return `Test notification delivered to ${result.delivered} subscription${result.delivered === 1 ? '' : 's'}.`
  })

  return <section className="space-y-6">
    <header><h2 className="text-xl font-semibold">Notifications</h2><p className="mt-1 text-sm text-[var(--app-text-muted)]">Receive system notifications when Swarm has an update.</p></header>
    <div className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)] p-5">
      <div className="flex items-start justify-between gap-4"><div><h3 className="font-medium">Web Push</h3><p className="mt-1 text-sm text-[var(--app-text-muted)]">{descriptions[capability]}</p>{status ? <p className="mt-2 text-xs text-[var(--app-text-muted)]">Daemon subscriptions: {status.subscription_count}</p> : null}</div><span className="rounded-full border border-[var(--app-border)] px-3 py-1 text-xs capitalize">{capability.split('-').join(' ')}</span></div>
      {error ? <p role="alert" className="mt-4 text-sm text-red-500">{error}</p> : null}{message ? <p role="status" className="mt-4 text-sm text-green-600">{message}</p> : null}
      <div className="mt-5 flex flex-wrap gap-2">
        <Button onClick={() => void enable()} disabled={busy || capability === 'unsupported' || capability === 'insecure' || capability === 'ios-home-screen-required' || capability === 'denied'}>Enable</Button>
        <Button variant="outline" onClick={() => void disable()} disabled={busy || (!localSubscription && !status?.enabled)}>Disable</Button>
        <Button variant="outline" onClick={() => void refresh()} disabled={busy}>Refresh</Button>
        <Button variant="outline" onClick={() => void test()} disabled={busy || capability !== 'enabled'}>Send test notification</Button>
      </div>
    </div>
  </section>
}
