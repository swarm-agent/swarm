import { ensureDesktopSession, getDesktopSessionIdentitySnapshot } from '../../../app/api'
import { DesktopV3RealtimeTransport, type DesktopV3RealtimeTransportStatus } from '../session-v3/transport'
import type { SessionV3RealtimeWorksetSubscriptionRequestWire } from '../session-v3/types'
import { getDesktopV3SessionEventsPage, type DesktopV3SessionEventsPage } from '../session-v3/read-api'
import { openDesktopV3RealtimeTransportSocket } from './client'
import { bootstrapDesktopV3SidebarMetadataOnly } from '../state/desktop-v3-bootstrap-controller'
import { saveDesktopV3CacheActiveOwnerKey } from '../state/desktop-v3-cache-active-owner'
import { createDesktopV3CacheOwnerFromIdentity, type DesktopV3CacheOwner } from '../state/desktop-v3-cache-owner'
import { desktopV3CachePersistenceCoordinator, persistDesktopV3OwnerAndTails } from '../state/desktop-v3-cache-persistence-coordinator'
import { desktopV3CacheReducer } from '../state/desktop-v3-cache-reducer'
import type { PersistedDesktopV3MessageTailV1, PersistedDesktopV3OwnerV1 } from '../state/desktop-v3-cache-persisted-types'
import { buildPersistedDesktopV3MessageTailV1FromState, buildPersistedDesktopV3OwnerV1FromState } from '../state/desktop-v3-persistence-controller'
import { dispatchDesktopV3Cache, getDesktopV3CacheSnapshot, replaceDesktopV3CacheSnapshotAfterDurableCommit, subscribeDesktopV3Cache, type DesktopV3CacheMutation } from '../state/desktop-v3-cache-store'
import { decodeSessionEventPayload, hydrateResponseToAction, realtimeFrameToActions } from '../state/desktop-v3-cache-wire'
import {
  buildDesktopV3InitialHydrateInput,
  postDesktopV3Reconnect,
  postDesktopV3SyncHydrate,
  type DesktopV3ReconnectInput,
  type DesktopV3HydrateInput,
} from '../state/desktop-v3-sync-api'
import type {
  DesktopV3CacheAction,
  DesktopV3CacheState,
  RealtimeCache,
  RealtimeMessage,
  RealtimeWorksetSubscriptionRequest,
  SessionsReconnectResponse,
  SyncSnapshotResponse,
  V3SessionProjection,
  V3SessionRunIntent,
} from '../state/desktop-v3-cache-types'

const DESKTOP_V3_CLIENT_ID = `desktop:${crypto.randomUUID()}`
const ACTIVE_REPAIR_PAGE_SIZE = 500
const ACTIVE_INTENT_STATUSES = new Set(['pending_executor', 'running', 'dispatch_blocked'])

export interface DesktopV3RealtimeController {
  ensureSessionSubscription(sessionId: string): void
  ensureSessionHistory(sessionId: string): Promise<void>
  start(preferredSessionId?: string | null, bootstrapReady?: Promise<unknown>): Promise<void>
  stop(reason?: string): void
}

export interface DesktopV3RealtimeLease {
  ready: Promise<void>
  release: () => void
}

interface DesktopV3RealtimeControllerDeps {
  getSnapshot?: () => DesktopV3CacheState
  dispatch?: (action: DesktopV3CacheAction) => void
  subscribe?: (listener: (mutation?: DesktopV3CacheMutation) => void) => () => void
  reconnect?: (input: DesktopV3ReconnectInput) => Promise<SessionsReconnectResponse>
  hydrate?: (input: DesktopV3HydrateInput) => Promise<SyncSnapshotResponse>
  readEventsPage?: (input: { sessionId: string; afterSeq: number; limit?: number }) => Promise<DesktopV3SessionEventsPage>
  ensureSession?: () => Promise<unknown>
  bootstrap?: (input?: { preferredSessionId?: string | null; restorePersisted?: boolean }) => Promise<unknown>
  openSocket?: (input: { endpointCursor: string }) => WebSocket | Promise<WebSocket>
  resolveOwner?: () => Promise<DesktopV3CacheOwner | undefined> | DesktopV3CacheOwner | undefined
  writeOwnerAndTails?: (owner: PersistedDesktopV3OwnerV1, tails: PersistedDesktopV3MessageTailV1[]) => Promise<boolean> | boolean
  saveActiveOwnerKey?: (ownerKey: string) => boolean
  replaceSnapshotAfterDurableCommit?: (previousState: DesktopV3CacheState, nextState: DesktopV3CacheState, actions: DesktopV3CacheAction[]) => void
  streamCommit?: DesktopV3StreamCommitController
  now?: () => number
}

