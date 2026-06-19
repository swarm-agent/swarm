import test from 'node:test'
import assert from 'node:assert/strict'

import { createDesktopV3CacheOwner } from './desktop-v3-cache-owner'
import {
  validatePersistedDesktopV3MessageTailV1,
  validatePersistedDesktopV3OwnerV1,
  type PersistedDesktopV3MessageTailV1,
  type PersistedDesktopV3OwnerV1,
} from './desktop-v3-cache-persisted-types'
import { messageA1, projectionA, sessionA } from './desktop-v3-cache.backend-fixtures'

test('desktop v3 cache owner key is origin, account, user, and surface isolated', () => {
  const owner = createDesktopV3CacheOwner({
    origin: ' https://app.example.test ',
    accountScopeId: 'acct-a',
    userId: 'user-a',
    surface: 'desktop',
  })

  assert.equal(owner.origin, 'https://app.example.test')
  assert.equal(owner.key, 'desktop-v3-cache:v1:https%3A%2F%2Fapp.example.test:acct-a:user-a:desktop')
  assert.notEqual(owner.key, createDesktopV3CacheOwner({
    origin: 'https://app.example.test',
    accountScopeId: 'acct-b',
    userId: 'user-a',
    surface: 'desktop',
  }).key)
})

test('desktop v3 cache owner rejects empty required identity fields', () => {
  assert.throws(() => createDesktopV3CacheOwner({ origin: '', accountScopeId: 'acct-a', userId: 'user-a' }), /origin is required/)
  assert.throws(() => createDesktopV3CacheOwner({ origin: 'https://app.example.test', accountScopeId: '', userId: 'user-a' }), /accountScopeId is required/)
  assert.throws(() => createDesktopV3CacheOwner({ origin: 'https://app.example.test', accountScopeId: 'acct-a', userId: '' }), /userId is required/)
})

test('persisted owner validator accepts schema v1 owner/sidebar projection', () => {
  const owner = createDesktopV3CacheOwner({ origin: 'https://app.example.test', accountScopeId: 'acct-a', userId: 'user-a' })
  const record: PersistedDesktopV3OwnerV1 = {
    schemaVersion: 1,
    owner,
    savedAt: 123,
    syncScopesById: {},
    sessionsById: { [sessionA.id]: sessionA },
    projectionsBySession: { [sessionA.id]: projectionA },
    sessionOrderByScope: { scope: [sessionA.id] },
    tombstonesBySession: {},
    runIntentsBySession: {},
    currentRunIntentBySession: {},
    selectedSessionId: sessionA.id,
  }

  const result = validatePersistedDesktopV3OwnerV1(record)
  assert.equal(result.ok, true)
  if (result.ok) assert.equal(result.value.owner.key, owner.key)
})

test('persisted validators treat corrupt or future records as deleteable cold misses', () => {
  assert.deepEqual(validatePersistedDesktopV3OwnerV1({ schemaVersion: 2 }).ok, false)
  const invalidOwner = validatePersistedDesktopV3OwnerV1({ schemaVersion: 2 })
  assert.equal(invalidOwner.ok, false)
  if (!invalidOwner.ok) assert.equal(invalidOwner.delete, true)

  const invalidTail = validatePersistedDesktopV3MessageTailV1({ schemaVersion: 1, ownerKey: 'owner', sessionId: 'session', savedAt: 1, messages: [{}], source: 'network' })
  assert.equal(invalidTail.ok, false)
  if (!invalidTail.ok) assert.equal(invalidTail.delete, true)
})

test('persisted message tail validator accepts source metadata', () => {
  const record: PersistedDesktopV3MessageTailV1 = {
    schemaVersion: 1,
    ownerKey: 'owner-key',
    sessionId: sessionA.id,
    savedAt: 123,
    messages: [messageA1],
    sourceMessageCount: 2,
    sourceLastMessageAt: 10,
    sourceProjectionHighWatermarkSeq: 2,
    hydratedAt: 456,
    source: 'network',
  }

  const result = validatePersistedDesktopV3MessageTailV1(record)
  assert.equal(result.ok, true)
  if (result.ok) {
    assert.equal(result.value.sourceMessageCount, 2)
    assert.equal(result.value.sourceProjectionHighWatermarkSeq, 2)
  }
})
