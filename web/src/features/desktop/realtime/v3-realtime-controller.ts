import { ensureDesktopSession } from '../../../app/api'
import { DesktopV3RealtimeTransport, type DesktopV3RealtimeTransportStatus } from '../session-v3/transport'
import type { SessionV3RealtimeWorksetSubscriptionRequestWire } from '../session-v3/types'
import { getDesktopV3SessionEventsPage, type DesktopV3SessionEventsPage } from '../session-v3/read-api'
import { openDesktopV3RealtimeTransportSocket } from './client'
import { bootstrapDesktopV3Sidebar } from '../state/desktop-v3-bootstrap-controller'
import { dispatchDesktopV3Cache, getDesktopV3CacheSnapshot, subscribeDesktopV3Cache, type DesktopV3CacheMutation } from '../state/desktop-v3-cache-store'
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
const ACTIVE_INTENT_STATUSES = new Set(['pending_executor', 'running', 'dispatch_blocked'])

export interface DesktopV3RealtimeController {
  ensureSessionSubscription(sessionId: string): void
  ensureSessionHistory(sessionId: string): Promise<void>
  start(preferredSessionId?: string): Promise<void>
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
  bootstrap?: (input?: { preferredSessionId?: string; restorePersisted?: boolean }) => Promise<unknown>
  openSocket?: (input: { endpointCursor: string }) => WebSocket | Promise<WebSocket>
}

export class DesktopV3RealtimeControllerRuntime implements DesktopV3RealtimeController {
  private readonly getSnapshot: () => DesktopV3CacheState
  private readonly dispatch: (action: DesktopV3CacheAction) => void
  private readonly subscribe: (listener: (mutation?: DesktopV3CacheMutation) => void) => () => void
  private readonly reconnect: (input: DesktopV3ReconnectInput) => Promise<SessionsReconnectResponse>
  private readonly hydrate: (input: DesktopV3HydrateInput) => Promise<SyncSnapshotResponse>
  private readonly readEventsPage: (input: { sessionId: string; afterSeq: number; limit?: number }) => Promise<DesktopV3SessionEventsPage>
  private readonly ensureSessionIdentity: () => Promise<unknown>
  private readonly bootstrap: (input?: { preferredSessionId?: string; restorePersisted?: boolean }) => Promise<unknown>
  private readonly transport: DesktopV3RealtimeTransport
  private readonly hydrateBySession = new Map<string, Promise<void>>()
  private readonly markedActiveRepairBySession = new Map<string, { runId: string; afterSeq: number }>()
  private readonly activeRepairByRun = new Map<string, Promise<void>>()
  private unsubscribeCache?: () => void
  private startPromise?: Promise<void>