export class DesktopV3RealtimeControllerRuntime implements DesktopV3RealtimeController {
  private readonly getSnapshot: () => DesktopV3CacheState
  private readonly dispatch: (action: DesktopV3CacheAction) => void
  private readonly subscribe: (listener: (mutation?: DesktopV3CacheMutation) => void) => () => void
  private readonly reconnect: (input: DesktopV3ReconnectInput) => Promise<SessionsReconnectResponse>
  private readonly hydrate: (input: DesktopV3HydrateInput) => Promise<SyncSnapshotResponse>
  private readonly readEventsPage: (input: { sessionId: string; afterSeq: number; limit?: number }) => Promise<DesktopV3SessionEventsPage>
  private readonly ensureSessionIdentity: () => Promise<unknown>
  private readonly bootstrap: (input?: { preferredSessionId?: string | null; restorePersisted?: boolean }) => Promise<unknown>
  private readonly transport: DesktopV3RealtimeTransport
  private readonly resolveOwner: () => Promise<DesktopV3CacheOwner | undefined> | DesktopV3CacheOwner | undefined
  private readonly streamCommit: DesktopV3StreamCommitController
  private readonly now: () => number
  private readonly hydrateBySession = new Map<string, Promise<void>>()
  private readonly discoveryHydrateAttemptedBySession = new Set<string>()
  private readonly markedActiveRepairBySession = new Map<string, { runId: string; afterSeq: number }>()
  private readonly activeRepairByRun = new Map<string, Promise<void>>()
  private readonly pendingHistoryRepair = new Set<string>()
  private firstResumeSent?: Deferred<void>
  private startupCancellation?: Deferred<never>
  private unsubscribeCache?: () => void
  private startPromise?: Promise<void>
  private stopped = false

  constructor(deps: DesktopV3RealtimeControllerDeps = {}) {
    this.getSnapshot = deps.getSnapshot ?? getDesktopV3CacheSnapshot
    this.dispatch = deps.dispatch ?? dispatchDesktopV3Cache
    this.subscribe = deps.subscribe ?? subscribeDesktopV3Cache
    this.reconnect = deps.reconnect ?? postDesktopV3Reconnect
    this.hydrate = deps.hydrate ?? postDesktopV3SyncHydrate
    this.readEventsPage = deps.readEventsPage ?? getDesktopV3SessionEventsPage
    this.ensureSessionIdentity = deps.ensureSession ?? ensureDesktopSession
    this.bootstrap = deps.bootstrap ?? (async (input) => {
      await bootstrapDesktopV3SidebarMetadataOnly({
        preferredSessionId: input?.preferredSessionId,
        restorePersisted: input?.restorePersisted,
      })
    })
    this.resolveOwner = deps.resolveOwner ?? resolveCurrentDesktopV3CacheOwner
    this.now = deps.now ?? Date.now
    this.streamCommit = deps.streamCommit ?? new DesktopV3StreamCommitController({
      getSnapshot: this.getSnapshot,
      dispatch: this.dispatch,
      resolveOwner: this.resolveOwner,
      writeOwnerAndTails: deps.writeOwnerAndTails ?? persistDesktopV3OwnerAndTails,
      saveActiveOwnerKey: deps.saveActiveOwnerKey ?? saveDesktopV3CacheActiveOwnerKey,
      replaceSnapshotAfterDurableCommit: deps.replaceSnapshotAfterDurableCommit
        ?? (deps.dispatch ? undefined : replaceDesktopV3CacheSnapshotAfterDurableCommit),
      now: this.now,
    })

    this.transport = new DesktopV3RealtimeTransport({
      getEndpointCursor: () => this.getSnapshot().realtime.endpointCursor,
      openSocket: deps.openSocket ?? (({ endpointCursor }) => openDesktopV3RealtimeTransportSocket({ endpointCursor })),
      onFrame: ({ frame }) => this.handleFrame(frame as RealtimeMessage),
      onStatus: ({ status, reason }) => {
        this.dispatch({
          type: 'realtime.statusChanged',
          status: mapTransportStatus(status),
          error: status === 'error' || status === 'stale' ? reason : undefined,
        })
      },
      onResumeSent: () => this.handleResumeSent(),
      onRehydrateRequested: async (_reason, frame) => {
        const selectedSessionId = this.getSnapshot().selectedSessionId?.trim()
        if (selectedSessionId) this.pendingHistoryRepair.add(selectedSessionId)
        if ((frame as { bootstrap_required?: boolean } | null)?.bootstrap_required) {
          await this.bootstrap({
            preferredSessionId: this.getSnapshot().selectedSessionId,
            restorePersisted: false,
          })
        }
        const reconnect = await this.reconnect(
          buildDesktopV3ReconnectInput(this.getSnapshot(), DESKTOP_V3_CLIENT_ID),
        )
        const durableResumeCursor = frame
          ? undefined
          : this.getSnapshot().realtime.endpointCursor?.trim()
        this.dispatch({ type: 'reconnect.applySnapshot', snapshot: reconnect })
        const resume = this.buildResume(
          reconnect,
          this.getSnapshot().selectedSessionId,
          durableResumeCursor,
        )
        this.dispatch({
          type: 'realtime.storeResume',
          streamPath: reconnect.realtime?.stream_path ?? '/v3/realtime/stream',
          resume,
        })
        this.markActiveRunsFromReconnect(reconnect)
        await this.repairMarkedActiveRuns()

        return {
          endpointCursor: resume.endpoint_cursor,
          snapshotEndpointCursor: reconnect.snapshot_endpoint_cursor,
          subscriptions: resume.subscriptions,
          worksets: normalizeTransportWorksets(resume.worksets),
        }
      },
    })
  }

