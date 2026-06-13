import {
  type DesktopDaemonSnapshot,
  type DesktopState,
} from '../state/desktop-state'
import {
  createV3PersistedRestoreEnvelope,
  type V3EnvelopeCursor,
} from './v3-envelope'
import {
  applyV3RuntimeEnvelope,
  getV3RuntimeSnapshot,
  subscribeV3Runtime,
  type V3RuntimeApplyOutcome,
  type V3RuntimeController,
  type V3RuntimeCursorState,
  type V3RuntimeState,
} from './v3-store'

const V3_RUNTIME_DB_NAME = 'swarm-desktop-v3-runtime'
const V3_RUNTIME_DB_VERSION = 1
const V3_RUNTIME_STORE = 'runtime-snapshots'
const DEFAULT_RUNTIME_PERSISTENCE_KEY = 'default'

export interface V3RuntimePersistedSnapshot {
  id: string
  desktop: DesktopDaemonSnapshot
  cursorsByScope: Record<string, V3EnvelopeCursor>
  mutationSeq: number
  savedAt: number
}

export interface V3RuntimePersistenceController {
  applyEnvelope: V3RuntimeController['applyEnvelope']
  getSnapshot: V3RuntimeController['getSnapshot']
  subscribe: V3RuntimeController['subscribe']
}

let runtimeDBPromise: Promise<IDBDatabase> | null = null
let defaultPersistenceUnsubscribe: (() => void) | null = null
const memoryRuntimeSnapshots = new Map<string, V3RuntimePersistedSnapshot>()

const defaultPersistenceController: V3RuntimePersistenceController = {
  applyEnvelope: applyV3RuntimeEnvelope,
  getSnapshot: getV3RuntimeSnapshot,
  subscribe: subscribeV3Runtime,
}

export function installV3RuntimePersistence(controller: V3RuntimePersistenceController = defaultPersistenceController): () => void {
  if (controller === defaultPersistenceController && defaultPersistenceUnsubscribe) {
    return defaultPersistenceUnsubscribe
  }

  let lastPersistedMutationSeq = controller.getSnapshot().mutationSeq
  const unsubscribe = controller.subscribe(() => {
    const snapshot = controller.getSnapshot()
    const lastApply = snapshot.lastApply
    if (!lastApply) {
      return
    }
    const successfulRuntimeAdvance = (lastApply.applied || lastApply.shouldAdvanceCursor) && !lastApply.rejected && !lastApply.stale
    if (!successfulRuntimeAdvance || lastApply.duplicate || snapshot.mutationSeq <= lastPersistedMutationSeq) {
      return
    }
    lastPersistedMutationSeq = snapshot.mutationSeq
    void writeV3RuntimePersistedSnapshot(snapshot).catch((error) => {
      console.error('[v3-runtime] persisted snapshot write failed', error)
    })
  })

  if (controller === defaultPersistenceController) {
    defaultPersistenceUnsubscribe = () => {
      unsubscribe()
      defaultPersistenceUnsubscribe = null
    }
    return defaultPersistenceUnsubscribe
  }
  return unsubscribe
}

export async function restoreV3RuntimeFromPersistence(controller: V3RuntimePersistenceController = defaultPersistenceController): Promise<V3RuntimeApplyOutcome | null> {
  if (controller.getSnapshot().mutationSeq > 0) {
    return null
  }
  const persisted = await readV3RuntimePersistedSnapshot()
  if (!persisted) {
    return null
  }
  return controller.applyEnvelope(createV3PersistedRestoreEnvelope(persisted.desktop, {
    id: `persisted.restore:${persisted.id}:${persisted.mutationSeq}`,
    mode: 'replace',
    receivedAt: Date.now(),
    highWatermarkSeq: maxPersistedHighWatermark(persisted.cursorsByScope),
    source: { name: 'v3-runtime-persistence' },
    cursorsByScope: persisted.cursorsByScope,
  }))
}

export async function readV3RuntimePersistedSnapshot(id = DEFAULT_RUNTIME_PERSISTENCE_KEY): Promise<V3RuntimePersistedSnapshot | null> {
  const db = await openV3RuntimeDB()
  if (!db) {
    return memoryRuntimeSnapshots.get(id) ?? null
  }
  return new Promise((resolve, reject) => {
    const transaction = db.transaction(V3_RUNTIME_STORE, 'readonly')
    const store = transaction.objectStore(V3_RUNTIME_STORE)
    const request = store.get(id)
    request.onsuccess = () => resolve((request.result as V3RuntimePersistedSnapshot | undefined) ?? null)
    request.onerror = () => reject(request.error ?? new Error('failed to read V3 runtime persisted snapshot'))
  })
}

