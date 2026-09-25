import assert from 'node:assert/strict'
import test from 'node:test'
import { createEmptyDesktopV3CacheState, desktopV3CacheReducer } from '../state/desktop-v3-cache-reducer'
import type { DesktopNotificationWire } from '../state/desktop-v3-cache-types'

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