  start(preferredSessionId?: string | null, bootstrapReady?: Promise<unknown>): Promise<void> {
    if (this.startPromise) return this.startPromise
    this.stopped = false
    this.startupCancellation = createDeferred<never>()
    this.startupCancellation.promise.catch(() => undefined)
    this.firstResumeSent = createDeferred<void>()
    const startPromise = this.startUncached(preferredSessionId, bootstrapReady).finally(() => {
      if (this.startPromise === startPromise) {
        this.startupCancellation = undefined
      }
    })
    this.startPromise = startPromise
    return this.startPromise
  }

  stop(reason = 'Desktop V3 realtime controller stopped'): void {
    this.stopped = true
    const stopError = new Error(reason)
    this.startupCancellation?.reject(stopError)
    this.startupCancellation = undefined
    this.unsubscribeCache?.()
    this.unsubscribeCache = undefined
    this.startPromise = undefined
    this.hydrateBySession.clear()
    this.discoveryHydrateAttemptedBySession.clear()
    this.markedActiveRepairBySession.clear()
    this.activeRepairByRun.clear()
    this.pendingHistoryRepair.clear()
    this.firstResumeSent?.resolve()
    this.firstResumeSent = undefined
    this.transport.stop(reason)
  }

  ensureSessionSubscription(sessionId: string): void {
    const normalized = sessionId.trim()
    if (!normalized) return

    const cursor = this.getSnapshot().realtime.endpointCursor
    if (!cursor) return

    this.transport.registerSession({
      session_id: normalized,
      subscription_id: `${DESKTOP_V3_CLIENT_ID}:session:${normalized}`,
      endpoint_cursor: cursor,
    })
  }

  async ensureSessionHistory(sessionId: string): Promise<void> {
    await this.hydrateSessionOnce(sessionId)
  }

  private async startUncached(preferredSessionId?: string | null, bootstrapReady?: Promise<unknown>): Promise<void> {
    await this.awaitUnlessStopped(this.ensureSessionIdentity())
    this.assertNotStopped()

    await this.waitForSidebarBootstrap(preferredSessionId, bootstrapReady)
    this.assertNotStopped()

    const durableResumeCursor = this.getSnapshot().realtime.endpointCursor?.trim()

    const reconnect = await this.awaitUnlessStopped(this.reconnect(
      buildDesktopV3ReconnectInput(this.getSnapshot(), DESKTOP_V3_CLIENT_ID),
    ))
    this.assertNotStopped()
    if (!reconnect.realtime?.resume) {
      throw new Error('Desktop V3 reconnect response is missing realtime.resume')
    }

    this.dispatch({ type: 'reconnect.applySnapshot', snapshot: reconnect })

    const selectedSessionId = preferredSessionId === null
      ? undefined
      : preferredSessionId?.trim() || this.getSnapshot().selectedSessionId
    const resume = this.buildResume(reconnect, selectedSessionId, durableResumeCursor)

    this.dispatch({
      type: 'realtime.storeResume',
      streamPath: reconnect.realtime?.stream_path ?? '/v3/realtime/stream',
      resume,
    })

    this.transport.setEndpointCursor(resume.endpoint_cursor, 'snapshot')
    this.transport.setWorksets(normalizeTransportWorksets(resume.worksets), { replace: true })
    this.transport.setSessions(resume.subscriptions ?? [], { replace: true })

    this.unsubscribeCache?.()
    this.unsubscribeCache = this.subscribe((mutation) => {
      if (mutation?.action.type !== 'session.select') return
      const sessionId = mutation.action.sessionId?.trim()
      if (sessionId) void this.ensureSession(sessionId)
    })

    this.markActiveRunsFromReconnect(reconnect)
    await this.repairMarkedActiveRuns()
    this.assertNotStopped()

    await this.awaitUnlessStopped(this.transport.start())
    await this.waitForFirstResumeSent()
    this.assertNotStopped()

    if (selectedSessionId) void this.hydrateSessionOnce(selectedSessionId)
  }

  private async waitForSidebarBootstrap(preferredSessionId?: string | null, bootstrapReady?: Promise<unknown>): Promise<void> {
    if (bootstrapReady) {
      await this.awaitUnlessStopped(bootstrapReady)
      return
    }
    if (this.getSnapshot().desktopSidebarBootstrap.scopeId?.trim()) return
    await this.awaitUnlessStopped(this.bootstrap({
      preferredSessionId,
      restorePersisted: true,
    }))
  }

