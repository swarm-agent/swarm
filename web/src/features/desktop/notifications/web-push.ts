import { requestJson } from '../../../app/api'

export interface WebPushStatus {
  enabled: boolean
  public_key: string
  subscription_count: number
}

export interface WebPushSubscriptionRecord {
  id: string
  created_at: number
  updated_at: number
}

interface StatusResponse { status?: WebPushStatus }
interface ListResponse { subscriptions?: WebPushSubscriptionRecord[] }
interface UpsertResponse { subscription?: WebPushSubscriptionRecord; changed?: boolean }
export interface WebPushTestResult { attempted: number; delivered: number; removed: number; errors?: string[] }
interface TestResponse { result?: WebPushTestResult }

export type WebPushCapability =
  | 'unsupported'
  | 'insecure'
  | 'ios-home-screen-required'
  | 'denied'
  | 'available'
  | 'enabled'
  | 'stale'

export function isIOSDevice(userAgent: string, platform = '', maxTouchPoints = 0): boolean {
  return /iPad|iPhone|iPod/.test(userAgent) || (platform === 'MacIntel' && maxTouchPoints > 1)
}

export function deriveWebPushCapability(input: {
  secure: boolean
  serviceWorker: boolean
  pushManager: boolean
  notification: boolean
  permission?: NotificationPermission
  ios: boolean
  standalone: boolean
  localSubscription: boolean
  serverEnabled: boolean
}): WebPushCapability {
  if (!input.secure) return 'insecure'
  if (!input.serviceWorker || !input.pushManager || !input.notification) return 'unsupported'
  if (input.ios && !input.standalone) return 'ios-home-screen-required'
  if (input.permission === 'denied') return 'denied'
  if (input.localSubscription && input.serverEnabled) return 'enabled'
  if (input.localSubscription !== input.serverEnabled) return 'stale'
  return 'available'
}

export function urlBase64ToUint8Array(value: string): Uint8Array<ArrayBuffer> {
  const padding = '='.repeat((4 - (value.length % 4)) % 4)
  const base64 = (value + padding).replace(/-/g, '+').replace(/_/g, '/')
  const bytes = Uint8Array.from(atob(base64), (character) => character.charCodeAt(0))
  return new Uint8Array(bytes.buffer)
}

export async function fetchWebPushStatus(): Promise<WebPushStatus> {
  const response = await requestJson<StatusResponse>('/v1/notifications/push')
  if (!response.status) throw new Error('Web Push status response was missing status')
  return response.status
}

export async function listWebPushSubscriptions(): Promise<WebPushSubscriptionRecord[]> {
  const response = await requestJson<ListResponse>('/v1/notifications/push/subscriptions')
  return response.subscriptions ?? []
}

export async function saveWebPushSubscription(subscription: PushSubscription): Promise<WebPushSubscriptionRecord> {
  const json = subscription.toJSON()
  if (!json.endpoint || !json.keys?.auth || !json.keys?.p256dh) throw new Error('Browser returned an incomplete push subscription')
  const response = await requestJson<UpsertResponse>('/v1/notifications/push/subscriptions', {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ endpoint: json.endpoint, keys: { auth: json.keys.auth, p256dh: json.keys.p256dh } }),
  })
  if (!response.subscription) throw new Error('Web Push subscription response was missing subscription')
  return response.subscription
}

export async function deleteWebPushSubscription(id: string): Promise<void> {
  await requestJson(`/v1/notifications/push/subscriptions/${encodeURIComponent(id)}`, { method: 'DELETE' })
}

export async function sendTestWebPush(): Promise<WebPushTestResult> {
  const response = await requestJson<TestResponse>('/v1/notifications/push/test', { method: 'POST' })
  if (!response.result) throw new Error('Test notification response was missing delivery results')
  return response.result
}
