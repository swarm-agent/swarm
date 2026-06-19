import assert from 'node:assert/strict'
import test from 'node:test'

import { projectionA, runIntentA, sessionA, snapshotFixture, tombstoneB } from './desktop-v3-cache.backend-fixtures'
import { createDesktopV3CacheOwner } from './desktop-v3-cache-owner'
import {
  buildPersistedDesktopV3MessageTailV1,
  createPersistedDesktopV3MessageTailKey,
  DESKTOP_V3_CACHE_SCHEMA_VERSION,
  validatePersistedDesktopV3MessageTailV1,
  validatePersistedDesktopV3OwnerV1,
  type PersistedDesktopV3OwnerV1,
} from './desktop-v3-cache-persisted-types'

const owner = createDesktopV3CacheOwner({
  origin: 'https://desktop.example.test/app',
  accountScopeId: 'acct-1',
  userId: 'user-1',
  surface: 'desktop',
})

test('DesktopV3CacheOwner builds stable owner-isolated key and rejects empty identity fields', () => {
  assert.equal(owner.origin, 'https://desktop.example.test')
  assert.equal(owner.key, 'desktop-v3-cache:v1:https%3A%2F%2Fdesktop.example.test:acct-1:user-1:desktop')

  assert.throws(() => createDesktopV3CacheOwner({ origin: '', accountScopeId: 'acct-1', userId: 'user-1' }), /origin is required/)
  assert.throws(() => createDesktopV3CacheOwner({ origin: 'https://example.test', accountScopeId: '', userId: 'user-1' }), /accountScopeId is required/)
  assert.throws(() => createDesktopV3CacheOwner({ origin: 'https://example.test', accountScopeId: 'acct-1', userId: '' }), /userId is required/)
})

test('persisted owner validator accepts schema v1 sidebar contract', () => {
  const snapshot = snapshotFixture()
  const record: PersistedDesktopV3OwnerV1 = {
    schemaVersion: DESKTOP_V3_CACHE_SCHEMA_VERSION,
    ownerKey: owner.key,
    owner,
    persistedAt: 123,
    selectedSessionId: sessionA.id,
    syncScopesById: {
      [snapshot.scope_id]: {
        scopeId: snapshot.scope_id,
        surface: 'desktop',
        streamKind: 'v3.sync.snapshot',
        selectorFilterHash: 'selector-hash',
        resourceSet: 'messages,run_intents',
        selector: snapshot.selector,
        endpointCursor: snapshot.snapshot_endpoint_cursor,
        replayPath: '/v3/sync/stream',
        replayTransport: 'http_post',
        needsBootstrap: false,
      },
    },
    sessionOrderByScope: {
      [snapshot.scope_id]: [sessionA.id],
    },
    sidebarSessionsById: {
      [sessionA.id]: {
        session: sessionA,
        projection: projectionA,
        tombstone: { ...tombstoneB, session_id: sessionA.id },
        runIntents: [runIntentA],
      },
    },
  }

  const result = validatePersistedDesktopV3OwnerV1(record, owner.key)
  assert.equal(result.ok, true)
  assert.equal(result.ok && result.value.owner.key, owner.key)
  assert.equal(result.ok && result.value.sidebarSessionsById[sessionA.id].projection?.projection_high_watermark_seq, 2)
})

test('persisted validators convert invalid or future records into deleteable cold misses', () => {
  assert.deepEqual(validatePersistedDesktopV3OwnerV1({ schemaVersion: 2 }, owner.key), {
    ok: false,
    deleteRecord: true,
    reason: 'unsupported schema version 2',
  })

  const wrongOwnerTail = buildPersistedDesktopV3MessageTailV1({
    ownerKey: owner.key,
    sessionId: sessionA.id,
    persistedAt: 456,
    messages: snapshotFixture().messages_by_session?.[sessionA.id] ?? [],
  })
  const result = validatePersistedDesktopV3MessageTailV1(wrongOwnerTail, 'other-owner', sessionA.id)
  assert.equal(result.ok, false)
  assert.equal(result.ok ? '' : result.reason, 'ownerKey mismatch')
})

test('persisted message tail key and validator enforce owner/session isolation', () => {
  const messages = snapshotFixture().messages_by_session?.[sessionA.id] ?? []
  const tail = buildPersistedDesktopV3MessageTailV1({
    ownerKey: owner.key,
    sessionId: sessionA.id,
    persistedAt: 456,
    messages,
    sourceMessageCount: sessionA.message_count,
    sourceLastMessageAt: sessionA.last_message_at,
    sourceProjectionHighWatermarkSeq: projectionA.projection_high_watermark_seq,
    hydratedAt: 457,
  })

  assert.equal(tail.key, createPersistedDesktopV3MessageTailKey(owner.key, sessionA.id))
  const result = validatePersistedDesktopV3MessageTailV1(tail, owner.key, sessionA.id)
  assert.equal(result.ok, true)
  assert.deepEqual(result.ok && result.value.messages.map((message) => message.id), ['msg-a-1', 'msg-a-2'])
  assert.equal(result.ok && result.value.sourceProjectionHighWatermarkSeq, 2)

  const wrongSession = validatePersistedDesktopV3MessageTailV1(tail, owner.key, 'session-other')
  assert.equal(wrongSession.ok, false)
  assert.equal(wrongSession.ok ? '' : wrongSession.reason, 'sessionId mismatch')
})