  private async awaitUnlessStopped<T>(promise: Promise<T>): Promise<T> {
    const cancellation = this.startupCancellation?.promise
    if (!cancellation) return promise
    return Promise.race([promise, cancellation])
  }

  private assertNotStopped(): void {
    if (this.stopped) {
      throw new Error('Desktop V3 realtime controller startup was cancelled')
    }
  }

  private buildResume(
    reconnect: SessionsReconnectResponse,
    preferredSessionId?: string | null,
    durableResumeCursor?: string,
  ): NonNullable<SessionsReconnectResponse['realtime']>['resume'] {
    if (!reconnect.realtime?.resume) {
      throw new Error('Desktop V3 reconnect response is missing realtime.resume')
    }

    const resume = structuredClone(reconnect.realtime.resume)
    const endpointCursor = durableResumeCursor?.trim() || reconnect.snapshot_endpoint_cursor
    resume.endpoint_cursor = endpointCursor
    resume.subscriptions = (resume.subscriptions ?? []).map((subscription) => ({
      ...subscription,
      endpoint_cursor: endpointCursor,
    }))

    const selectedSessionId = preferredSessionId === null ? undefined : preferredSessionId?.trim()
    if (!selectedSessionId) return resume

    const subscriptions = [...(resume.subscriptions ?? [])]
    if (!subscriptions.some((subscription) => subscription.session_id === selectedSessionId)) {
      subscriptions.push({
        session_id: selectedSessionId,
        subscription_id: `${DESKTOP_V3_CLIENT_ID}:session:${selectedSessionId}`,
        endpoint_cursor: endpointCursor,
      })
    }
    resume.subscriptions = subscriptions
    return resume
  }

  private async waitForFirstResumeSent(): Promise<void> {
    const firstResumeSent = this.firstResumeSent
    if (!firstResumeSent) return
    await this.awaitUnlessStopped(firstResumeSent.promise)
  }

  private handleResumeSent(): void {
    const isStartupResume = this.firstResumeSent !== undefined
    this.firstResumeSent?.resolve()
    this.firstResumeSent = undefined

    // Also catch selection changes that happened while reconnect was running.
    // Startup selection is hydrated explicitly after the first resume.
    const selectedSessionId = this.getSnapshot().selectedSessionId?.trim()
    if (!isStartupResume && selectedSessionId) {
      this.pendingHistoryRepair.add(selectedSessionId)
    }

    for (const sessionId of Array.from(this.pendingHistoryRepair)) {
      void this.repairHistoryAfterResume(sessionId).catch((error) => {
        console.error('[desktop-v3] selected transcript repair failed', error)
      })
    }

  }

  private async repairHistoryAfterResume(sessionId: string): Promise<void> {
    // This request may have started before the reconnect snapshot.
    const preReconnectHydrate = this.hydrateBySession.get(sessionId)
    if (preReconnectHydrate) {
      await preReconnectHydrate.catch(() => undefined)
    }

    if (this.stopped || !this.pendingHistoryRepair.has(sessionId)) {
      return
    }

    // hydrateSessionOnce now re-evaluates completeness against the newer
    // reconnect projection and starts another request if needed.
    await this.hydrateSessionOnce(sessionId)

    this.pendingHistoryRepair.delete(sessionId)
  }

  private async handleFrame(frame: RealtimeMessage): Promise<void> {
    const sessionId = frame.session_id?.trim() || frame.event?.session_id?.trim() || ''

    try {
      await commitDesktopV3StreamFrame(this.streamCommit, frame)
      if (frame.kind === 'workset.session.discovered' && sessionId) {
        this.hydrateAutoDiscoveredSessionOnce(sessionId)
      }
    } catch (error) {
      this.handleDurableStreamCommitFailure(error)
      throw error
    }
  }

  private handleDurableStreamCommitFailure(error: unknown): void {
    const message = error instanceof Error ? error.message : String(error)

    this.dispatch({
      type: 'realtime.statusChanged',
      status: 'stale',
      errorCode: 'durable_cache_commit_failed',
      error: message,
    })

    this.transport.reopenFromDurableCursor('Desktop V3 durable cache commit failed')
  }

  private async ensureSession(sessionId: string): Promise<void> {
    const normalized = sessionId.trim()
    if (!normalized) return
    this.ensureSessionSubscription(normalized)
    await this.hydrateSessionOnce(normalized)
  }

  private hydrateAutoDiscoveredSessionOnce(sessionId: string): void {
    const normalized = sessionId.trim()
    if (!normalized) return
    if (this.discoveryHydrateAttemptedBySession.has(normalized)) return
    this.discoveryHydrateAttemptedBySession.add(normalized)
    void this.hydrateSessionOnce(normalized).catch((error) => {
      console.error('[desktop-v3] auto-discovered session hydrate failed', error)
    })
  }

