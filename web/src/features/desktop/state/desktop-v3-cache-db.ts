import { deleteDB, openDB, type DBSchema, type IDBPDatabase } from 'idb'

import { parseDesktopV3CacheOwnerKey } from './desktop-v3-cache-owner'
import {
  createPersistedDesktopV3MessageTailKey,
  validatePersistedDesktopV3MessageTailV1,
  validatePersistedDesktopV3OwnerV1,
  type PersistedDesktopV3MessageTailV1,
  type PersistedDesktopV3OwnerV1,
} from './desktop-v3-cache-persisted-types'

export const DESKTOP_V3_CACHE_DB_NAME = 'swarm-desktop-v3-cache'
export const DESKTOP_V3_CACHE_DB_VERSION = 1
export const DESKTOP_V3_CACHE_OWNERS_STORE = 'owners'
export const DESKTOP_V3_CACHE_MESSAGE_TAILS_STORE = 'messageTails'
export const DESKTOP_V3_CACHE_BY_OWNER_INDEX = 'byOwner'

const DESKTOP_V3_CACHE_DB_OPEN_TIMEOUT_MS = 250

interface DesktopV3CacheDBSchema extends DBSchema {
  owners: {
    key: string
    value: PersistedDesktopV3OwnerV1
  }
  messageTails: {
    key: string
    value: PersistedDesktopV3MessageTailV1
    indexes: {
      byOwner: string
    }
  }
}

type DesktopV3CacheDB = IDBPDatabase<DesktopV3CacheDBSchema>

let desktopV3CacheDBPromise: Promise<DesktopV3CacheDB> | undefined
let activeDesktopV3CacheDB: DesktopV3CacheDB | undefined

export async function readDesktopV3Owner(ownerKey: string): Promise<PersistedDesktopV3OwnerV1 | undefined> {
  const normalizedOwnerKey = normalizeOwnerKey(ownerKey)
  if (normalizedOwnerKey === undefined) return undefined

  return runDesktopV3DBOperation(undefined, async (db) => {
    const raw = await db.get(DESKTOP_V3_CACHE_OWNERS_STORE, normalizedOwnerKey)
    if (raw === undefined) return undefined

    const result = validatePersistedDesktopV3OwnerV1(raw, normalizedOwnerKey)
    if (result.ok) return result.value

    await deleteDesktopV3OwnerCache(normalizedOwnerKey)
    return undefined
  })
}

export async function readDesktopV3MessageTail(
  ownerKey: string,
  sessionId: string,
): Promise<PersistedDesktopV3MessageTailV1 | undefined> {
  const key = normalizeMessageTailKey(ownerKey, sessionId)
  if (key === undefined) return undefined

  return runDesktopV3DBOperation(undefined, async (db) => {
    const raw = await db.get(DESKTOP_V3_CACHE_MESSAGE_TAILS_STORE, key.key)
    if (raw === undefined) return undefined

    const result = validatePersistedDesktopV3MessageTailV1(raw, key.ownerKey, key.sessionId)
    if (result.ok) return result.value

    await deleteDesktopV3MessageTailByKey(key.key)
    return undefined
  })
}

export async function readAllDesktopV3MessageTails(ownerKey: string): Promise<PersistedDesktopV3MessageTailV1[]> {
  const normalizedOwnerKey = normalizeOwnerKey(ownerKey)
  if (normalizedOwnerKey === undefined) return []

  return runDesktopV3DBOperation([], async (db) => {
    const tx = db.transaction(DESKTOP_V3_CACHE_MESSAGE_TAILS_STORE, 'readonly')
    const index = tx.store.index(DESKTOP_V3_CACHE_BY_OWNER_INDEX)
    const rawTails: Array<{ key: string; raw: unknown; expectedSessionId?: string }> = []
    for (let cursor = await index.openCursor(normalizedOwnerKey); cursor; cursor = await cursor.continue()) {
      if (typeof cursor.primaryKey === 'string') rawTails.push({ key: cursor.primaryKey, raw: cursor.value })
    }
    await tx.done
    const { tails, invalidKeys } = validateMessageTailRecords(rawTails, normalizedOwnerKey)
    if (invalidKeys.length > 0) await deleteDesktopV3MessageTailsByKeys(invalidKeys)
    return tails.sort((left, right) => left.sessionId.localeCompare(right.sessionId))
  })
}

export async function readDesktopV3MessageTails(
  ownerKey: string,
  sessionIds: string[],
): Promise<PersistedDesktopV3MessageTailV1[]> {
  const normalizedOwnerKey = normalizeOwnerKey(ownerKey)
  if (normalizedOwnerKey === undefined) return []
  const keysBySessionId = new Map<string, string>()
  for (const rawSessionId of sessionIds) {
    const key = normalizeMessageTailKey(normalizedOwnerKey, rawSessionId)
    if (key) keysBySessionId.set(key.sessionId, key.key)
  }
  if (keysBySessionId.size === 0) return []

  return runDesktopV3DBOperation([], async (db) => {
    const tx = db.transaction(DESKTOP_V3_CACHE_MESSAGE_TAILS_STORE, 'readonly')
    const rawTails = await Promise.all([...keysBySessionId.entries()].map(async ([expectedSessionId, key]) => ({ key, raw: await tx.store.get(key), expectedSessionId })))
    await tx.done
    const { tails, invalidKeys } = validateMessageTailRecords(rawTails, normalizedOwnerKey, new Set(keysBySessionId.keys()))
    if (invalidKeys.length > 0) await deleteDesktopV3MessageTailsByKeys(invalidKeys)
    const order = new Map([...keysBySessionId.keys()].map((sessionId, index) => [sessionId, index]))
    return tails.sort((left, right) => (order.get(left.sessionId) ?? 0) - (order.get(right.sessionId) ?? 0))
  })
}