  constructor(deps: DesktopV3RealtimeControllerDeps = {}) {
    this.getSnapshot = deps.getSnapshot ?? getDesktopV3CacheSnapshot
    this.dispatch = deps.dispatch ?? dispatchDesktopV3Cache
    this.subscribe = deps.subscribe ?? subscribeDesktopV3Cache
    this.reconnect = deps.reconnect ?? postDesktopV3Reconnect
    this.hydrate = deps.hydrate ?? postDesktopV3SyncHydrate
    this.readEventsPage = deps.readEventsPage ?? getDesktopV3SessionEventsPage
    this.ensureSessionIdentity = deps.ensureSession ?? ensureDesktopSession
    this.bootstrap = deps.bootstrap ?? (async (input) => {
      await bootstrapDesktopV3Sidebar({ preferredSessionId: input?.preferredSessionId })
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
        if (status === 'open') {
          queueMicrotask(() => this.startMarkedActiveRunRepairs())
        }
      },
      onRehydrateRequested: async (_reason, frame) => {
        if ((frame as { bootstrap_required?: boolean } | null)?.bootstrap_required) {
          await this.bootstrap({
            preferredSessionId: this.getSnapshot().selectedSessionId,
            restorePersisted: false,
          })
        }
        const reconnect = await this.reconnect(
          buildDesktopV3ReconnectInput(this.getSnapshot(), DESKTOP_V3_CLIENT_ID),
        )
        this.dispatch({ type: 'reconnect.applySnapshot', snapshot: reconnect })
        const resume = this.buildResume(reconnect, this.getSnapshot().selectedSessionId)
        this.dispatch({
          type: 'realtime.storeResume',
          streamPath: reconnect.realtime?.stream_path ?? '/v3/realtime/stream',
          resume,
        })
        this.markActiveRunsFromReconnect(reconnect)
        return {
          endpointCursor: resume.endpoint_cursor,
          snapshotEndpointCursor: reconnect.snapshot_endpoint_cursor,
          subscriptions: resume.subscriptions,
          worksets: normalizeTransportWorksets(resume.worksets),
        }
      },
    })
  }

  start(preferredSessionId?: string): Promise<void> {
    if (this.startPromise) return this.startPromise
    this.startPromise = this.startUncached(preferredSessionId)
    return this.startPromise
  }

  stop(reason = 'Desktop V3 realtime controller stopped'): void {
    this.unsubscribeCache?.()
    this.unsubscribeCache = undefined
    this.startPromise = undefined
    this.hydrateBySession.clear()
    this.markedActiveRepairBySession.clear()
    this.activeRepairByRun.clear()
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

  private async startUncached(preferredSessionId?: string): Promise<void> {
    await this.ensureSessionIdentity()

    const reconnect = await this.reconnect(
      buildDesktopV3ReconnectInput(this.getSnapshot(), DESKTOP_V3_CLIENT_ID),
    )
    if (!reconnect.realtime?.resume) {
      throw new Error('Desktop V3 reconnect response is missing realtime.resume')
    }

    this.dispatch({ type: 'reconnect.applySnapshot', snapshot: reconnect })

    const selectedSessionId = preferredSessionId?.trim() || this.getSnapshot().selectedSessionId
    const resume = this.buildResume(reconnect, selectedSessionId)

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
    await this.transport.start()

    if (selectedSessionId) void this.hydrateSessionOnce(selectedSessionId)
  }

  private buildResume(reconnect: SessionsReconnectResponse, preferredSessionId?: string): NonNullable<SessionsReconnectResponse['realtime']>['resume'] {
    if (!reconnect.realtime?.resume) {
      throw new Error('Desktop V3 reconnect response is missing realtime.resume')
    }
    const resume = structuredClone(reconnect.realtime.resume)
    const selectedSessionId = preferredSessionId?.trim()
    if (!selectedSessionId) return resume

    const subscriptions = [...(resume.subscriptions ?? [])]
    if (!subscriptions.some((subscription) => subscription.session_id === selectedSessionId)) {
      subscriptions.push({
        session_id: selectedSessionId,
        subscription_id: `${DESKTOP_V3_CLIENT_ID}:session:${selectedSessionId}`,
        endpoint_cursor: reconnect.snapshot_endpoint_cursor,
      })
    }
    resume.subscriptions = subscriptions
    return resume
  }

  private handleFrame(frame: RealtimeMessage): void {
    const sessionId = frame.session_id?.trim() || frame.event?.session_id?.trim() || ''
    const deferLiveOverlay = frame.kind === 'event'
      && Boolean(sessionId && this.markedActiveRepairBySession.has(sessionId))

    for (const action of realtimeFrameToActions(frame)) {
      this.dispatch(
        action.type === 'realtime.applyEvent' && deferLiveOverlay
          ? { ...action, deferLiveOverlay: true }
          : action,
      )
    }

    if (frame.kind === 'workset.session.discovered' && sessionId) {
      void this.hydrateSessionOnce(sessionId)
    }
  }

  private async ensureSession(sessionId: string): Promise<void> {
    const normalized = sessionId.trim()
    if (!normalized) return
    this.ensureSessionSubscription(normalized)
    await this.hydrateSessionOnce(normalized)
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
    for (const sessionId of raw.session_order ?? []) {
      const explicit = raw.current_run_intent_by_session?.[sessionId]
      const intent = explicit ?? deriveCurrentRunIntent(raw.run_intents_by_session?.[sessionId] ?? [])
      if (!intent) continue

      const status = intent.status.trim().toLowerCase()
      if (!ACTIVE_INTENT_STATUSES.has(status)) continue
      this.markedActiveRepairBySession.set(sessionId, {
        runId: intent.run_id,
        afterSeq: intent.event_seq,
      })
    }
  }

  private startMarkedActiveRunRepairs(): void {
    for (const [sessionId, marked] of this.markedActiveRepairBySession) {
      const key = `${sessionId}:${marked.runId}`
      if (this.activeRepairByRun.has(key)) continue

      const pending = this.repairActiveRun(
        sessionId,
        marked.runId,
        marked.afterSeq,
      ).catch((error) => {
        this.dispatch({
          type: 'liveRun.rebuildFromEvents',
          sessionId,
          runId: marked.runId,
          afterSeq: marked.afterSeq,
        })
        console.error('[desktop-v3] active-run repair failed', error)
      }).finally(() => {
        this.activeRepairByRun.delete(key)
        const current = this.markedActiveRepairBySession.get(sessionId)
        if (current?.runId === marked.runId) {
          this.markedActiveRepairBySession.delete(sessionId)
        }
      })

      this.activeRepairByRun.set(key, pending)
    }
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
        limit: 500,
      })

      const ordered = [...page.events].sort((a, b) => a.seq - b.seq)
      for (const event of ordered) {
        this.dispatch({
          type: 'realtime.applyEvent',
          deferLiveOverlay: true,
          event: {
            source: 'sync-stream',
            sessionId,
            eventType: event.event_type,
            sessionEvent: event,
            projection: page.projection,
            payload: decodeSessionEventPayload(event),
          },
        })
      }

      const latestCurrent = this.getSnapshot().currentRunIntentBySession[sessionId]
      if (!latestCurrent || latestCurrent.run_id !== expectedRunId) return

      const lastReturnedSeq = ordered.length > 0 ? ordered[ordered.length - 1].seq : cursor
      const targetSeq = projectionSeq(page.projection)
      if (lastReturnedSeq <= cursor || lastReturnedSeq >= targetSeq) break
      cursor = lastReturnedSeq
    }

    this.dispatch({
      type: 'liveRun.rebuildFromEvents',
      sessionId,
      runId: expectedRunId,
      afterSeq: runStartSeq,
    })
  }
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
  preferredSessionId?: string
} = {}): DesktopV3RealtimeLease {
  desktopV3RealtimeRetainCount += 1
  const generation = desktopV3RealtimeGeneration

  if (!retainedDesktopV3RealtimeController) {
    const controller = new DesktopV3RealtimeControllerRuntime()
    const ready = controller.start(input.preferredSessionId).catch((error) => {
      if (desktopV3RealtimeGeneration === generation) {
        retainedDesktopV3RealtimeController = undefined
        desktopV3RealtimeRetainCount = 0
      }
      throw error
    })
    retainedDesktopV3RealtimeController = { controller, ready }
  }

  const retained = retainedDesktopV3RealtimeController
  return {
    ready: retained.ready,
    release: () => {
      if (desktopV3RealtimeRetainCount <= 0) return
      desktopV3RealtimeRetainCount -= 1
      if (desktopV3RealtimeRetainCount === 0) {
        desktopV3RealtimeGeneration += 1
        retained.controller.stop('Desktop V3 realtime lease released')
        if (retainedDesktopV3RealtimeController === retained) {
          retainedDesktopV3RealtimeController = undefined
        }
      }
    },
  }
}

export async function requireDesktopV3RealtimeControllerReady(): Promise<DesktopV3RealtimeController> {
  const retained = retainedDesktopV3RealtimeController
  if (!retained || desktopV3RealtimeRetainCount <= 0) {
    throw new Error('Desktop V3 realtime controller is not retained by the desktop page')
  }
  await retained.ready
  return retained.controller
}

export function resetDesktopV3RealtimeControllerForTests(): void {
  retainedDesktopV3RealtimeController?.controller.stop('Desktop V3 realtime controller reset')
  retainedDesktopV3RealtimeController = undefined
  desktopV3RealtimeRetainCount = 0
  desktopV3RealtimeGeneration += 1
}

let retainedDesktopV3RealtimeController: {
  controller: DesktopV3RealtimeControllerRuntime
  ready: Promise<void>
} | undefined
let desktopV3RealtimeRetainCount = 0
let desktopV3RealtimeGeneration = 0

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