  private hydrateSessionOnce(sessionId: string): Promise<void> {
    const normalized = sessionId.trim()
    if (!normalized) return Promise.resolve()
    if (this.sessionMessageTailComplete(normalized)) return Promise.resolve()

    const existing = this.hydrateBySession.get(normalized)
    if (existing) return existing

    const promise = (async () => {
      const response = await this.hydrate(buildDesktopV3InitialHydrateInput([normalized]))
      this.dispatch(hydrateResponseToAction(response, [normalized]))
    })().finally(() => {
      this.hydrateBySession.delete(normalized)
    })
    this.hydrateBySession.set(normalized, promise)
    return promise
  }

  private sessionMessageTailComplete(sessionId: string): boolean {
    const state = this.getSnapshot()
    const projection = state.projectionsBySession[sessionId]
    const messages = state.messagesBySession[sessionId]
    const record = state.sessionsById[sessionId]
    const session = record?.kind === 'full' ? record.session : undefined
    if (!projection || !messages) return false
    const projectionHighWatermark = projectionSeq(projection)
    return (messages.sourceProjectionHighWatermarkSeq ?? 0) >= projectionHighWatermark
      && (session?.message_count === undefined || (messages.sourceMessageCount ?? 0) >= session.message_count)
      && (session?.last_message_at === undefined || (messages.sourceLastMessageAt ?? 0) >= session.last_message_at)
  }

  private markActiveRunsFromReconnect(raw: SessionsReconnectResponse): void {
    this.markedActiveRepairBySession.clear()
    const sessionIds = new Set<string>([
      ...(raw.session_order ?? []),
      ...Object.keys(raw.current_run_intent_by_session ?? {}),
      ...Object.keys(raw.run_intents_by_session ?? {}),
    ])

    for (const sessionId of sessionIds) {
      const explicit = raw.current_run_intent_by_session?.[sessionId]
      const intent = explicit ?? deriveCurrentRunIntent(raw.run_intents_by_session?.[sessionId] ?? [])
      if (!intent) continue

      const restored = this.getSnapshot().liveRunsBySession[sessionId]?.[intent.run_id]
      if (restored && (restored.lastEventSeqSeen ?? 0) >= intent.event_seq) {
        continue
      }

      const status = intent.status.trim().toLowerCase()
      if (!ACTIVE_INTENT_STATUSES.has(status)) continue
      this.markedActiveRepairBySession.set(sessionId, {
        runId: intent.run_id,
        afterSeq: intent.event_seq,
      })
    }
  }

  private async repairMarkedActiveRuns(): Promise<void> {
    const repairs: Promise<void>[] = []

    for (const [sessionId, marked] of this.markedActiveRepairBySession) {
      const key = `${sessionId}:${marked.runId}`
      if (this.activeRepairByRun.has(key)) {
        repairs.push(this.activeRepairByRun.get(key)!)
        continue
      }

      let completed = false

      const pending = this.repairActiveRun(
        sessionId,
        marked.runId,
        marked.afterSeq,
      )
        .then(() => {
          completed = true
        })
        .finally(() => {
          this.activeRepairByRun.delete(key)

          if (completed) {
            const current = this.markedActiveRepairBySession.get(sessionId)

            if (current?.runId === marked.runId) {
              this.markedActiveRepairBySession.delete(sessionId)
            }
          }
        })

      this.activeRepairByRun.set(key, pending)
      repairs.push(pending)
    }

    await Promise.all(repairs)
  }

  private async repairActiveRun(
    sessionId: string,
    expectedRunId: string,
    afterSeq: number,
  ): Promise<void> {
    const runStartSeq = Math.max(0, Math.floor(afterSeq))
    let cursor = runStartSeq

    for (;;) {
      const current = this.getSnapshot().currentRunIntentBySession[sessionId]
      if (!current || current.run_id !== expectedRunId) return

      const page = await this.readEventsPage({
        sessionId,
        afterSeq: cursor,
        limit: ACTIVE_REPAIR_PAGE_SIZE,
      })

      const currentBeforeApply = this.getSnapshot().currentRunIntentBySession[sessionId]
      if (!currentBeforeApply
        || currentBeforeApply.run_id !== expectedRunId
        || !ACTIVE_INTENT_STATUSES.has(currentBeforeApply.status.trim().toLowerCase())) {
        return
      }

      const ordered = [...page.events].sort((a, b) => a.seq - b.seq)
      if (ordered.length > 0) {
        await this.streamCommit.commitActions([{
          type: 'liveRun.mergeRepairEvents',
          sessionId,
          runId: expectedRunId,
          events: ordered.map((event) => ({
            source: 'sync-stream',
            sessionId,
            eventType: event.event_type,
            sessionEvent: event,
            projection: page.projection,
            payload: decodeSessionEventPayload(event),
          })),
        }])
      }

      const latestCurrent = this.getSnapshot().currentRunIntentBySession[sessionId]
      if (!latestCurrent || latestCurrent.run_id !== expectedRunId) return

      const lastReturnedSeq = ordered.length > 0 ? ordered[ordered.length - 1].seq : cursor
      const targetSeq = projectionSeq(page.projection)
      if (lastReturnedSeq <= cursor || lastReturnedSeq >= targetSeq) break
      cursor = lastReturnedSeq
    }
  }
}

