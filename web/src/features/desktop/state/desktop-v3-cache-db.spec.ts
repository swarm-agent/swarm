import 'fake-indexeddb/auto'

import assert from 'node:assert/strict'
import test from 'node:test'
import { openDB } from 'idb'

import {
  DESKTOP_V3_CACHE_BY_OWNER_INDEX,
  DESKTOP_V3_CACHE_DB_NAME,
  DESKTOP_V3_CACHE_DB_VERSION,
  DESKTOP_V3_CACHE_MESSAGE_TAILS_STORE,
  DESKTOP_V3_CACHE_OWNERS_STORE,
  deleteDesktopV3OwnerCache,
  readAllDesktopV3MessageTails,
  readDesktopV3MessageTail,
  readDesktopV3MessageTails,
  readDesktopV3Owner,
  resetDesktopV3CacheDBForTests,
  writeDesktopV3OwnerAndTails,
} from './desktop-v3-cache-db'
import { createDesktopV3CacheOwner, type DesktopV3CacheOwner } from './desktop-v3-cache-owner'
import {
  buildPersistedDesktopV3MessageTailV1,
  DESKTOP_V3_CACHE_SCHEMA_VERSION,
  validatePersistedDesktopV3OwnerV1,
  type PersistedDesktopV3MessageTailV1,
  type PersistedDesktopV3OwnerV1,
} from './desktop-v3-cache-persisted-types'
import { messageA1, messageA2, messageB1, projectionA, projectionB, sessionA, sessionB } from './desktop-v3-cache.backend-fixtures'
import type { MessageSnapshot, SessionSnapshot, V3SessionProjection } from './desktop-v3-cache-types'

const ownerA = createDesktopV3CacheOwner({
  origin: 'https://desktop.example.test',
  accountScopeId: 'acct-a',
  userId: 'user-a',
  surface: 'desktop',
})

const ownerB = createDesktopV3CacheOwner({
  origin: 'https://desktop.example.test',
  accountScopeId: 'acct-b',
  userId: 'user-b',
  surface: 'desktop',
})

function ownerRecordFixture(
  owner: DesktopV3CacheOwner,
  session: SessionSnapshot,
  projection: V3SessionProjection,
  persistedAt = 1_000,
): PersistedDesktopV3OwnerV1 {
  const scopeId = `scope:${owner.accountScopeId}:${session.id}`
  return {
    schemaVersion: DESKTOP_V3_CACHE_SCHEMA_VERSION,
    ownerKey: owner.key,
    owner,
    persistedAt,
    selectedSessionId: session.id,
    sidebarScopeId: scopeId,
    syncScopesById: {
      [scopeId]: {
        scopeId,
        surface: owner.surface,
        streamKind: 'v3.sync.snapshot',
        selectorFilterHash: `selector:${session.id}`,
        resourceSet: 'messages,run_intents',
        selector: { kind: 'session_ids', session_ids: [session.id] },
        endpointCursor: `cursor:${session.id}`,
        replayPath: '/v3/sync/stream',
        replayTransport: 'http_post',
        needsBootstrap: false,
      },
    },
    sessionOrderByScope: {
      [scopeId]: [session.id],
    },
    sidebarSessionsById: {
      [session.id]: {
        session,
        projection,
      },
    },
  }
}

function messageTailFixture(
  owner: DesktopV3CacheOwner,
  session: SessionSnapshot,
  projection: V3SessionProjection,
  messages: MessageSnapshot[],
  persistedAt = 2_000,
): PersistedDesktopV3MessageTailV1 {
  return buildPersistedDesktopV3MessageTailV1({
    ownerKey: owner.key,
    sessionId: session.id,
    persistedAt,
    messages,
    sourceMessageCount: session.message_count,
    sourceLastMessageAt: session.last_message_at,
    sourceProjectionHighWatermarkSeq: projection.projection_high_watermark_seq,
    hydratedAt: persistedAt + 1,
  })
}

async function resetDB(): Promise<void> {
  assert.equal(await resetDesktopV3CacheDBForTests(), true)
}

