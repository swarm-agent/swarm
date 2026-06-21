import { ensureDesktopSession, getDesktopSessionIdentitySnapshot } from '../../../app/api'

import { saveDesktopV3CacheActiveOwnerKey } from './desktop-v3-cache-active-owner'
import {
  desktopV3CachePersistenceCoordinator,
  persistDesktopV3OwnerAndTails,
} from './desktop-v3-cache-persistence-coordinator'
import { createDesktopV3CacheOwnerFromIdentity, type DesktopV3CacheOwner } from './desktop-v3-cache-owner'
import {
  buildPersistedDesktopV3MessageTailV1,
  DESKTOP_V3_CACHE_SCHEMA_VERSION,
  type PersistedDesktopV3MessageTailV1,
  type PersistedDesktopV3OwnerV1,
} from './desktop-v3-cache-persisted-types'
import { getDesktopV3CacheSnapshot, subscribeDesktopV3Cache, type DesktopV3CacheMutation } from './desktop-v3-cache-store'
import type { DesktopV3CacheAction, DesktopV3CacheState, LiveRunOverlay } from './desktop-v3-cache-types'

const DEFAULT_DEBOUNCE_MS = 75

interface DesktopV3PersistenceScheduler {
  setTimeout(callback: () => void, delayMs: number): unknown
  clearTimeout(timer: unknown): void
}

export interface DesktopV3PersistenceControllerDeps {
  subscribe?: (listener: (mutation?: DesktopV3CacheMutation) => void) => () => void
  getSnapshot?: () => DesktopV3CacheState
  resolveOwner?: () => Promise<DesktopV3CacheOwner | undefined> | DesktopV3CacheOwner | undefined
  writeOwnerAndTails?: (owner: PersistedDesktopV3OwnerV1, tails: PersistedDesktopV3MessageTailV1[]) => Promise<boolean> | boolean
  saveActiveOwnerKey?: (ownerKey: string) => boolean
  now?: () => number
  scheduler?: DesktopV3PersistenceScheduler
  debounceMs?: number
}

export interface DesktopV3PersistenceController {
  start(): () => void
  stop(): void
  flushNow(): Promise<void>
}

let sharedController: DesktopV3PersistenceController | undefined
let sharedStop: (() => void) | undefined

export function startDesktopV3PersistenceController(deps: DesktopV3PersistenceControllerDeps = {}): () => void {
  if (sharedStop) return sharedStop

  const controller = createDesktopV3PersistenceController(deps)
  const stopController = controller.start()
  let released = false

  const release = () => {
    if (released) return
    released = true

    stopController()

    if (sharedController === controller) {
      sharedController = undefined
      sharedStop = undefined
    }
  }

  sharedController = controller
  sharedStop = release
  return release
}

export function stopDesktopV3PersistenceControllerForTests(): void {
  sharedStop?.()
  sharedController = undefined
  sharedStop = undefined
}