export function commitDesktopV3StreamFrame(
  streamCommit: DesktopV3StreamCommitController,
  frame: RealtimeMessage,
): Promise<void> {
  return streamCommit.commitActions(realtimeFrameToActions(frame))
}

export class DesktopV3StreamCommitError extends Error {
  constructor(message: string) {
    super(message)
    this.name = 'DesktopV3StreamCommitError'
  }
}

interface DesktopV3StreamCommitControllerDeps {
  getSnapshot: () => DesktopV3CacheState
  dispatch: (action: DesktopV3CacheAction) => void
  resolveOwner: () => Promise<DesktopV3CacheOwner | undefined> | DesktopV3CacheOwner | undefined
  writeOwnerAndTails: (owner: PersistedDesktopV3OwnerV1, tails: PersistedDesktopV3MessageTailV1[]) => Promise<boolean> | boolean
  saveActiveOwnerKey: (ownerKey: string) => boolean
  replaceSnapshotAfterDurableCommit?: (previousState: DesktopV3CacheState, nextState: DesktopV3CacheState, actions: DesktopV3CacheAction[]) => void
  now: () => number
}

export class DesktopV3StreamCommitController {
  constructor(private readonly deps: DesktopV3StreamCommitControllerDeps) {}

  commitActions(actions: DesktopV3CacheAction[]): Promise<void> {
    if (actions.length === 0) return Promise.resolve()

    return desktopV3CachePersistenceCoordinator.enqueue(async () => {
      const owner = await resolveDesktopV3StreamOwner(this.deps.resolveOwner)
      if (!owner) {
        throw new DesktopV3StreamCommitError('Desktop V3 cache owner is unavailable')
      }

      const previousState = this.deps.getSnapshot()
      const nextState = reduceDesktopV3CacheActions(previousState, actions)
      const persistedAt = this.deps.now()
      const ownerRecord = buildPersistedDesktopV3OwnerV1FromState(nextState, owner, persistedAt)
      if (!ownerRecord) {
        throw new DesktopV3StreamCommitError('Desktop V3 owner record could not be built')
      }

      const tails = buildDesktopV3StreamCommitTails(actions, nextState, owner.key, persistedAt)
      const currentOwner = await resolveDesktopV3StreamOwner(this.deps.resolveOwner)
      if (currentOwner?.key !== owner.key) {
        throw new DesktopV3StreamCommitError('Desktop V3 cache owner changed before stream commit')
      }

      const wrote = await this.deps.writeOwnerAndTails(ownerRecord, tails)
      if (!wrote) {
        throw new DesktopV3StreamCommitError('Desktop V3 stream persistence transaction failed')
      }

      const latestOwner = await resolveDesktopV3StreamOwner(this.deps.resolveOwner)
      if (latestOwner?.key !== owner.key) {
        throw new DesktopV3StreamCommitError('Desktop V3 cache owner changed after stream commit')
      }

      const savedActiveOwner = this.deps.saveActiveOwnerKey(owner.key)
      if (!savedActiveOwner) {
        throw new DesktopV3StreamCommitError(
          'Desktop V3 active owner key persistence failed',
        )
      }

      if (this.deps.replaceSnapshotAfterDurableCommit) {
        this.deps.replaceSnapshotAfterDurableCommit(previousState, nextState, actions)
      } else {
        for (const action of actions) this.deps.dispatch(action)
      }
    })
  }
}

export function reduceDesktopV3CacheActions(
  state: DesktopV3CacheState,
  actions: DesktopV3CacheAction[],
): DesktopV3CacheState {
  let nextState = structuredClone(state)
  for (const action of actions) {
    nextState = desktopV3CacheReducer(nextState, action)
  }
  return nextState
}

function buildDesktopV3StreamCommitTails(
  actions: DesktopV3CacheAction[],
  state: DesktopV3CacheState,
  ownerKey: string,
  persistedAt: number,
): PersistedDesktopV3MessageTailV1[] {
  return committedMessageTailSessionIds(actions)
    .map((sessionId) => buildPersistedDesktopV3MessageTailV1FromState(state, ownerKey, sessionId, persistedAt))
    .filter((tail): tail is PersistedDesktopV3MessageTailV1 => tail !== undefined)
}