function normalizedOwnerRecord(record: PersistedDesktopV3OwnerV1): PersistedDesktopV3OwnerV1 {
  const result = validatePersistedDesktopV3OwnerV1(record, record.ownerKey)
  assert.equal(result.ok, true)
  return result.value
}

test('Desktop V3 IndexedDB adapter creates schema and roundtrips owner plus message tails', async () => {
  await resetDB()
  const owner = ownerRecordFixture(ownerA, sessionA, projectionA)
  const tail = messageTailFixture(ownerA, sessionA, projectionA, [messageA1, messageA2])

  assert.equal(await writeDesktopV3OwnerAndTails(owner, [tail]), true)

  assert.deepEqual(await readDesktopV3Owner(ownerA.key), normalizedOwnerRecord(owner))
  assert.deepEqual(await readDesktopV3MessageTail(ownerA.key, sessionA.id), tail)
  assert.deepEqual((await readAllDesktopV3MessageTails(ownerA.key)).map((entry) => entry.sessionId), [sessionA.id])

  const db = await openDB(DESKTOP_V3_CACHE_DB_NAME, DESKTOP_V3_CACHE_DB_VERSION)
  try {
    assert.equal(db.objectStoreNames.contains(DESKTOP_V3_CACHE_OWNERS_STORE), true)
    assert.equal(db.objectStoreNames.contains(DESKTOP_V3_CACHE_MESSAGE_TAILS_STORE), true)
    const tx = db.transaction(DESKTOP_V3_CACHE_MESSAGE_TAILS_STORE, 'readonly')
    assert.equal(tx.store.indexNames.contains(DESKTOP_V3_CACHE_BY_OWNER_INDEX), true)
  } finally {
    db.close()
  }

  await resetDB()
})

test('Desktop V3 IndexedDB adapter isolates reads by owner key', async () => {
  await resetDB()
  const ownerRecordA = ownerRecordFixture(ownerA, sessionA, projectionA)
  const ownerRecordB = ownerRecordFixture(ownerB, sessionB, projectionB)
  const tailA = messageTailFixture(ownerA, sessionA, projectionA, [messageA1, messageA2])
  const tailB = messageTailFixture(ownerB, sessionB, projectionB, [messageB1])

  assert.equal(await writeDesktopV3OwnerAndTails(ownerRecordA, [tailA]), true)
  assert.equal(await writeDesktopV3OwnerAndTails(ownerRecordB, [tailB]), true)

  assert.equal((await readDesktopV3Owner(ownerA.key))?.ownerKey, ownerA.key)
  assert.equal((await readDesktopV3Owner(ownerB.key))?.ownerKey, ownerB.key)
  assert.equal(await readDesktopV3MessageTail(ownerA.key, sessionB.id), undefined)
  assert.equal((await readDesktopV3MessageTail(ownerB.key, sessionB.id))?.ownerKey, ownerB.key)
  assert.deepEqual((await readAllDesktopV3MessageTails(ownerA.key)).map((entry) => entry.ownerKey), [ownerA.key])
  assert.deepEqual((await readAllDesktopV3MessageTails(ownerB.key)).map((entry) => entry.ownerKey), [ownerB.key])

  await resetDB()
})

test('Desktop V3 IndexedDB owner delete removes only that owner and its tails', async () => {
  await resetDB()
  const ownerRecordA = ownerRecordFixture(ownerA, sessionA, projectionA)
  const ownerRecordB = ownerRecordFixture(ownerB, sessionB, projectionB)
  const tailA = messageTailFixture(ownerA, sessionA, projectionA, [messageA1, messageA2])
  const tailB = messageTailFixture(ownerB, sessionB, projectionB, [messageB1])

  assert.equal(await writeDesktopV3OwnerAndTails(ownerRecordA, [tailA]), true)
  assert.equal(await writeDesktopV3OwnerAndTails(ownerRecordB, [tailB]), true)
  assert.equal(await deleteDesktopV3OwnerCache(ownerA.key), true)

  assert.equal(await readDesktopV3Owner(ownerA.key), undefined)
  assert.deepEqual(await readAllDesktopV3MessageTails(ownerA.key), [])
  assert.deepEqual(await readDesktopV3Owner(ownerB.key), normalizedOwnerRecord(ownerRecordB))
  assert.deepEqual(await readDesktopV3MessageTail(ownerB.key, sessionB.id), tailB)

  await resetDB()
})