export async function writeDesktopV3OwnerAndTails(
  owner: PersistedDesktopV3OwnerV1,
  tails: PersistedDesktopV3MessageTailV1[] = [],
): Promise<boolean> {
  return runDesktopV3DBOperation(false, async (db) => {
    const ownerResult = validatePersistedDesktopV3OwnerV1(owner, owner.ownerKey)
    if (!ownerResult.ok) return false

    const ownerKey = ownerResult.value.ownerKey
    const validatedTails: PersistedDesktopV3MessageTailV1[] = []
    for (const tail of tails) {
      const tailResult = validatePersistedDesktopV3MessageTailV1(tail, ownerKey)
      if (!tailResult.ok) return false
      validatedTails.push(tailResult.value)
    }

    const tx = db.transaction([DESKTOP_V3_CACHE_OWNERS_STORE, DESKTOP_V3_CACHE_MESSAGE_TAILS_STORE], 'readwrite')
    tx.objectStore(DESKTOP_V3_CACHE_OWNERS_STORE).put(ownerResult.value)
    const tailStore = tx.objectStore(DESKTOP_V3_CACHE_MESSAGE_TAILS_STORE)
    for (const tail of validatedTails) {
      tailStore.put(tail)
    }
    await tx.done
    return true
  })
}

export async function deleteDesktopV3OwnerCache(ownerKey: string): Promise<boolean> {
  const normalizedOwnerKey = normalizeOwnerKey(ownerKey)
  if (normalizedOwnerKey === undefined) return false

  return runDesktopV3DBOperation(false, async (db) => {
    const tailKeys = await db.getAllKeysFromIndex(
      DESKTOP_V3_CACHE_MESSAGE_TAILS_STORE,
      DESKTOP_V3_CACHE_BY_OWNER_INDEX,
      normalizedOwnerKey,
    )
    const tx = db.transaction([DESKTOP_V3_CACHE_OWNERS_STORE, DESKTOP_V3_CACHE_MESSAGE_TAILS_STORE], 'readwrite')
    tx.objectStore(DESKTOP_V3_CACHE_OWNERS_STORE).delete(normalizedOwnerKey)
    const tailStore = tx.objectStore(DESKTOP_V3_CACHE_MESSAGE_TAILS_STORE)
    for (const tailKey of tailKeys) {
      tailStore.delete(tailKey)
    }
    await tx.done
    return true
  })
}

export async function resetDesktopV3CacheDBForTests(): Promise<boolean> {
  await closeDesktopV3CacheDB()
  if (!hasIndexedDB()) return false

  let rejectBlocked: (error: Error) => void = () => undefined
  const blockedPromise = new Promise<never>((_resolve, reject) => {
    rejectBlocked = reject
  })
  const deletePromise = deleteDB(DESKTOP_V3_CACHE_DB_NAME, {
    blocked() {
      rejectBlocked(new Error(`${DESKTOP_V3_CACHE_DB_NAME} delete blocked by an open IndexedDB connection`))
    },
  })
  deletePromise.catch(() => undefined)

  try {
    await Promise.race([deletePromise, blockedPromise])
    return true
  } catch {
    return false
  }
}

function validateMessageTailRecords(
  rawTails: Array<{ key: string; raw: unknown; expectedSessionId?: string }>,
  ownerKey: string,
  expectedSessionIds?: Set<string>,
): { tails: PersistedDesktopV3MessageTailV1[]; invalidKeys: string[] } {
  const tails: PersistedDesktopV3MessageTailV1[] = []
  const invalidKeys: string[] = []
  for (const { key, raw, expectedSessionId } of rawTails) {
    if (raw === undefined) continue
    const rawSessionId = typeof raw === 'object' && raw !== null && typeof (raw as { sessionId?: unknown }).sessionId === 'string'
      ? (raw as { sessionId: string }).sessionId.trim()
      : undefined
    const result = validatePersistedDesktopV3MessageTailV1(raw, ownerKey, expectedSessionId ?? rawSessionId)
    if (result.ok && (expectedSessionIds === undefined || expectedSessionIds.has(result.value.sessionId))) {
      tails.push(result.value)
    } else {
      invalidKeys.push(key)
    }
  }
  return { tails, invalidKeys }
}

async function deleteDesktopV3MessageTailByKey(key: string): Promise<void> {
  await runDesktopV3DBOperation(undefined, async (db) => {
    await db.delete(DESKTOP_V3_CACHE_MESSAGE_TAILS_STORE, key)
    return undefined
  })
}

