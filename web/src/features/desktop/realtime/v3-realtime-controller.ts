import { ensureDesktopSession } from '../../../app/api'
import { DesktopV3RealtimeTransport, type DesktopV3RealtimeTransportStatus } from '../session-v3/transport'
import type { SessionV3RealtimeWorksetSubscriptionRequestWire } from '../session-v3/types'
import { getDesktopV3SessionEventsPage, type DesktopV3SessionEventsPage } from '../session-v3/read-api'
import { openDesktopV3RealtimeTransportSocket } from './client'
import { bootstrapDesktopV3SidebarMetadataOnly } from '../state/desktop-v3-bootstrap-controller'
import { desktopV3CacheReducer } from '../state/desktop-v3-cache-reducer'
import { dispatchDesktopV3Cache, getDesktopV3CacheSnapshot, commitDesktopV3CacheSnapshot, subscribeDesktopV3Cache, type DesktopV3CacheMutation } from '../state/desktop-v3-cache-store'
import { decodeSessionEventPayload, hydrateResponseToAction, realtimeFrameToActions } from '../state/desktop-v3-cache-wire'
import {
  buildDesktopV3SelectedSessionHydrateInput,
  postDesktopV3Reconnect,
  postDesktopV3SyncHydrate,
  type DesktopV3ReconnectInput,
  type DesktopV3HydrateInput,
} from '../state/desktop-v3-sync-api'
import { isDesktopV3SessionTailReady } from '../state/desktop-v3-cache-selectors'
import type {
  DesktopV3CacheAction,
  DesktopV3CacheState,
  SyncSelector,
  RealtimeCache,
  RealtimeMessage,
  RealtimeSubscriptionRequest,
  RealtimeWorksetSubscriptionRequest,
  SessionsReconnectResponse,
  SyncSnapshotResponse,
  V3SessionProjection,
  V3SessionRunIntent,
} from '../state/desktop-v3-cache-types'

const DESKTOP_V3_CLIENT_ID = `desktop:${crypto.randomUUID()}`
const ACTIVE_REPAIR_PAGE_SIZE = 100
const ACTIVE_REPAIR_HIDDEN_MAX_CONCURRENT = 4
const ACTIVE_REPAIR_HIDDEN_MAX_SESSIONS_PER_GENERATION = 25
const ACTIVE_REPAIR_HIDDEN_MAX_PAGES_PER_SESSION = 2
const ACTIVE_REPAIR_HIDDEN_MAX_EVENTS_PER_GENERATION = 500
const ACTIVE_INTENT_STATUSES = new Set(['pending_executor', 'running', 'dispatch_blocked'])

export interface DesktopV3SessionConnectInput {
  sessionId: string
  endpointCursor?: string
}

export interface DesktopV3RealtimeController {
  currentEndpointCursor(): string
  connectSession(input: DesktopV3SessionConnectInput): Promise<void>
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
  bootstrap?: (input?: { preferredSessionId?: string | null }) => Promise<unknown>
  openSocket?: (input: { endpointCursor: string }) => WebSocket | Promise<WebSocket>
  commitSnapshot?: (previousState: DesktopV3CacheState, nextState: DesktopV3CacheState, actions: DesktopV3CacheAction[]) => void
  streamCommit?: DesktopV3StreamCommitController
}

