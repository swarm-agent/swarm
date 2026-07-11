import assert from 'node:assert/strict'
import test from 'node:test'
import { deriveWebPushCapability, isIOSDevice, urlBase64ToUint8Array } from './web-push'

test('converts URL-safe VAPID public keys', () => {
  const original = globalThis.atob
  globalThis.atob = (value: string) => Buffer.from(value, 'base64').toString('binary')
  try { assert.deepEqual([...urlBase64ToUint8Array('AQID-_8')], [1, 2, 3, 251, 255]) } finally { globalThis.atob = original }
})

test('derives precise capability and stale states', () => {
  const base = { secure: true, serviceWorker: true, pushManager: true, notification: true, permission: 'granted' as const, ios: false, standalone: false }
  assert.equal(deriveWebPushCapability({ ...base, localSubscription: true, serverEnabled: true }), 'enabled')
  assert.equal(deriveWebPushCapability({ ...base, localSubscription: true, serverEnabled: false }), 'stale')
  assert.equal(deriveWebPushCapability({ ...base, secure: false, localSubscription: false, serverEnabled: false }), 'insecure')
  assert.equal(deriveWebPushCapability({ ...base, permission: 'denied', localSubscription: false, serverEnabled: false }), 'denied')
})

test('requires Home Screen mode for iOS devices', () => {
  assert.equal(isIOSDevice('Mozilla/5.0 (iPhone)'), true)
  assert.equal(deriveWebPushCapability({ secure: true, serviceWorker: true, pushManager: true, notification: true, permission: 'default', ios: true, standalone: false, localSubscription: false, serverEnabled: false }), 'ios-home-screen-required')
})