export async function writeV3RuntimePersistedSnapshot(snapshot: V3RuntimeState, id = DEFAULT_RUNTIME_PERSISTENCE_KEY): Promise<void> {
  const entry: V3RuntimePersistedSnapshot = {
    id,
    desktop: desktopSnapshotFromRuntimeState(snapshot.desktop),
    cursorsByScope: persistedCursors(snapshot.cursorsByScope),
    mutationSeq: snapshot.mutationSeq,
    savedAt: Date.now(),
  }
  const db = await openV3RuntimeDB()
  if (!db) {
    memoryRuntimeSnapshots.set(id, entry)
    return
  }
  await new Promise<void>((resolve, reject) => {
    const transaction = db.transaction(V3_RUNTIME_STORE, 'readwrite')
    const store = transaction.objectStore(V3_RUNTIME_STORE)
    const request = store.put(entry)
    request.onsuccess = () => resolve()
    request.onerror = () => reject(request.error ?? new Error('failed to write V3 runtime persisted snapshot'))
  })
}

export function clearV3RuntimePersistenceForTests(): void {
  memoryRuntimeSnapshots.clear()
  defaultPersistenceUnsubscribe?.()
}

function openV3RuntimeDB(): Promise<IDBDatabase | null> {
  if (typeof indexedDB === 'undefined') {
    return Promise.resolve(null)
  }
  if (runtimeDBPromise) {
    return runtimeDBPromise
  }
  const promise = new Promise<IDBDatabase>((resolve, reject) => {
    const request = indexedDB.open(V3_RUNTIME_DB_NAME, V3_RUNTIME_DB_VERSION)
    request.onupgradeneeded = () => {
      const db = request.result
      if (!db.objectStoreNames.contains(V3_RUNTIME_STORE)) {
        db.createObjectStore(V3_RUNTIME_STORE, { keyPath: 'id' })
      }
    }
    request.onsuccess = () => resolve(request.result)
    request.onerror = () => reject(request.error ?? new Error('failed to open V3 runtime persistence'))
    request.onblocked = () => reject(new Error('V3 runtime persistence open was blocked'))
  }).catch((error) => {
    runtimeDBPromise = null
    throw error
  })
  runtimeDBPromise = promise
  return promise
}

function desktopSnapshotFromRuntimeState(state: DesktopState): DesktopDaemonSnapshot {
  return {
    rev: state.rev,
    sessionsById: state.sessionsById,
    sessionOrder: state.sessionOrder,
    messagesBySessionId: state.messagesBySessionId,
    permissionsById: state.permissionsById,
    plansBySessionId: state.plansBySessionId,
    planRevisionsBySessionId: state.planRevisionsBySessionId,
    usageBySessionId: state.usageBySessionId,
    runIntentsBySessionId: state.runIntentsBySessionId,
    workspacesByPath: state.workspacesByPath,
    notificationsById: state.notificationsById,
    notificationSummary: state.notificationSummary,
    preferencesBySessionId: state.preferencesBySessionId,
    agentModelPolicyBySessionId: state.agentModelPolicyBySessionId,
    routeReadinessBySessionId: state.routeReadinessBySessionId,
  }
}

function persistedCursors(cursors: Record<string, V3RuntimeCursorState>): Record<string, V3EnvelopeCursor> {
  const persisted: Record<string, V3EnvelopeCursor> = {}
  for (const [scope, cursor] of Object.entries(cursors)) {
    persisted[scope] = {
      endpointCursor: cursor.endpointCursor,
      stream: cursor.stream,
      rev: cursor.rev,
      prevRev: cursor.prevRev,
      globalSeq: cursor.globalSeq,
      sourceSeq: cursor.sourceSeq,
      highWatermarkSeq: cursor.highWatermarkSeq,
      tsUnixMs: cursor.tsUnixMs,
    }
  }
  return persisted
}

function maxPersistedHighWatermark(cursors: Record<string, V3EnvelopeCursor>): number | null {
  let max = 0
  for (const cursor of Object.values(cursors)) {
    if (typeof cursor.highWatermarkSeq === 'number' && Number.isFinite(cursor.highWatermarkSeq)) {
      max = Math.max(max, Math.floor(cursor.highWatermarkSeq))
    }
    if (typeof cursor.globalSeq === 'number' && Number.isFinite(cursor.globalSeq)) {
      max = Math.max(max, Math.floor(cursor.globalSeq))
    }
  }
  return max > 0 ? max : null
}