test('Desktop V3 IndexedDB adapter cold-misses and prunes invalid persisted records', async () => {
  await resetDB()
  const owner = ownerRecordFixture(ownerA, sessionA, projectionA)
  const tail = messageTailFixture(ownerA, sessionA, projectionA, [messageA1, messageA2])

  assert.equal(await writeDesktopV3OwnerAndTails(owner, [tail]), true)

  const db = await openDB(DESKTOP_V3_CACHE_DB_NAME, DESKTOP_V3_CACHE_DB_VERSION)
  try {
    await db.put(DESKTOP_V3_CACHE_OWNERS_STORE, { ...owner, persistedAt: -1 })
    await db.put(DESKTOP_V3_CACHE_MESSAGE_TAILS_STORE, { ...tail, persistedAt: -1 })
  } finally {
    db.close()
  }

  assert.equal(await readDesktopV3Owner(ownerA.key), undefined)
  assert.equal(await readDesktopV3MessageTail(ownerA.key, sessionA.id), undefined)

  await resetDB()
})

test('Desktop V3 IndexedDB adapter reports blocked storage deletion as fallback failure', async () => {
  await resetDB()
  const externalDB = await openDB(DESKTOP_V3_CACHE_DB_NAME, DESKTOP_V3_CACHE_DB_VERSION)
  try {
    assert.equal(await resetDesktopV3CacheDBForTests(), false)
  } finally {
    externalDB.close()
  }

  await new Promise((resolve) => setTimeout(resolve, 0))
  await resetDB()
})

test('Desktop V3 IndexedDB adapter returns fallback when open is blocked by pending delete', async () => {
  await resetDB()
  const owner = ownerRecordFixture(ownerA, sessionA, projectionA)
  assert.equal(await writeDesktopV3OwnerAndTails(owner), true)

  const externalDB = await openDB(DESKTOP_V3_CACHE_DB_NAME, DESKTOP_V3_CACHE_DB_VERSION)
  try {
    assert.equal(await resetDesktopV3CacheDBForTests(), false)
    assert.equal(await readDesktopV3Owner(ownerA.key), undefined)
  } finally {
    externalDB.close()
  }

  await new Promise((resolve) => setTimeout(resolve, 0))
  await resetDB()
})


test('Desktop V3 IndexedDB targeted tail read deduplicates, owner-validates, and prunes only malformed records', async () => {
  await resetDB()
  const ownerRecordA = ownerRecordFixture(ownerA, sessionA, projectionA)
  const ownerRecordB = ownerRecordFixture(ownerB, sessionB, projectionB)
  const tailA = messageTailFixture(ownerA, sessionA, projectionA, [messageA1, messageA2])
  const tailB = messageTailFixture(ownerA, sessionB, projectionB, [messageB1])
  const foreignTail = messageTailFixture(ownerB, sessionB, projectionB, [messageB1])

  assert.equal(await writeDesktopV3OwnerAndTails(ownerRecordA, [tailA, tailB]), true)
  assert.equal(await writeDesktopV3OwnerAndTails(ownerRecordB, [foreignTail]), true)

  const db = await openDB(DESKTOP_V3_CACHE_DB_NAME, DESKTOP_V3_CACHE_DB_VERSION)
  try {
    await db.put(DESKTOP_V3_CACHE_MESSAGE_TAILS_STORE, { ...tailB, key: tailB.key, sessionId: sessionA.id })
  } finally {
    db.close()
  }

  const targeted = await readDesktopV3MessageTails(ownerA.key, [sessionA.id, sessionA.id, sessionB.id])
  assert.deepEqual(targeted.map((tail) => tail.sessionId), [sessionA.id])
  assert.deepEqual((await readAllDesktopV3MessageTails(ownerA.key)).map((tail) => tail.sessionId), [sessionA.id])
  assert.deepEqual((await readAllDesktopV3MessageTails(ownerB.key)).map((tail) => tail.sessionId), [sessionB.id])

  await resetDB()
})