export function committedMessageTailSessionIds(actions: readonly DesktopV3CacheAction[]): string[] {
  const ids = new Set<string>()

  for (const action of actions) {
    if (action.type === 'realtime.applyEvent' && action.event.payload.message) {
      ids.add(action.event.sessionId)
    }

    if (action.type === 'mutation.messageResult' && action.raw.ok && action.raw.message) {
      ids.add(action.raw.session_id || action.raw.message.session_id)
    }

    if (action.type === 'syncStream.applyBatch') {
      for (const event of action.events) {
        if (event.payload.message) ids.add(event.sessionId)
      }
    }

    if (action.type === 'liveRun.mergeRepairEvents') {
      for (const event of action.events) {
        if (event.payload.message) ids.add(event.sessionId)
      }
    }
  }

  return [...ids].sort()
}

async function resolveDesktopV3StreamOwner(
  resolveOwner: () => Promise<DesktopV3CacheOwner | undefined> | DesktopV3CacheOwner | undefined,
): Promise<DesktopV3CacheOwner | undefined> {
  const owner = resolveOwner()
  return isPromiseLike(owner) ? await owner : owner
}

function resolveCurrentDesktopV3CacheOwner(): DesktopV3CacheOwner | undefined {
  try {
    const identity = getDesktopSessionIdentitySnapshot()
    return identity ? createDesktopV3CacheOwnerFromIdentity(identity) : undefined
  } catch {
    return undefined
  }
}

function isPromiseLike(value: unknown): value is PromiseLike<unknown> {
  return typeof (value as { then?: unknown } | undefined)?.then === 'function'
}

export function buildDesktopV3ReconnectInput(
  state: DesktopV3CacheState,
  clientId: string,
): DesktopV3ReconnectInput {
  const sidebarScopeId = state.desktopSidebarBootstrap.scopeId?.trim()
  if (!sidebarScopeId) {
    throw new Error('Desktop V3 reconnect requires the bootstrap sidebar scope')
  }

  const sidebarScope = state.syncScopesById[sidebarScopeId]
  if (!sidebarScope) {
    throw new Error(`Desktop V3 reconnect missing scope ${sidebarScopeId}`)
  }

  if (sidebarScope.selector.kind !== 'global' || !sidebarScope.selector.global) {
    throw new Error('Desktop V3 reconnect requires the principal-wide global selector')
  }

  return {
    surface: 'desktop',
    client_id: clientId,
    workset: {
      workset_id: sidebarScopeId,
      selector: sidebarScope.selector,
      history: {
        mode: 'none',
      },
      resources: {
        messages: false,
        events: false,
        run_intents: true,
        active_plan: false,
        plan_revisions: false,
      },
      include_active: true,
      auto_subscribe_sessions: true,
    },
  }
}

export function mapTransportStatus(
  status: DesktopV3RealtimeTransportStatus,
): RealtimeCache['status'] {
  switch (status) {
    case 'stopped':
    case 'closed':
      return 'closed'
    case 'connecting':
      return 'connecting'
    case 'open':
      return 'open'
    case 'reopening':
    case 'rehydrating':
      return 'reconnecting'
    case 'stale':
      return 'stale'
    case 'error':
      return 'error'
  }
}

export function deriveCurrentRunIntent(
  intents: V3SessionRunIntent[],
): V3SessionRunIntent | undefined {
  return [...intents]
    .filter((intent) => ACTIVE_INTENT_STATUSES.has(intent.status.trim().toLowerCase()))
    .sort((left, right) =>
      right.updated_at - left.updated_at
      || right.event_seq - left.event_seq
      || right.run_id.localeCompare(left.run_id),
    )[0]
}

export function retainDesktopV3RealtimeController(input: {
  ownerKey?: string
  preferredSessionId?: string | null
  bootstrap?: Promise<unknown>
} = {}): DesktopV3RealtimeLease {
  const ownerKey = input.ownerKey?.trim()
  let retained = retainedDesktopV3RealtimeController
  if (retained) {
    clearPendingDesktopV3RealtimeRelease(retained)
  }

  if (!retained) {
    const controller = desktopV3RealtimeControllerFactory()
    const generation = desktopV3RealtimeGeneration
    const created: RetainedDesktopV3RealtimeController = {
      controller,
      ready: Promise.resolve(),
      ownerTokens: new Map<string, symbol>(),
      anonymousRetainCount: 0,
    }
    const bootstrapReady = input.bootstrap?.then((value) => {
      if (retainedDesktopV3RealtimeController !== created || retainedDesktopV3RealtimeCount(created) <= 0) {
        throw new Error('Desktop V3 realtime lease released')
      }
      return value
    })
    created.ready = controller.start(input.preferredSessionId, bootstrapReady).catch((error) => {
      const stillCurrent = retainedDesktopV3RealtimeController === created && desktopV3RealtimeGeneration === generation
      if (stillCurrent) {
        controller.stop('Desktop V3 realtime controller startup failed')
        if (retainedDesktopV3RealtimeCount(created) <= 0) {
          retainedDesktopV3RealtimeController = undefined
          desktopV3RealtimeGeneration += 1
        }
      }
      throw error
    })
    retained = created
    retainedDesktopV3RealtimeController = created
  }

  const ownerToken = ownerKey ? Symbol(ownerKey) : undefined
  if (ownerKey && ownerToken) {
    retained.ownerTokens.set(ownerKey, ownerToken)
  } else {
    retained.anonymousRetainCount += 1
  }

  let released = false
  return {
    ready: retained.ready,
    release: () => {
      if (released) return
      released = true
      const current = retainedDesktopV3RealtimeController
      if (current !== retained) return
      if (ownerKey) {
        if (ownerToken && current.ownerTokens.get(ownerKey) === ownerToken) {
          current.ownerTokens.delete(ownerKey)
        }
      } else if (current.anonymousRetainCount > 0) {
        current.anonymousRetainCount -= 1
      }
      releaseDesktopV3RealtimeControllerIfUnused(
        current,
        'Desktop V3 realtime lease released',
        Boolean(ownerKey),
      )
    },
  }
}