export function createDesktopV3PersistenceController(
  deps: DesktopV3PersistenceControllerDeps = {},
): DesktopV3PersistenceController {
  const subscribe = deps.subscribe ?? subscribeDesktopV3Cache
  const getSnapshot = deps.getSnapshot ?? getDesktopV3CacheSnapshot
  const resolveOwner = deps.resolveOwner ?? resolveCurrentDesktopV3CacheOwner
  const writeOwnerAndTails = deps.writeOwnerAndTails ?? persistDesktopV3OwnerAndTails
  const saveActiveOwnerKey = deps.saveActiveOwnerKey ?? saveDesktopV3CacheActiveOwnerKey
  const now = deps.now ?? Date.now
  const scheduler = deps.scheduler ?? globalDesktopV3PersistenceScheduler
  const debounceMs = deps.debounceMs ?? DEFAULT_DEBOUNCE_MS

  let unsubscribe: (() => void) | undefined
  let stopped = false
  let stopping = false
  let lifecycleVersion = 0
  let pendingTimer: unknown
  let revision = 0
  let ownerDirtyVersion = 0
  let dirtyOwnerKey: string | undefined
  let ownerResolutionInFlight: Promise<void> | undefined
  const tailDirtyVersions = new Map<string, number>()
  const lastWrittenTailSignatures = new Map<string, string>()
  let flushInFlight: Promise<void> | undefined
  let flushAgain = false

  const markOwnerDirty = (ownerKey: string) => {
    resetDirtyStateForOwnerIfNeeded(ownerKey)
    ownerDirtyVersion = ++revision
  }

  const markTailDirty = (ownerKey: string, sessionId: string | undefined) => {
    const normalizedSessionId = sessionId?.trim()
    if (!normalizedSessionId) return
    resetDirtyStateForOwnerIfNeeded(ownerKey)
    tailDirtyVersions.set(normalizedSessionId, ++revision)
  }

  const resetDirtyStateForOwnerIfNeeded = (ownerKey: string) => {
    if (dirtyOwnerKey === ownerKey) return
    dirtyOwnerKey = ownerKey
    ownerDirtyVersion = 0
    tailDirtyVersions.clear()
  }

  const clearPendingTimer = () => {
    if (pendingTimer !== undefined) {
      scheduler.clearTimeout(pendingTimer)
      pendingTimer = undefined
    }
  }

  const scheduleFlush = (immediate: boolean) => {
    if (stopped) return
    if (stopping) {
      flushAgain = true
      return
    }
    if (immediate) {
      clearPendingTimer()
      void flushNow().catch(() => undefined)
      return
    }
    if (pendingTimer !== undefined) return
    pendingTimer = scheduler.setTimeout(() => {
      pendingTimer = undefined
      void flushNow().catch(() => undefined)
    }, debounceMs)
  }

  const handleMutation = (mutation?: DesktopV3CacheMutation) => {
    if (!mutation) return
    const decision = classifyDesktopV3PersistenceAction(mutation.action)
    if (!decision.persistOwner && decision.tailSessionIds.length === 0) return

    ownerResolutionInFlight = Promise.resolve(resolveOwner())
      .then((owner) => {
        if (!owner || stopped) return
        if (decision.persistOwner) markOwnerDirty(owner.key)
        for (const sessionId of decision.tailSessionIds) markTailDirty(owner.key, sessionId)
        scheduleFlush(decision.immediate)
      })
      .catch(() => undefined)
  }

  const buildDirtyTailsFromCurrentState = (
    state: DesktopV3CacheState,
    ownerKey: string,
    pendingVersions: Map<string, number>,
    persistedAt: number,
  ): PersistedDesktopV3MessageTailV1[] => {
    const tails: PersistedDesktopV3MessageTailV1[] = []
    for (const sessionId of pendingVersions.keys()) {
      const tail = buildPersistedDesktopV3MessageTailV1FromState(state, ownerKey, sessionId, persistedAt)
      if (!tail) continue

      const signatureKey = persistedTailSignatureKey(ownerKey, sessionId)
      const signature = persistedTailSignature(tail)
      if (lastWrittenTailSignatures.get(signatureKey) === signature) continue
      tails.push(tail)
    }
    return tails
  }

  const flushOnce = async (): Promise<void> => {
    const pendingTailVersions = new Map(tailDirtyVersions)
    const pendingOwnerVersion = ownerDirtyVersion
    if (pendingOwnerVersion === 0 && pendingTailVersions.size === 0) return
    const expectedOwnerKey = dirtyOwnerKey
    if (!expectedOwnerKey) return

    await desktopV3CachePersistenceCoordinator.enqueue(async () => {
      let owner: DesktopV3CacheOwner | undefined
      try {
        owner = await resolveOwner()
      } catch {
        return
      }
      if (!owner) return
      if (owner.key !== expectedOwnerKey) {
        ownerDirtyVersion = 0
        tailDirtyVersions.clear()
        dirtyOwnerKey = undefined
        return
      }

      const state = getSnapshot()
      const persistedAt = now()
      const ownerRecord = buildPersistedDesktopV3OwnerV1FromState(state, owner, persistedAt)
      if (!ownerRecord) return

      const tails = buildDirtyTailsFromCurrentState(
        state,
        owner.key,
        pendingTailVersions,
        persistedAt,
      )

      const wrote = await writeOwnerAndTails(ownerRecord, tails)
      if (!wrote) {
        throw new Error('Desktop V3 IndexedDB transaction failed')
      }

      saveActiveOwnerKey(owner.key)
      if (ownerDirtyVersion === pendingOwnerVersion) ownerDirtyVersion = 0
      for (const [sessionId, dirtyVersion] of pendingTailVersions.entries()) {
        if (tailDirtyVersions.get(sessionId) === dirtyVersion) {
          tailDirtyVersions.delete(sessionId)
        }
      }
      for (const tail of tails) {
        lastWrittenTailSignatures.set(persistedTailSignatureKey(owner.key, tail.sessionId), persistedTailSignature(tail))
      }
      for (const sessionId of pendingTailVersions.keys()) {
        if (!state.messagesBySession[sessionId] && tailDirtyVersions.get(sessionId) === pendingTailVersions.get(sessionId)) {
          tailDirtyVersions.delete(sessionId)
        }
      }
      if (ownerDirtyVersion === 0 && tailDirtyVersions.size === 0 && dirtyOwnerKey === owner.key) {
        dirtyOwnerKey = undefined
      }
    })
  }

  const flushNow = async (): Promise<void> => {
    if (stopped) return
    clearPendingTimer()
    await ownerResolutionInFlight
    if (flushInFlight) {
      flushAgain = true
      return flushInFlight
    }

    flushInFlight = (async () => {
      do {
        flushAgain = false
        await flushOnce()
      } while (flushAgain && !stopped)
    })().finally(() => {
      flushInFlight = undefined
    })

    return flushInFlight
  }

  const stop = () => {
    if ((stopped || stopping) && !unsubscribe) return
    const stopVersion = ++lifecycleVersion
    stopping = true
    clearPendingTimer()
    unsubscribe?.()
    unsubscribe = undefined
    void flushNow()
      .catch(() => undefined)
      .finally(() => {
        if (lifecycleVersion !== stopVersion) return
        stopped = true
        stopping = false
      })
  }

  return {
    start() {
      if (unsubscribe) return stop
      lifecycleVersion += 1
      stopped = false
      stopping = false
      unsubscribe = subscribe(handleMutation)
      return stop
    },
    stop,
    flushNow,
  }
}

