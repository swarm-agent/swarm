import assert from 'node:assert/strict'
import test from 'node:test'
import { createEmptyDesktopV3CacheState, desktopV3CacheReducer } from '../state/desktop-v3-cache-reducer'
import type { DesktopNotificationWire } from '../state/desktop-v3-cache-types'
import {
  isSafeActionURL,
  isSafeMediaURL,
  isSafeNotificationActionEndpoint,
} from './components/desktop-notifications-modal'

test('normalizes inbox notification with kind, payload and actions', () => {
  const wireNotification: DesktopNotificationWire = {
    id: 'notif_123',
    swarm_id: 'swarm_local',
    title: 'AI Deliverable Ready',
    body: 'Generated 5 posts and 1 video',
    category: 'inbox',
    kind: 'ai_deliverable',
    severity: 'info',
    status: 'active',
    payload: {
      media_url: 's3://vault/video.mp4',
      thread: ['Post 1', 'Post 2'],
    },
    actions: [
      {
        id: 'approve',
        label: 'Approve & Publish',
        action_type: 'publish',
        endpoint: '/v3/deliverables/del_1/approve',
        variant: 'primary',
      },
      {
        id: 'reject',
        label: 'Request Changes',
        variant: 'secondary',
      },
    ],
    created_at: 1000,
    updated_at: 1000,
  }

  const state = desktopV3CacheReducer(createEmptyDesktopV3CacheState(), {
    type: 'realtime.applyNotificationResource',
    frame: {
      type: 'notification.resource.updated',
      notification: wireNotification,
    } as any,
  })

  const stored = state.notificationsById['notif_123']
  assert.ok(stored)
  assert.equal(stored.id, 'notif_123')
  assert.equal(stored.kind, 'ai_deliverable')
  assert.equal(stored.category, 'inbox')
  assert.equal((stored.payload as any)?.media_url, 's3://vault/video.mp4')
  assert.equal(Array.isArray((stored.payload as any)?.thread), true)
  assert.equal(stored.actions?.length, 2)
  assert.equal(stored.actions?.[0].id, 'approve')
  assert.equal(stored.actions?.[0].endpoint, '/v3/deliverables/del_1/approve')
  assert.equal(stored.actions?.[1].id, 'reject')
})

test('validates safe and unsafe notification action endpoints', () => {
  // Safe endpoints
  assert.equal(isSafeNotificationActionEndpoint('/v3/deliverables/del_1/approve'), true)
  assert.equal(isSafeNotificationActionEndpoint('/v3/deliverables/del_1/dismiss'), true)
  assert.equal(isSafeNotificationActionEndpoint('/v1/notifications/notif_1/ack'), true)
  assert.equal(isSafeNotificationActionEndpoint('/v3/automations/v2/trigger'), true)
  assert.equal(isSafeNotificationActionEndpoint('/v3/sessions/sess_123'), true)
  assert.equal(isSafeNotificationActionEndpoint('/settings?tab=cloud'), true)
  assert.equal(isSafeNotificationActionEndpoint('/v1/storage/buckets/bkt_123/accept-canonical'), true)

  // Unsafe SSRF / Confused Deputy endpoints
  assert.equal(isSafeNotificationActionEndpoint('http://localhost:8765/v1/activate'), false)
  assert.equal(isSafeNotificationActionEndpoint('https://evil.com/hook'), false)
  assert.equal(isSafeNotificationActionEndpoint('//evil.com/hook'), false)
  assert.equal(isSafeNotificationActionEndpoint('/v1/environments/env-1/destroy'), false)
  assert.equal(isSafeNotificationActionEndpoint('/v1/keys/rotate'), false)
  assert.equal(isSafeNotificationActionEndpoint('/v3/auth/tokens/tok_1/revoke'), false)
  assert.equal(isSafeNotificationActionEndpoint('/v1/permissions/reset'), false)
  assert.equal(isSafeNotificationActionEndpoint('/v3/deliverables/../../etc/passwd'), false)
  assert.equal(isSafeNotificationActionEndpoint(''), false)
  assert.equal(isSafeNotificationActionEndpoint(null as any), false)
})

test('validates safe action and media URLs against XSS', () => {
  // Safe URLs
  assert.equal(isSafeActionURL('/workspace/session-1'), true)
  assert.equal(isSafeActionURL('https://swarmagent.dev'), true)
  assert.equal(isSafeActionURL('http://example.com/item'), true)

  // Unsafe URLs
  assert.equal(isSafeActionURL('javascript:alert(1)'), false)
  assert.equal(isSafeActionURL('javascript:void(0)'), false)
  assert.equal(isSafeActionURL('data:text/html,<script>alert(1)</script>'), false)
  assert.equal(isSafeActionURL('//malicious.com'), false)
  assert.equal(isSafeActionURL(''), false)
  assert.equal(isSafeActionURL(null as any), false)

  // Media URLs
  assert.equal(isSafeMediaURL('https://cdn.example.com/video.mp4'), true)
  assert.equal(isSafeMediaURL('/media/video.mp4'), true)
  assert.equal(isSafeMediaURL('blob:http://localhost:5555/uuid'), true)
  assert.equal(isSafeMediaURL('javascript:alert(1)'), false)
  assert.equal(isSafeMediaURL('data:text/html,...'), false)
})