export async function requireDesktopV3RealtimeControllerReady(): Promise<DesktopV3RealtimeController> {
  const retained = retainedDesktopV3RealtimeController
  if (!retained || retainedDesktopV3RealtimeCount(retained) <= 0) {
    throw new Error('Desktop V3 realtime controller is not retained by the desktop page')
  }
  await retained.ready
  return retained.controller
}

export function resetDesktopV3RealtimeControllerForTests(): void {
  if (retainedDesktopV3RealtimeController) {
    clearPendingDesktopV3RealtimeRelease(retainedDesktopV3RealtimeController)
    retainedDesktopV3RealtimeController.controller.stop('Desktop V3 realtime controller reset')
  }
  retainedDesktopV3RealtimeController = undefined
  desktopV3RealtimeGeneration += 1
  desktopV3RealtimeControllerFactory = () => new DesktopV3RealtimeControllerRuntime()
}

export function setDesktopV3RealtimeControllerFactoryForTests(factory: () => DesktopV3RealtimeControllerRuntime): void {
  resetDesktopV3RealtimeControllerForTests()
  desktopV3RealtimeControllerFactory = factory
}

let retainedDesktopV3RealtimeController: RetainedDesktopV3RealtimeController | undefined
let desktopV3RealtimeGeneration = 0
let desktopV3RealtimeControllerFactory = () => new DesktopV3RealtimeControllerRuntime()

interface RetainedDesktopV3RealtimeController {
  controller: DesktopV3RealtimeControllerRuntime
  ready: Promise<void>
  ownerTokens: Map<string, symbol>
  anonymousRetainCount: number
  releaseTimer?: ReturnType<typeof setTimeout>
}

function retainedDesktopV3RealtimeCount(retained: RetainedDesktopV3RealtimeController): number {
  return retained.ownerTokens.size + retained.anonymousRetainCount
}

function clearPendingDesktopV3RealtimeRelease(retained: RetainedDesktopV3RealtimeController): void {
  if (!retained.releaseTimer) return
  clearTimeout(retained.releaseTimer)
  retained.releaseTimer = undefined
}

function releaseDesktopV3RealtimeControllerIfUnused(
  retained: RetainedDesktopV3RealtimeController,
  reason: string,
  delayed: boolean,
): void {
  if (retainedDesktopV3RealtimeCount(retained) > 0) return

  const stop = () => {
    if (retainedDesktopV3RealtimeController !== retained) return
    clearPendingDesktopV3RealtimeRelease(retained)
    if (retainedDesktopV3RealtimeCount(retained) > 0) return
    desktopV3RealtimeGeneration += 1
    retained.controller.stop(reason)
    if (retainedDesktopV3RealtimeController === retained) {
      retainedDesktopV3RealtimeController = undefined
    }
  }

  if (!delayed) {
    stop()
    return
  }

  clearPendingDesktopV3RealtimeRelease(retained)
  retained.releaseTimer = setTimeout(stop, 0)
}

interface Deferred<T> {
  promise: Promise<T>
  resolve: (value: T | PromiseLike<T>) => void
  reject: (reason?: unknown) => void
}

function createDeferred<T>(): Deferred<T> {
  let resolve!: (value: T | PromiseLike<T>) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((promiseResolve, promiseReject) => {
    resolve = promiseResolve
    reject = promiseReject
  })
  return { promise, resolve, reject }
}

function normalizeTransportWorksets(
  worksets: RealtimeWorksetSubscriptionRequest[] | undefined,
): SessionV3RealtimeWorksetSubscriptionRequestWire[] {
  return (worksets ?? []).map((workset) => ({
    workset_id: workset.workset_id,
    subscription_id: workset.subscription_id,
    surface: workset.surface,
    selector: workset.selector,
    resources: workset.resources,
    auto_subscribe_sessions: Boolean(workset.auto_subscribe_sessions),
  }))
}

function projectionSeq(projection: V3SessionProjection | undefined): number {
  return Math.max(
    projection?.last_event_seq ?? 0,
    projection?.projection_high_watermark_seq ?? 0,
  )
}