export function buildPersistedDesktopV3OwnerV1FromState(
  state: DesktopV3CacheState,
  owner: DesktopV3CacheOwner,
  persistedAt: number,
): PersistedDesktopV3OwnerV1 | undefined {
  const sidebarScopeId = selectPersistedSidebarScopeId(state)
  if (!sidebarScopeId) return undefined
  const sidebarScope = state.syncScopesById[sidebarScopeId]
  if (!sidebarScope) return undefined

  const sidebarSessionsById: PersistedDesktopV3OwnerV1['sidebarSessionsById'] = {}
  for (const [sessionId, record] of Object.entries(state.sessionsById)) {
    if (record.kind !== 'full') continue
    sidebarSessionsById[sessionId] = {
      session: record.session,
      projection: state.projectionsBySession[sessionId],
      tombstone: state.tombstonesBySession[sessionId],
      runIntents: sortedRunIntents(state, sessionId),
    }
    if (sidebarSessionsById[sessionId].runIntents?.length === 0) {
      delete sidebarSessionsById[sessionId].runIntents
    }
  }

  const sessionOrderByScope: DesktopV3CacheState['sessionOrderByScope'] = {}
  for (const [scopeId, order] of Object.entries(state.sessionOrderByScope)) {
    if (!state.syncScopesById[scopeId]) continue
    const durableOrder = order.filter((sessionId) => sidebarSessionsById[sessionId] !== undefined)
    sessionOrderByScope[scopeId] = durableOrder
  }
  if (!sessionOrderByScope[sidebarScopeId]) {
    sessionOrderByScope[sidebarScopeId] = Object.keys(sidebarSessionsById)
  }

  const selectedSessionId = state.selectedSessionId && sidebarSessionsById[state.selectedSessionId]
    ? state.selectedSessionId
    : undefined

  const liveRunsBySession =
    buildPersistedDesktopV3LiveRunsBySessionV1FromState(
      state,
      sidebarSessionsById,
    )

  return {
    schemaVersion: DESKTOP_V3_CACHE_SCHEMA_VERSION,
    ownerKey: owner.key,
    owner,
    persistedAt,
    selectedSessionId,
    sidebarScopeId,
    syncScopesById: cloneSyncScopes(state.syncScopesById),
    sessionOrderByScope,
    sidebarSessionsById,
    realtimeEndpointCursor:
      state.realtime.endpointCursor?.trim() || undefined,
    liveRunsBySession:
      Object.keys(liveRunsBySession).length > 0
        ? liveRunsBySession
        : undefined,
  }
}