export class DesktopV3RealtimeControllerRuntime implements DesktopV3RealtimeController {
  private readonly getSnapshot: () => DesktopV3CacheState
  private readonly dispatch: (action: DesktopV3CacheAction) => void
  private readonly subscribe: (listener: (mutation?: DesktopV3CacheMutation) => void) => () => void
  private readonly reconnect: (input: DesktopV3ReconnectInput) => Promise<SessionsReconnectResponse>
  private readonly hydrate: (input: DesktopV3HydrateInput) => Promise<SyncSnapshotResponse>
  private readonly readEventsPage: (input: { sessionId: string; afterSeq: number; limit?: number }) => Promise<DesktopV3SessionEventsPage>
  private readonly ensureSessionIdentity: () => Promise<unknown>
  private readonly bootstrap: (input?: { preferredSessionId?: string | null }) => Promise<unknown>
  private readonly transport: DesktopV3RealtimeTransport
  private readonly streamCommit: DesktopV3StreamCommitController
  private readonly hydrateBySession = new Map<string, Promise<void>>()
  private readonly markedActiveRepairBySession = new Map<string, { runId: string; afterSeq: number }>()
  private readonly activeRepairByRun = new Map<string, { generation: number }>()
  private readonly pendingHistoryRepair = new Set<string>()
  private readonly connectingSessionIds = new Set<string>()
  private firstResumeSent?: Deferred<void>
  private startupCancellation?: Deferred<never>
  private unsubscribeCache?: () => void
  private startPromise?: Promise<void>
  private repairGeneration = 0
  private hiddenRepairInFlight = 0
  private hiddenRepairBudget = {
    generation: 0,
    sessionsStarted: 0,
    eventsApplied: 0,
  }
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
      })
    })
    this.streamCommit = deps.streamCommit ?? new DesktopV3StreamCommitController({
      getSnapshot: this.getSnapshot,
      dispatch: this.dispatch,
      commitSnapshot: deps.commitSnapshot
        ?? (deps.dispatch ? undefined : commitDesktopV3CacheSnapshot),
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
          })
        }
        const reconnect = await this.reconnect(
          buildDesktopV3ReconnectInput(this.getSnapshot(), DESKTOP_V3_CLIENT_ID),
        )
        const cursorWasRejected = frame?.kind === 'cursor.error'
        const durableResumeCursor = cursorWasRejected
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
        const generation = this.beginActiveRepairGeneration()
        setTimeout(() => this.scheduleMarkedActiveRunRepairs(generation), 0)

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
    this.beginActiveRepairGeneration()
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
    this.markedActiveRepairBySession.clear()
    this.activeRepairByRun.clear()
    this.repairGeneration += 1
    this.hiddenRepairInFlight = 0
    this.pendingHistoryRepair.clear()
    this.connectingSessionIds.clear()
    this.firstResumeSent?.resolve()
    this.firstResumeSent = undefined
    this.transport.stop(reason)
  }

  currentEndpointCursor(): string {
    const cursor = this.getSnapshot().realtime.endpointCursor?.trim()
    if (!cursor) {
      throw new Error('Desktop V3 realtime has no durable endpoint cursor')
    }
    return cursor
  }

  connectSession(input: DesktopV3SessionConnectInput): Promise<void> {
    const sessionId = input.sessionId.trim()
    if (!sessionId) {
      return Promise.reject(new Error('Desktop V3 session connect requires sessionId'))
    }
    let endpointCursor: string
    try {
      endpointCursor = input.endpointCursor?.trim() || this.currentEndpointCursor()
    } catch (error) {
      return Promise.reject(error)
    }
    this.connectingSessionIds.add(sessionId)
    const connect = this.transport.subscribeSession({
      session_id: sessionId,
      subscription_id: `${DESKTOP_V3_CLIENT_ID}:session:${sessionId}`,
      endpoint_cursor: endpointCursor,
    })
    connect.finally(() => {
      this.connectingSessionIds.delete(sessionId)
      this.reconcileDesiredSessionConnections()
    }).catch(() => undefined)
    return connect
  }

  async ensureSessionHistory(sessionId: string): Promise<void> {
    await this.hydrateSessionOnce(sessionId)
  }

  private async startUncached(preferredSessionId?: string | null, bootstrapReady?: Promise<unknown>): Promise<void> {
    await this.awaitUnlessStopped(this.ensureSessionIdentity())
    this.assertNotStopped()

    await this.waitForSidebarBootstrap(preferredSessionId, bootstrapReady)
    this.assertNotStopped()

    const initial = buildDesktopV3InitialRealtimeResume(
      this.getSnapshot(),
      DESKTOP_V3_CLIENT_ID,
      preferredSessionId,
    )

    this.dispatch({
      type: 'realtime.storeResume',
      streamPath: '/v3/realtime/stream',
      resume: initial.resume,
    })

    this.transport.setEndpointCursor(initial.endpointCursor, 'snapshot')
    this.transport.setWorksets(normalizeTransportWorksets(initial.worksets), { replace: true })
    this.transport.setSessions(initial.subscriptions, { replace: true })

    this.unsubscribeCache?.()
    this.unsubscribeCache = this.subscribe((mutation) => {
      this.reconcileDesiredSessionConnections()
      if (mutation?.action.type !== 'session.select') return
      const sessionId = mutation.action.sessionId?.trim()
      if (sessionId) void this.hydrateSessionOnce(sessionId)
    })
    this.reconcileDesiredSessionConnections()

    this.markActiveRunsFromCacheState(this.getSnapshot())

    await this.awaitUnlessStopped(this.transport.start())
    await this.waitForFirstResumeSent()
    this.assertNotStopped()

    const selectedSessionId = preferredSessionId === null
      ? undefined
      : preferredSessionId?.trim() || this.getSnapshot().selectedSessionId
    if (selectedSessionId) void this.hydrateSessionOnce(selectedSessionId)
    this.scheduleMarkedActiveRunRepairs(this.repairGeneration)
  }

  private async waitForSidebarBootstrap(preferredSessionId?: string | null, bootstrapReady?: Promise<unknown>): Promise<void> {
    if (bootstrapReady) {
      await this.awaitUnlessStopped(bootstrapReady)
      return
    }
    if (this.getSnapshot().desktopSidebarBootstrap.scopeId?.trim()) return
    await this.awaitUnlessStopped(this.bootstrap({
      preferredSessionId,
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
    const subscriptions = new Map<string, RealtimeSubscriptionRequest>()
    for (const subscription of resume.subscriptions ?? []) {
      const sessionId = subscription.session_id?.trim()
      if (!sessionId || subscriptions.has(sessionId)) continue
      subscriptions.set(sessionId, {
        ...subscription,
        session_id: sessionId,
        endpoint_cursor: endpointCursor,
      })
    }

    const addRecoverySubscription = (sessionId: string | null | undefined) => {
      const normalized = sessionId?.trim()
      if (!normalized || subscriptions.has(normalized)) return
      subscriptions.set(normalized, {
        session_id: normalized,
        subscription_id: `${DESKTOP_V3_CLIENT_ID}:session:${normalized}`,
        endpoint_cursor: endpointCursor,
      })
    }

    const state = this.getSnapshot()
    addRecoverySubscription(selectedSessionId)
    for (const sessionId of activeRealtimeSessionIds(state)) {
      addRecoverySubscription(sessionId)
    }
    for (const pending of Object.values(state.pendingUserByClientRequestId)) {
      if (pending.status !== 'pending') continue
      addRecoverySubscription(pending.sessionId)
    }
    for (const sessionId of this.connectingSessionIds) {
      addRecoverySubscription(sessionId)
    }

    resume.subscriptions = Array.from(subscriptions.values())
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
    try {
      await commitDesktopV3StreamFrame(this.streamCommit, frame)
      if (frame.kind === 'workset.session.discovered' || frame.kind === 'workset.session.removed') {
        // The transport records auto-discovered subscriptions immediately after
        // onFrame resolves; reconcile on the next macrotask so hidden sessions
        // can be unsubscribed without waiting for transcript hydration.
        setTimeout(() => {
          if (!this.stopped) this.reconcileDesiredSessionConnections()
        }, 0)
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
      errorCode: 'runtime_store_commit_failed',
      error: message,
    })

    this.transport.reopenFromDurableCursor('Desktop V3 runtime store commit failed')
  }

  private reconcileDesiredSessionConnections(): void {
    if (this.stopped) return
    const state = this.getSnapshot()
    const desired = new Set<string>()
    const addDesired = (sessionId: string | null | undefined) => {
      const normalized = sessionId?.trim()
      if (normalized) desired.add(normalized)
    }

    addDesired(state.selectedSessionId)

    for (const sessionId of activeRealtimeSessionIds(state)) {
      addDesired(sessionId)
    }

    for (const pending of Object.values(state.pendingUserByClientRequestId)) {
      if (pending.status === 'pending') addDesired(pending.sessionId)
    }

    for (const sessionId of this.connectingSessionIds) {
      addDesired(sessionId)
    }

    const diagnostics = this.transport.diagnostics()
    if (diagnostics.status === 'rehydrating' || diagnostics.status === 'stale') return

    const registered = new Map(diagnostics.sessions.map((session) => [session.session_id, session]))

    for (const sessionId of desired) {
      if (registered.has(sessionId)) continue
      const connect = this.connectSession({ sessionId })
      connect.catch((error) => {
        if (!this.stopped) {
          console.error('[desktop-v3] desired session connection failed', error)
        }
      })
    }

    for (const sessionId of registered.keys()) {
      if (desired.has(sessionId)) continue
      this.transport.unsubscribeSession(sessionId)
    }
  }

  private hydrateSessionOnce(sessionId: string): Promise<void> {
    const normalized = sessionId.trim()
    if (!normalized) return Promise.resolve()
    if (this.sessionMessageTailComplete(normalized)) return Promise.resolve()

    const existing = this.hydrateBySession.get(normalized)
    if (existing) return existing

    const promise = (async () => {
      this.dispatch({ type: 'desktopV3Cache.markHydrateInFlight', sessionIds: [normalized], inFlight: true })
      const response = await this.hydrate(buildDesktopV3SelectedSessionHydrateInput(normalized))
      this.dispatch(hydrateResponseToAction(response, [normalized]))
    })().finally(() => {
      this.dispatch({ type: 'desktopV3Cache.markHydrateInFlight', sessionIds: [normalized], inFlight: false })
      this.hydrateBySession.delete(normalized)
      this.reconcileDesiredSessionConnections()
    })
    this.hydrateBySession.set(normalized, promise)
    return promise
  }

  private sessionMessageTailComplete(sessionId: string): boolean {
    return isDesktopV3SessionTailReady(this.getSnapshot(), sessionId)
  }

  private markActiveRunsFromCacheState(state: DesktopV3CacheState): void {
    this.markedActiveRepairBySession.clear()
    for (const [sessionId, intent] of Object.entries(state.currentRunIntentBySession)) {
      if (!intent) continue
      const status = intent.status.trim().toLowerCase()
      if (!ACTIVE_INTENT_STATUSES.has(status)) continue

      const restored = state.liveRunsBySession[sessionId]?.[intent.run_id]
      const restoredSeq = restored?.lastEventSeqSeen ?? 0
      const targetSeq = projectionSeq(state.projectionsBySession[sessionId])
      if (targetSeq <= 0 || restoredSeq >= targetSeq) continue

      this.markedActiveRepairBySession.set(sessionId, {
        runId: intent.run_id,
        afterSeq: restoredSeq,
      })
    }
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
      const restoredSeq = restored?.lastEventSeqSeen ?? 0
      if (restoredSeq >= intent.event_seq) {
        continue
      }

      const status = intent.status.trim().toLowerCase()
      if (!ACTIVE_INTENT_STATUSES.has(status)) continue
      this.markedActiveRepairBySession.set(sessionId, {
        runId: intent.run_id,
        afterSeq: restoredSeq,
      })
    }
  }

  private beginActiveRepairGeneration(): number {
    this.repairGeneration += 1
    this.hiddenRepairInFlight = 0
    this.hiddenRepairBudget = {
      generation: this.repairGeneration,
      sessionsStarted: 0,
      eventsApplied: 0,
    }
    return this.repairGeneration
  }

  private scheduleMarkedActiveRunRepairs(generation: number): void {
    if (this.stopped || generation !== this.repairGeneration) return
    const selectedSessionId = this.getSnapshot().selectedSessionId?.trim()
    const hiddenCandidates: Array<[string, { runId: string; afterSeq: number }]> = []

    for (const [sessionId, marked] of this.markedActiveRepairBySession) {
      if (sessionId === selectedSessionId) {
        this.startActiveRunRepair(sessionId, marked, generation, false)
      } else {
        hiddenCandidates.push([sessionId, marked])
      }
    }

    for (const [sessionId, marked] of hiddenCandidates) {
      if (this.hiddenRepairInFlight >= ACTIVE_REPAIR_HIDDEN_MAX_CONCURRENT) break
      if (this.hiddenRepairBudget.sessionsStarted >= ACTIVE_REPAIR_HIDDEN_MAX_SESSIONS_PER_GENERATION) break
      if (this.hiddenRepairBudget.eventsApplied >= ACTIVE_REPAIR_HIDDEN_MAX_EVENTS_PER_GENERATION) break
      this.startActiveRunRepair(sessionId, marked, generation, true)
    }
  }

  private startActiveRunRepair(
    sessionId: string,
    marked: { runId: string; afterSeq: number },
    generation: number,
    hidden: boolean,
  ): void {
    const key = `${sessionId}:${marked.runId}`
    const existing = this.activeRepairByRun.get(key)
    if (existing) return
    if (hidden) {
      this.hiddenRepairInFlight += 1
      this.hiddenRepairBudget.sessionsStarted += 1
    }

    let completed = false
    this.repairActiveRun(
      sessionId,
      marked.runId,
      marked.afterSeq,
      generation,
      hidden,
    )
      .then((result) => {
        completed = result === 'completed'
      })
      .catch((error) => {
        if (!this.stopped && generation === this.repairGeneration) {
          console.error('[desktop-v3] active run repair failed', error)
        }
      })
      .finally(() => {
        const currentRepair = this.activeRepairByRun.get(key)
        if (currentRepair?.generation === generation) {
          this.activeRepairByRun.delete(key)
        }
        if (hidden) {
          this.hiddenRepairInFlight = Math.max(0, this.hiddenRepairInFlight - 1)
        }
        if (completed && generation === this.repairGeneration) {
          const current = this.markedActiveRepairBySession.get(sessionId)
          if (current?.runId === marked.runId) {
            this.markedActiveRepairBySession.delete(sessionId)
          }
        }
        if (hidden && generation === this.repairGeneration && !this.stopped) {
          this.scheduleMarkedActiveRunRepairs(generation)
        }
      })

    this.activeRepairByRun.set(key, { generation })
  }

  private repairStillCurrent(sessionId: string, expectedRunId: string, generation: number): boolean {
    if (this.stopped || generation !== this.repairGeneration) return false
    const current = this.getSnapshot().currentRunIntentBySession[sessionId]
    if (!current || current.run_id !== expectedRunId) return false
    return ACTIVE_INTENT_STATUSES.has(current.status.trim().toLowerCase())
  }

  private async repairActiveRun(
    sessionId: string,
    expectedRunId: string,
    afterSeq: number,
    generation: number,
    hidden: boolean,
  ): Promise<'completed' | 'cancelled' | 'budget_exhausted'> {
    const runStartSeq = Math.max(0, Math.floor(afterSeq))
    let cursor = runStartSeq
    let pagesRead = 0

    for (;;) {
      if (!this.repairStillCurrent(sessionId, expectedRunId, generation)) return 'cancelled'
      if (hidden) {
        if (pagesRead >= ACTIVE_REPAIR_HIDDEN_MAX_PAGES_PER_SESSION) return 'budget_exhausted'
        if (this.hiddenRepairBudget.eventsApplied >= ACTIVE_REPAIR_HIDDEN_MAX_EVENTS_PER_GENERATION) return 'budget_exhausted'
      }

      const page = await this.readEventsPage({
        sessionId,
        afterSeq: cursor,
        limit: ACTIVE_REPAIR_PAGE_SIZE,
      })
      pagesRead += 1

      if (!this.repairStillCurrent(sessionId, expectedRunId, generation)) return 'cancelled'

      let ordered = [...page.events].sort((a, b) => a.seq - b.seq)
      if (hidden) {
        const remaining = Math.max(0, ACTIVE_REPAIR_HIDDEN_MAX_EVENTS_PER_GENERATION - this.hiddenRepairBudget.eventsApplied)
        if (remaining <= 0) return 'budget_exhausted'
        ordered = ordered.slice(0, remaining)
      }

      if (ordered.length > 0) {
        if (!this.repairStillCurrent(sessionId, expectedRunId, generation)) return 'cancelled'
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
        if (hidden) this.hiddenRepairBudget.eventsApplied += ordered.length
      }

      if (!this.repairStillCurrent(sessionId, expectedRunId, generation)) return 'cancelled'

      const lastReturnedSeq = ordered.length > 0 ? ordered[ordered.length - 1].seq : cursor
      const targetSeq = projectionSeq(page.projection)
      if (lastReturnedSeq <= cursor || lastReturnedSeq >= targetSeq) break
      cursor = lastReturnedSeq
    }
    return 'completed'
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
  commitSnapshot?: (previousState: DesktopV3CacheState, nextState: DesktopV3CacheState, actions: DesktopV3CacheAction[]) => void
}


export class DesktopV3StreamCommitController {
  constructor(private readonly deps: DesktopV3StreamCommitControllerDeps) {}

  commitActions(actions: DesktopV3CacheAction[]): Promise<void> {
    if (actions.length === 0) return Promise.resolve()

    const previousState = this.deps.getSnapshot()
    const nextState = reduceDesktopV3CacheActions(previousState, actions)
    if (this.deps.commitSnapshot) {
      this.deps.commitSnapshot(previousState, nextState, actions)
    } else {
      for (const action of actions) this.deps.dispatch(action)
    }
    return Promise.resolve()
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

export function buildDesktopV3InitialRealtimeResume(
  state: DesktopV3CacheState,
  clientId: string,
  preferredSessionId?: string | null,
): {
  endpointCursor: string
  subscriptions: RealtimeSubscriptionRequest[]
  worksets: RealtimeWorksetSubscriptionRequest[]
  resume: RealtimeMessage
} {
  const sidebarScopeId = state.desktopSidebarBootstrap.scopeId?.trim()
  if (!sidebarScopeId) {
    throw new Error('Desktop V3 initial realtime requires the bootstrapped sidebar scope')
  }

  const sidebarScope = state.syncScopesById[sidebarScopeId]
  if (!sidebarScope) {
    throw new Error(`Desktop V3 initial realtime missing scope ${sidebarScopeId}`)
  }

  const endpointCursor = sidebarScope.endpointCursor?.trim()
  if (!endpointCursor) {
    throw new Error('Desktop V3 initial realtime requires the bootstrap endpoint cursor')
  }

  const selectedSessionId = preferredSessionId === null
    ? undefined
    : preferredSessionId?.trim() || state.selectedSessionId?.trim()
  const activeSessionIDs = activeRealtimeSessionIds(state)

  const sessionOrder = state.sessionOrderByScope[sidebarScopeId] ?? []
  const orderedSessionIDs: string[] = []
  const seen = new Set<string>()
  const append = (sessionId: string | null | undefined) => {
    const normalized = sessionId?.trim()
    if (!normalized || seen.has(normalized)) return
    seen.add(normalized)
    orderedSessionIDs.push(normalized)
  }

  append(selectedSessionId)
  for (const sessionId of sessionOrder) {
    const normalized = sessionId.trim()
    if (activeSessionIDs.has(normalized)) append(normalized)
  }
  for (const sessionId of [...activeSessionIDs].filter((sessionId) => !seen.has(sessionId)).sort()) {
    append(sessionId)
  }

  const subscriptions = orderedSessionIDs.map((sessionId) => ({
    session_id: sessionId,
    subscription_id: `${clientId}:session:${sessionId}`,
    endpoint_cursor: endpointCursor,
  }))
  const worksets: RealtimeWorksetSubscriptionRequest[] = [{
    workset_id: sidebarScopeId,
    subscription_id: `${clientId}:workset:${sidebarScopeId}`,
    surface: 'desktop',
    selector: cloneDesktopV3SyncSelector(sidebarScope.selector),
    resources: ['membership', 'projections', 'run_intents', 'sessions', 'tombstones'],
    auto_subscribe_sessions: true,
  }]
  const resume: RealtimeMessage = {
    protocol: 'v3.realtime',
    protocol_version: 1,
    kind: 'resume',
    endpoint_cursor: endpointCursor,
    subscriptions,
    worksets,
  }

  return { endpointCursor, subscriptions, worksets, resume }
}

export function activeRealtimeSessionIds(state: DesktopV3CacheState): Set<string> {
  const activeSessionIDs = new Set<string>()
  for (const [sessionId, intent] of Object.entries(state.currentRunIntentBySession)) {
    if (!intent) continue
    if (!ACTIVE_INTENT_STATUSES.has(intent.status.trim().toLowerCase())) continue
    const normalized = intent.session_id?.trim() || sessionId.trim()
    if (normalized) activeSessionIDs.add(normalized)
  }
  return activeSessionIDs
}

export function cloneDesktopV3SyncSelector(selector: SyncSelector): SyncSelector {
  const clone: SyncSelector = { ...selector }
  if (selector.workspace_paths) clone.workspace_paths = [...selector.workspace_paths]
  if (selector.session_ids) clone.session_ids = [...selector.session_ids]
  if (selector.recent) clone.recent = { ...selector.recent }
  return clone
}

export function isDesktopV3BoundedSidebarSelector(selector: SyncSelector): boolean {
  const kind = selector.kind?.trim().toLowerCase()
  if (kind === 'global' && selector.global && !selector.recent?.limit && !selector.session_ids?.length && !selector.workspace_path?.trim() && !selector.workspace_paths?.length) {
    return false
  }
  return Boolean(selector.recent?.limit || selector.session_ids?.length || selector.workspace_path?.trim() || selector.workspace_paths?.length || kind === 'workspace' || kind === 'session_ids' || kind === 'recent' || kind === 'tui')
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

  if (!isDesktopV3BoundedSidebarSelector(sidebarScope.selector)) {
    throw new Error('Desktop V3 reconnect requires a bounded sidebar selector')
  }

  return {
    surface: 'desktop',
    client_id: clientId,
    workset: {
      workset_id: sidebarScopeId,
      selector: cloneDesktopV3SyncSelector(sidebarScope.selector),
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
