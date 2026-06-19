import assert from 'node:assert/strict'
import test from 'node:test'

import { messageA1, projectionA, runIntentA, sessionA, snapshotFixture, tombstoneB } from './desktop-v3-cache.backend-fixtures'
import { createDesktopV3CacheOwner, parseDesktopV3CacheOwnerKey } from './desktop-v3-cache-owner'
import {
  buildPersistedDesktopV3MessageTailV1,
  createPersistedDesktopV3MessageTailKey,
  DESKTOP_V3_CACHE_SCHEMA_VERSION,
  validatePersistedDesktopV3MessageTailV1,
  validatePersistedDesktopV3OwnerV1,
  type PersistedDesktopV3OwnerV1,
} from './desktop-v3-cache-persisted-types'
import type { MessageSnapshot } from './desktop-v3-cache-types'

const owner = createDesktopV3CacheOwner({
  origin: 'https://desktop.example.test/app',
  accountScopeId: 'acct-1',
  userId: 'user-1',
  surface: 'desktop',
})

function ownerRecordFixture(overrides: Partial<PersistedDesktopV3OwnerV1> = {}): PersistedDesktopV3OwnerV1 {
  const snapshot = snapshotFixture()
  const scopeId = snapshot.scope_id
  return {
    schemaVersion: DESKTOP_V3_CACHE_SCHEMA_VERSION,
    ownerKey: owner.key,
    owner,
    persistedAt: 123,
    selectedSessionId: sessionA.id,
    sidebarScopeId: scopeId,
    syncScopesById: {
      [scopeId]: {
        scopeId,
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
      [scopeId]: [sessionA.id],
    },
    sidebarSessionsById: {
      [sessionA.id]: {
        session: sessionA,
        projection: projectionA,
        tombstone: { ...tombstoneB, session_id: sessionA.id },
        runIntents: [runIntentA],
      },
    },
    ...overrides,
  }
}

function messageTailFixture(overrides: Partial<MessageSnapshot> = {}) {
  return buildPersistedDesktopV3MessageTailV1({
    ownerKey: owner.key,
    sessionId: sessionA.id,
    persistedAt: 456,
    messages: [{ ...messageA1, ...overrides }],
    sourceMessageCount: sessionA.message_count,
    sourceLastMessageAt: sessionA.last_message_at,
    sourceProjectionHighWatermarkSeq: projectionA.projection_high_watermark_seq,
    hydratedAt: 457,
  })
}

test('DesktopV3CacheOwner builds stable owner-isolated key and rejects empty identity fields', () => {
  assert.equal(owner.origin, 'https://desktop.example.test')
  assert.equal(owner.key, 'desktop-v3-cache:v1:https%3A%2F%2Fdesktop.example.test:acct-1:user-1:desktop')
  assert.deepEqual(parseDesktopV3CacheOwnerKey(owner.key), owner)

  assert.throws(() => createDesktopV3CacheOwner({ origin: '', accountScopeId: 'acct-1', userId: 'user-1' }), /origin is required/)
  assert.throws(() => createDesktopV3CacheOwner({ origin: 'https://example.test', accountScopeId: '', userId: 'user-1' }), /accountScopeId is required/)
  assert.throws(() => createDesktopV3CacheOwner({ origin: 'https://example.test', accountScopeId: 'acct-1', userId: '' }), /userId is required/)
  assert.throws(() => parseDesktopV3CacheOwnerKey('not-an-owner-key'), /unsupported prefix/)
})

test('persisted owner validator accepts schema v1 sidebar contract', () => {
  const record = ownerRecordFixture()

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

  const wrongKey = validatePersistedDesktopV3MessageTailV1({ ...tail, key: createPersistedDesktopV3MessageTailKey(owner.key, 'session-other') }, owner.key, sessionA.id)
  assert.equal(wrongKey.ok, false)
  assert.equal(wrongKey.ok ? '' : wrongKey.reason, 'message tail key mismatch')
})

test('persisted message validation preserves content byte-for-byte', () => {
  const content = '  first line\n    code line\n\n'
  const tail = messageTailFixture({ content })
  const restored = validatePersistedDesktopV3MessageTailV1(tail, owner.key, sessionA.id)

  assert.equal(restored.ok, true)
  assert.equal(restored.ok && restored.value.messages[0].content, content)

  const whitespaceOnly = validatePersistedDesktopV3MessageTailV1(messageTailFixture({ content: '   ' }), owner.key, sessionA.id)
  assert.equal(whitespaceOnly.ok, true)
  assert.equal(whitespaceOnly.ok && whitespaceOnly.value.messages[0].content, '   ')
})

test('persisted sync scope validation is fail-closed', () => {
  const base = ownerRecordFixture()
  const scopeId = Object.keys(base.syncScopesById)[0]
  const scope = base.syncScopesById[scopeId]

  assert.equal(validatePersistedDesktopV3OwnerV1({
    ...base,
    syncScopesById: { [scopeId]: { ...scope, streamKind: 'legacy.stream' } },
  }, owner.key).ok, false)

  const stringBootstrap = validatePersistedDesktopV3OwnerV1({
    ...base,
    syncScopesById: { [scopeId]: { ...scope, needsBootstrap: 'false' } },
  }, owner.key)
  assert.equal(stringBootstrap.ok, false)
  assert.equal(stringBootstrap.ok ? '' : stringBootstrap.reason, `syncScopesById.${scopeId}.needsBootstrap must be boolean`)

  assert.equal(validatePersistedDesktopV3OwnerV1({
    ...base,
    syncScopesById: { ['different-scope-key']: scope },
  }, owner.key).ok, false)

  assert.equal(validatePersistedDesktopV3OwnerV1({
    ...base,
    syncScopesById: { [scopeId]: { ...scope, replayPath: '/legacy/stream' } },
  }, owner.key).ok, false)

  assert.equal(validatePersistedDesktopV3OwnerV1({
    ...base,
    syncScopesById: { [scopeId]: { ...scope, replayTransport: 'websocket' } },
  }, owner.key).ok, false)
})

test('persisted owner validation rejects broken session references', () => {
  const base = ownerRecordFixture()
  const scopeId = Object.keys(base.sessionOrderByScope)[0]

  assert.equal(validatePersistedDesktopV3OwnerV1({ ...base, selectedSessionId: 'missing-session' }, owner.key).ok, false)
  const missingSyncScope = validatePersistedDesktopV3OwnerV1({ ...base, sidebarScopeId: 'missing-scope' }, owner.key)
  assert.equal(missingSyncScope.ok, false)
  assert.equal(missingSyncScope.ok ? '' : missingSyncScope.reason, 'persisted sidebarScopeId does not resolve to a persisted sync scope')
  const missingSessionOrder = validatePersistedDesktopV3OwnerV1({ ...base, sessionOrderByScope: {}, sidebarScopeId: scopeId }, owner.key)
  assert.equal(missingSessionOrder.ok, false)
  assert.equal(missingSessionOrder.ok ? '' : missingSessionOrder.reason, 'persisted sidebarScopeId does not resolve to a persisted session order')
  assert.equal(validatePersistedDesktopV3OwnerV1({ ...base, sessionOrderByScope: { [scopeId]: [sessionA.id, sessionA.id] } }, owner.key).ok, false)
  assert.equal(validatePersistedDesktopV3OwnerV1({ ...base, sessionOrderByScope: { [scopeId]: [sessionA.id, 'missing-session'] } }, owner.key).ok, false)
  assert.equal(validatePersistedDesktopV3OwnerV1({ ...base, sessionOrderByScope: { ['missing-scope']: [sessionA.id] } }, owner.key).ok, false)

  const whitespaceScope = validatePersistedDesktopV3OwnerV1({ ...base, sessionOrderByScope: { [` ${scopeId}`]: [sessionA.id] } }, owner.key)
  assert.equal(whitespaceScope.ok, false)
  assert.equal(whitespaceScope.ok ? '' : whitespaceScope.reason, 'sessionOrderByScope key must not include leading or trailing whitespace')

  const tombstoneEmbeddedMismatch = validatePersistedDesktopV3OwnerV1({
    ...base,
    sidebarSessionsById: {
      [sessionA.id]: {
        ...base.sidebarSessionsById[sessionA.id],
        tombstone: {
          ...tombstoneB,
          session_id: sessionA.id,
          session: { ...sessionA, id: 'other-session' },
        },
      },
    },
  }, owner.key)
  assert.equal(tombstoneEmbeddedMismatch.ok, false)
  assert.equal(tombstoneEmbeddedMismatch.ok ? '' : tombstoneEmbeddedMismatch.reason, `sidebarSessionsById.${sessionA.id}.tombstone.session.id mismatch`)
})

test('persisted validators reject nested owner identity conflicts', () => {
  const sidebarConflict = validatePersistedDesktopV3OwnerV1({
    ...ownerRecordFixture(),
    sidebarSessionsById: {
      [sessionA.id]: {
        session: { ...sessionA, account_scope_id: 'other-account' },
      },
    },
  }, owner.key)
  assert.equal(sidebarConflict.ok, false)
  assert.match(sidebarConflict.ok ? '' : sidebarConflict.reason, /account_scope_id conflicts/)

  const validTail = messageTailFixture()
  const messageConflict = validatePersistedDesktopV3MessageTailV1({
    ...validTail,
    messages: [{ ...validTail.messages[0], user_id: 'other-user' }],
  }, owner.key, sessionA.id)
  assert.equal(messageConflict.ok, false)
  assert.match(messageConflict.ok ? '' : messageConflict.reason, /user_id conflicts/)
})

test('persisted numeric metadata must be non-negative safe integers', () => {
  assert.equal(validatePersistedDesktopV3OwnerV1({ ...ownerRecordFixture(), persistedAt: -1 }, owner.key).ok, false)
  assert.equal(validatePersistedDesktopV3OwnerV1({
    ...ownerRecordFixture(),
    sidebarSessionsById: {
      [sessionA.id]: { session: { ...sessionA, message_count: 1.5 } },
    },
  }, owner.key).ok, false)

  const validTail = messageTailFixture()
  const fractionalSeq = validatePersistedDesktopV3MessageTailV1({
    ...validTail,
    messages: [{ ...validTail.messages[0], global_seq: 1.25 }],
  }, owner.key, sessionA.id)
  assert.equal(fractionalSeq.ok, false)

  assert.throws(() => buildPersistedDesktopV3MessageTailV1({
    ownerKey: owner.key,
    sessionId: sessionA.id,
    persistedAt: 456,
    messages: [messageA1],
    sourceMessageCount: -1,
  }), /sourceMessageCount must be a non-negative safe integer/)

  assert.throws(() => buildPersistedDesktopV3MessageTailV1({
    ownerKey: owner.key,
    sessionId: sessionA.id,
    persistedAt: 456,
    messages: [{ ...messageA1, created_at: Number.MAX_SAFE_INTEGER + 1 }],
  }), /created_at must be a non-negative safe integer/)
})