export function buildPersistedDesktopV3LiveRunsBySessionV1FromState(
  state: DesktopV3CacheState,
  sidebarSessionsById: PersistedDesktopV3OwnerV1['sidebarSessionsById'],
): NonNullable<PersistedDesktopV3OwnerV1['liveRunsBySession']> {
  const output: NonNullable<
    PersistedDesktopV3OwnerV1['liveRunsBySession']
  > = {}

  for (const [sessionId, runsById] of Object.entries(
    state.liveRunsBySession,
  )) {
    if (!sidebarSessionsById[sessionId]) continue
    if (state.tombstonesBySession[sessionId]) continue

    for (const [runId, run] of Object.entries(runsById)) {
      if (run.sessionId !== sessionId) continue
      if (run.runId !== runId) continue
      if (!isPersistableDesktopV3LiveRun(run)) continue

      output[sessionId] ??= {}
      output[sessionId][runId] = structuredClone(run)
    }
  }

  return output
}

export function isPersistableDesktopV3LiveRun(
  run: LiveRunOverlay,
): boolean {
  const active =
    run.status === 'pending_executor'
    || run.status === 'running'
    || run.status === 'dispatch_blocked'

  const hasVisibleState =
    Boolean(run.assistantDraft?.content)
    || Boolean(run.assistantSegments?.some((segment) => segment.content))
    || Object.keys(run.toolCallsByCallId).length > 0
    || Boolean(run.reasoning)
    || Object.keys(run.reasoningByKey ?? {}).length > 0

  return active || hasVisibleState
}

export function buildPersistedDesktopV3MessageTailV1FromState(
  state: DesktopV3CacheState,
  ownerKey: string,
  sessionId: string,
  persistedAt: number,
): PersistedDesktopV3MessageTailV1 | undefined {
  const messages = state.messagesBySession[sessionId]
  if (!messages) return undefined
  return buildPersistedDesktopV3MessageTailV1({
    ownerKey,
    sessionId,
    persistedAt,
    messages: messages.items,
    sourceMessageCount: messages.sourceMessageCount,
    sourceLastMessageAt: messages.sourceLastMessageAt,
    sourceProjectionHighWatermarkSeq: messages.sourceProjectionHighWatermarkSeq,
    hydratedAt: messages.hydratedAt,
  })
}