async function deleteDesktopV3MessageTailsByKeys(keys: string[]): Promise<void> {
  if (keys.length === 0) return
  await runDesktopV3DBOperation(undefined, async (db) => {
    const tx = db.transaction(DESKTOP_V3_CACHE_MESSAGE_TAILS_STORE, 'readwrite')
    for (const key of keys) {
      tx.store.delete(key)
    }
    await tx.done
    return undefined
  })
}

async function runDesktopV3DBOperation<T>(fallback: T, operation: (db: DesktopV3CacheDB) => Promise<T>): Promise<T> {
  try {
    const db = await openDesktopV3CacheDB()
    return await operation(db)
  } catch {
    return fallback
  }
}

async function openDesktopV3CacheDB(): Promise<DesktopV3CacheDB> {
  if (!hasIndexedDB()) {
    throw new Error('IndexedDB is unavailable for Desktop V3 cache')
  }
  if (desktopV3CacheDBPromise !== undefined) return desktopV3CacheDBPromise

  let openCancelled = false
  let openTimeout: ReturnType<typeof setTimeout> | undefined
  let rejectOpen: (error: Error) => void = () => undefined
  const controlledFailurePromise = new Promise<never>((_resolve, reject) => {
    rejectOpen = (error) => {
      openCancelled = true
      reject(error)
    }
    openTimeout = setTimeout(() => {
      rejectOpen(new Error(`${DESKTOP_V3_CACHE_DB_NAME} open timed out`))
    }, DESKTOP_V3_CACHE_DB_OPEN_TIMEOUT_MS)
  })
  const rawOpenPromise = openDB<DesktopV3CacheDBSchema>(DESKTOP_V3_CACHE_DB_NAME, DESKTOP_V3_CACHE_DB_VERSION, {
    upgrade(db, oldVersion) {
      if (oldVersion < 1) {
        if (!db.objectStoreNames.contains(DESKTOP_V3_CACHE_OWNERS_STORE)) {
          db.createObjectStore(DESKTOP_V3_CACHE_OWNERS_STORE, { keyPath: 'ownerKey' })
        }
        if (!db.objectStoreNames.contains(DESKTOP_V3_CACHE_MESSAGE_TAILS_STORE)) {
          const messageTails = db.createObjectStore(DESKTOP_V3_CACHE_MESSAGE_TAILS_STORE, { keyPath: 'key' })
          messageTails.createIndex(DESKTOP_V3_CACHE_BY_OWNER_INDEX, 'ownerKey')
        }
      }
    },
    blocked() {
      rejectOpen(new Error(`${DESKTOP_V3_CACHE_DB_NAME} open blocked by an older IndexedDB connection`))
    },
    blocking() {
      activeDesktopV3CacheDB?.close()
      activeDesktopV3CacheDB = undefined
      desktopV3CacheDBPromise = undefined
    },
    terminated() {
      activeDesktopV3CacheDB = undefined
      desktopV3CacheDBPromise = undefined
    },
  })

  rawOpenPromise.then((db) => {
    if (openCancelled) {
      db.close()
    }
  }, () => undefined)

  desktopV3CacheDBPromise = Promise.race([rawOpenPromise, controlledFailurePromise])
    .then((db) => {
      if (openTimeout !== undefined) clearTimeout(openTimeout)
      if (openCancelled) {
        db.close()
        throw new Error(`${DESKTOP_V3_CACHE_DB_NAME} open cancelled`)
      }
      activeDesktopV3CacheDB = db
      return db
    })
    .catch((error) => {
      if (openTimeout !== undefined) clearTimeout(openTimeout)
      activeDesktopV3CacheDB = undefined
      desktopV3CacheDBPromise = undefined
      throw error
    })

  return desktopV3CacheDBPromise
}

async function closeDesktopV3CacheDB(): Promise<void> {
  const dbPromise = desktopV3CacheDBPromise
  desktopV3CacheDBPromise = undefined
  const activeDB = activeDesktopV3CacheDB
  activeDesktopV3CacheDB = undefined
  activeDB?.close()
  if (dbPromise === undefined) return
  try {
    const db = await dbPromise
    db.close()
  } catch {
    // Ignore cached open failures while resetting test storage.
  }
}

function normalizeOwnerKey(ownerKey: string): string | undefined {
  try {
    return parseDesktopV3CacheOwnerKey(ownerKey).key
  } catch {
    return undefined
  }
}

function normalizeMessageTailKey(ownerKey: string, sessionId: string): { ownerKey: string; sessionId: string; key: string } | undefined {
  try {
    const normalizedOwnerKey = parseDesktopV3CacheOwnerKey(ownerKey).key
    const key = createPersistedDesktopV3MessageTailKey(normalizedOwnerKey, sessionId)
    return { ownerKey: normalizedOwnerKey, sessionId: sessionId.trim(), key }
  } catch {
    return undefined
  }
}

function hasIndexedDB(): boolean {
  return typeof globalThis.indexedDB !== 'undefined'
}