export function classifyDesktopV3PersistenceAction(
  action: DesktopV3CacheAction,
): { persistOwner: boolean; tailSessionIds: string[]; immediate: boolean } {
  switch (action.type) {
    case 'snapshot.apply':
      return {
        persistOwner: true,
        tailSessionIds: Object.keys(action.snapshot.messages_by_session ?? {}),
        immediate: true,
      }
    case 'hydrate.apply':
      return {
        persistOwner: true,
        tailSessionIds: Object.keys(action.snapshot.messages_by_session ?? {}),
        immediate: true,
      }
    case 'session.select':
      return {
        persistOwner: true,
        tailSessionIds: action.sessionId ? [action.sessionId] : [],
        immediate: true,
      }
    case 'mutation.messageResult':
      if (action.raw.ok === false) return noPersistenceDecision()
      return {
        persistOwner: Boolean(action.raw.run_intent),
        tailSessionIds: action.raw.message ? [action.raw.session_id || action.raw.message.session_id] : [],
        immediate: false,
      }
    case 'syncStream.applyBatch':
      return {
        persistOwner: action.events.length > 0,
        tailSessionIds: action.events.flatMap((event) => event.payload.message ? [event.sessionId] : []),
        immediate: false,
      }
    case 'desktopV3Cache.restore':
    case 'desktopV3Cache.restoreMessageTails':
    case 'desktopV3Cache.applyHydrationPlan':
    case 'desktopSidebarBootstrap.update':
    case 'desktopInitialHydrate.update':
    case 'pendingUser.upsert':
    case 'reconnect.applySnapshot':
    case 'realtime.storeResume':
    case 'realtime.applyEvent':
    case 'liveRun.rebuildFromEvents':
    case 'realtime.statusChanged':
    case 'mutation.sessionCreateResult':
    case 'realtime.worksetSessionDiscovered':
    case 'realtime.worksetSessionRemoved':
    case 'realtime.cursorError':
    case 'realtime.control':
    case 'realtime.unknownFrame':
      return noPersistenceDecision()
    default: {
      const _exhaustive: never = action
      return _exhaustive
    }
  }
}

async function resolveCurrentDesktopV3CacheOwner(): Promise<DesktopV3CacheOwner | undefined> {
  try {
    const identity = getDesktopSessionIdentitySnapshot() ?? await ensureDesktopSession()
    return createDesktopV3CacheOwnerFromIdentity(identity)
  } catch {
    return undefined
  }
}

function selectPersistedSidebarScopeId(state: DesktopV3CacheState): string | undefined {
  const explicitScopeId = state.desktopSidebarBootstrap.scopeId
  if (explicitScopeId && state.syncScopesById[explicitScopeId]) return explicitScopeId
  for (const scopeId of Object.keys(state.syncScopesById).sort()) {
    if (state.sessionOrderByScope[scopeId]) return scopeId
  }
  return Object.keys(state.syncScopesById).sort()[0]
}

function cloneSyncScopes(syncScopesById: DesktopV3CacheState['syncScopesById']): DesktopV3CacheState['syncScopesById'] {
  const output: DesktopV3CacheState['syncScopesById'] = {}
  for (const [scopeId, scope] of Object.entries(syncScopesById)) {
    output[scopeId] = {
      ...scope,
      selector: cloneRecord(scope.selector),
    }
  }
  return output
}

function cloneRecord<T>(value: T): T {
  if (value === undefined || value === null) return value
  return JSON.parse(JSON.stringify(value)) as T
}

function sortedRunIntents(state: DesktopV3CacheState, sessionId: string) {
  return Object.values(state.runIntentsBySession[sessionId] ?? {})
    .sort((left, right) => left.run_id.localeCompare(right.run_id))
}

function persistedTailSignatureKey(ownerKey: string, sessionId: string): string {
  return `${ownerKey}\u0000${sessionId}`
}

function persistedTailSignature(tail: PersistedDesktopV3MessageTailV1): string {
  return JSON.stringify({
    messages: tail.messages,
    sourceMessageCount: tail.sourceMessageCount,
    sourceLastMessageAt: tail.sourceLastMessageAt,
    sourceProjectionHighWatermarkSeq: tail.sourceProjectionHighWatermarkSeq,
    hydratedAt: tail.hydratedAt,
  })
}

function noPersistenceDecision(): { persistOwner: boolean; tailSessionIds: string[]; immediate: boolean } {
  return { persistOwner: false, tailSessionIds: [], immediate: false }
}

const globalDesktopV3PersistenceScheduler: DesktopV3PersistenceScheduler = {
  setTimeout(callback, delayMs) {
    return globalThis.setTimeout(callback, delayMs)
  },
  clearTimeout(timer) {
    globalThis.clearTimeout(timer as ReturnType<typeof setTimeout>)
  },
}
