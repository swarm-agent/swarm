import {
  SESSION_V3_REALTIME_PROTOCOL,
  SESSION_V3_REALTIME_PROTOCOL_VERSION,
  SESSION_V3_REALTIME_RESUME_KIND,
  SESSION_V3_REALTIME_LIVE_PATCH_CAPABILITY,
  type SessionV3RealtimeFrameWire,
  type SessionV3RealtimeLivePatchWire,
  type SessionV3RealtimeResumeWire,
  type SessionV3RealtimeSubscribeWire,
  type SessionV3RealtimeSubscriptionRequestWire,
  type SessionV3RealtimeUnsubscribeWire,
  type SessionV3RealtimeWorksetSubscriptionRequestWire,
  type SessionV3SyncSnapshot,
} from './types'
import { openDesktopV3RealtimeTransportSocket } from '../realtime/client'

const REOPEN_BASE_DELAY_MS = 1_500
const REOPEN_MAX_DELAY_MS = 15_000
const REOPEN_JITTER_RATIO = 0.2
const LIVENESS_TIMEOUT_MS = 45_000
const DURABLE_QUEUE_MAX_FRAMES = 512
const DURABLE_QUEUE_MAX_BYTES = 2 * 1024 * 1024
export const SESSION_CONNECT_ACK_TIMEOUT_MS = 30_000

export type DesktopV3RealtimeTransportSocketState = 'none' | 'connecting' | 'open' | 'closing' | 'closed'
export type DesktopV3RealtimeTransportStatus = 'stopped' | 'connecting' | 'open' | 'reopening' | 'rehydrating' | 'stale' | 'closed' | 'error'

export interface DesktopV3RealtimeTransportMeta {
  generation: number
  at: number
}

export interface DesktopV3RealtimeTransportStatusEvent extends DesktopV3RealtimeTransportMeta {
  status: DesktopV3RealtimeTransportStatus
  reason: string
}

export interface DesktopV3RealtimeTransportFrameEvent extends DesktopV3RealtimeTransportMeta {
  frame: SessionV3RealtimeFrameWire
  kind: string
  sessionId: string
  endpointCursor: string
}

export interface DesktopV3RealtimeLivePatchEvent extends DesktopV3RealtimeTransportMeta {
  patch: SessionV3RealtimeLivePatchWire
}

export interface DesktopV3RealtimeTransportCursorEvent extends DesktopV3RealtimeTransportMeta {
  endpointCursor: string
  previousEndpointCursor: string
  source: 'frame' | 'resume' | 'snapshot' | 'manual'
}

export interface DesktopV3RealtimeTransportOpenSocketOptions {
  endpointCursor: string
}

export type DesktopV3RealtimeTransportOpenSocket = (options: DesktopV3RealtimeTransportOpenSocketOptions) => Promise<WebSocket> | WebSocket

export interface DesktopV3RealtimeTransportRehydrateResult {
  endpointCursor?: string | null
  snapshotEndpointCursor?: string | null
  subscriptions?: SessionV3RealtimeSubscriptionRequestWire[]
  worksets?: SessionV3RealtimeWorksetSubscriptionRequestWire[]
}

export interface DesktopV3RealtimeTransportOptions {
  getEndpointCursor?: () => string | null | undefined
  openSocket?: DesktopV3RealtimeTransportOpenSocket
  onStatus?: (event: DesktopV3RealtimeTransportStatusEvent) => void
  onFrame?: (event: DesktopV3RealtimeTransportFrameEvent) => Promise<void> | void
  onLivePatch?: (event: DesktopV3RealtimeLivePatchEvent) => void
  onCursor?: (event: DesktopV3RealtimeTransportCursorEvent) => void
  onResumeSent?: (resume: SessionV3RealtimeResumeWire, meta: DesktopV3RealtimeTransportMeta) => void
  onRehydrateRequested?: (reason: string, frame: SessionV3RealtimeFrameWire | null, meta: DesktopV3RealtimeTransportMeta) => Promise<DesktopV3RealtimeTransportRehydrateResult | void> | DesktopV3RealtimeTransportRehydrateResult | void
  now?: () => number
  reopenBaseDelayMs?: number
  reopenMaxDelayMs?: number
  livenessTimeoutMs?: number
  livePatchEnabled?: boolean
}

export interface DesktopV3RealtimeTransportSessionRegistryEntry extends SessionV3RealtimeSubscriptionRequestWire {
  autoDiscovered: boolean
  updatedAt: number
}

export interface DesktopV3RealtimeTransportWorksetRegistryEntry extends SessionV3RealtimeWorksetSubscriptionRequestWire {
  updatedAt: number
}

export interface DesktopV3RealtimeTransportDiagnostics {
  desired: boolean
  status: DesktopV3RealtimeTransportStatus
  socketState: DesktopV3RealtimeTransportSocketState
  generation: number
  streamPath: '/v3/realtime/stream'
  endpointCursorPresent: boolean
  reopenAttempt: number
  reopenTimerActive: boolean
  rehydrateInFlight: boolean
  durableQueueFrames: number
  durableQueueBytes: number
  lastActivityAt: number
  sessionSubscriptionCount: number
  worksetSubscriptionCount: number
  sessions: DesktopV3RealtimeTransportSessionRegistryEntry[]
  worksets: DesktopV3RealtimeTransportWorksetRegistryEntry[]
}

type SessionRegistryEntry = DesktopV3RealtimeTransportSessionRegistryEntry

type WorksetRegistryEntry = DesktopV3RealtimeTransportWorksetRegistryEntry

interface Deferred<T> {
  promise: Promise<T>
  resolve: (value: T | PromiseLike<T>) => void
  reject: (reason?: unknown) => void
}

interface PendingSessionSubscription {
  subscriptionId: string
  deferred: Deferred<void>
  sentGeneration?: number
  timeoutId?: number
}

export function buildDesktopV3RealtimeResume(input: {
  endpointCursor: string
  subscriptions?: SessionV3RealtimeSubscriptionRequestWire[]
  worksets?: SessionV3RealtimeWorksetSubscriptionRequestWire[]
  capabilities?: string[]
}): SessionV3RealtimeResumeWire {
  const endpointCursor = normalizeString(input.endpointCursor)
  if (!endpointCursor) {
    throw new Error('Desktop V3 realtime resume requires endpoint_cursor.')
  }
  const capabilities = Array.isArray(input.capabilities)
    ? input.capabilities.map((capability) => normalizeString(capability)).filter(Boolean)
    : []
  return {
    protocol: SESSION_V3_REALTIME_PROTOCOL,
    protocol_version: SESSION_V3_REALTIME_PROTOCOL_VERSION,
    kind: SESSION_V3_REALTIME_RESUME_KIND,
    endpoint_cursor: endpointCursor,
    subscriptions: normalizeSessionSubscriptions(input.subscriptions ?? [], endpointCursor),
    worksets: normalizeWorksetSubscriptions(input.worksets ?? []),
    ...(capabilities.length > 0 ? { capabilities } : {}),
  }
}

function buildSessionSubscribeFrame(subscription: SessionV3RealtimeSubscriptionRequestWire): SessionV3RealtimeSubscribeWire {
  const endpointCursor = normalizeString(subscription.endpoint_cursor)
  if (!endpointCursor) {
    throw new Error('Desktop V3 realtime subscribe.session requires endpoint_cursor.')
  }
  return {
    protocol: SESSION_V3_REALTIME_PROTOCOL,
    protocol_version: SESSION_V3_REALTIME_PROTOCOL_VERSION,
    kind: 'subscribe.session',
    session_id: subscription.session_id,
    subscription_id: subscription.subscription_id,
    endpoint_cursor: endpointCursor,
  }
}

function buildSessionUnsubscribeFrame(subscription: SessionV3RealtimeSubscriptionRequestWire): SessionV3RealtimeUnsubscribeWire {
  return {
    protocol: SESSION_V3_REALTIME_PROTOCOL,
    protocol_version: SESSION_V3_REALTIME_PROTOCOL_VERSION,
    kind: 'unsubscribe.session',
    session_id: subscription.session_id,
    subscription_id: subscription.subscription_id,
  }
}

export class DesktopV3RealtimeTransport {
  private readonly sessions = new Map<string, SessionRegistryEntry>()
  private readonly worksets = new Map<string, WorksetRegistryEntry>()
  private readonly pendingSessionSubscriptions = new Map<string, PendingSessionSubscription>()
  private readonly readySessionSubscriptions = new Map<string, { subscriptionId: string; generation: number }>()
  private socket: WebSocket | null = null
  private connecting: Promise<void> | null = null
  private reopenTimer: number | null = null
  private livenessTimer: number | null = null
  private rehydrateInFlight: Promise<void> | null = null
  private durableMessageQueue: Array<{
    socket: WebSocket
    frame: SessionV3RealtimeFrameWire
    bytes: number
    generation: number
  }> = []
  private durableMessageQueueBytes = 0
  private drainingDurableMessages = false
  private endpointCursor = ''
  private desired = false
  private generation = 0
  private reopenAttempt = 0
  private lastActivityAt = 0
  private status: DesktopV3RealtimeTransportStatus = 'stopped'

  constructor(private readonly options: DesktopV3RealtimeTransportOptions = {}) {
    this.endpointCursor = normalizeString(options.getEndpointCursor?.())
  }

  start(): Promise<void> {
    this.desired = true
    return this.connect()
  }

  stop(reason = 'transport stopped'): void {
    this.desired = false
    this.clearScheduledReopen()
    this.clearLiveness()
    this.closeOwnedSocket()
    this.rejectAllPendingSessionSubscriptions(new Error(reason))
    this.reopenAttempt = 0
    this.lastActivityAt = 0
    this.emitStatus('stopped', reason)
  }

  dispose(): void {
    this.stop('transport disposed')
    this.sessions.clear()
    this.worksets.clear()
    this.readySessionSubscriptions.clear()
  }

  setEndpointCursor(endpointCursor: string | null | undefined, source: DesktopV3RealtimeTransportCursorEvent['source'] = 'manual'): void {
    this.advanceEndpointCursor(endpointCursor, source)
  }

  subscribeSession(subscription: SessionV3RealtimeSubscriptionRequestWire): Promise<void> {
    const entry = normalizeSessionSubscription(subscription, this.currentEndpointCursor(), true)
    if (!entry) return Promise.resolve()
    if (!entry.endpoint_cursor) {
      return Promise.reject(new Error('Desktop V3 realtime subscribe.session requires endpoint_cursor.'))
    }

    const ready = this.readySessionSubscriptions.get(entry.session_id)
    if (
      ready
      && ready.subscriptionId === entry.subscription_id
      && ready.generation === this.generation
      && this.socket?.readyState === WebSocket.OPEN
    ) {
      return Promise.resolve()
    }

    this.sessions.set(entry.session_id, {
      ...entry,
      autoDiscovered: false,
      updatedAt: this.now(),
    })

    let pending = this.pendingSessionSubscriptions.get(entry.session_id)
    if (pending && pending.subscriptionId !== entry.subscription_id) {
      this.rejectPendingSessionSubscription(
        entry.session_id,
        new Error('Desktop V3 session subscription was replaced before acknowledgement.'),
      )
      pending = undefined
    }
    if (!pending) {
      pending = this.createPendingSessionSubscription(entry.session_id, entry.subscription_id)
      this.pendingSessionSubscriptions.set(entry.session_id, pending)
    }

    if (this.socket?.readyState === WebSocket.OPEN && pending.sentGeneration !== this.generation) {
      try {
        this.socket.send(JSON.stringify(buildSessionSubscribeFrame(entry)))
        pending.sentGeneration = this.generation
      } catch (error) {
        this.rejectPendingSessionSubscription(entry.session_id, error)
      }
    } else if (this.desired) {
      void this.connect().catch((error) => this.emitStatus('error', errorMessage(error, 'session subscription connect failed')))
    }

    return pending.deferred.promise
  }

  unsubscribeSession(sessionId: string): void {
    const normalizedSessionId = normalizeString(sessionId)
    if (!normalizedSessionId) return
    const existing = this.sessions.get(normalizedSessionId)
    this.sessions.delete(normalizedSessionId)
    this.readySessionSubscriptions.delete(normalizedSessionId)
    this.rejectPendingSessionSubscription(
      normalizedSessionId,
      new Error('Desktop V3 session subscription was unsubscribed before acknowledgement.'),
    )
    if (existing && this.socket?.readyState === WebSocket.OPEN) {
      this.socket.send(JSON.stringify(buildSessionUnsubscribeFrame(existing)))
    }
  }

  setSessions(subscriptions: SessionV3RealtimeSubscriptionRequestWire[], options: { replace?: boolean } = {}): void {
    if (options.replace) {
      this.assertRegistryReplacementAllowed()
      this.sessions.clear()
    }
    const endpointCursor = this.currentEndpointCursor()
    for (const subscription of normalizeSessionSubscriptions(subscriptions, endpointCursor)) {
      this.sessions.set(subscription.session_id, {
        ...subscription,
        autoDiscovered: this.sessions.get(subscription.session_id)?.autoDiscovered ?? false,
        updatedAt: this.now(),
      })
    }
    this.syncOpenSocketResume('sessions updated')
  }

  registerWorkset(workset: SessionV3RealtimeWorksetSubscriptionRequestWire): void {
    const entry = normalizeWorksetSubscription(workset)
    if (!entry) return
    this.worksets.set(entry.workset_id, { ...entry, updatedAt: this.now() })
    this.syncOpenSocketResume('workset registered')
  }

  unregisterWorkset(worksetId: string): void {
    const normalizedWorksetId = normalizeString(worksetId)
    if (!normalizedWorksetId) return
    this.worksets.delete(normalizedWorksetId)
    this.syncOpenSocketResume('workset unregistered')
  }

  setWorksets(worksets: SessionV3RealtimeWorksetSubscriptionRequestWire[], options: { replace?: boolean } = {}): void {
    if (options.replace) {
      this.assertRegistryReplacementAllowed()
      this.worksets.clear()
    }
    for (const workset of normalizeWorksetSubscriptions(worksets)) {
      this.worksets.set(workset.workset_id, { ...workset, updatedAt: this.now() })
    }
    this.syncOpenSocketResume('worksets updated')
  }

  applySyncSnapshot(
    result: SessionV3SyncSnapshot,
    resumeState: {
      endpointCursor?: string | null
      subscriptions?: SessionV3RealtimeSubscriptionRequestWire[]
      worksets?: SessionV3RealtimeWorksetSubscriptionRequestWire[]
    } = {},
  ): void {
    this.advanceEndpointCursor((resumeState.endpointCursor ?? result.endpointCursor) || result.snapshot.snapshotEndpointCursor, 'snapshot')
    this.replaceRegistries({
      subscriptions: resumeState.subscriptions ?? [],
      worksets: resumeState.worksets ?? [],
      replace: true,
      subscriptionFallbackEndpointCursor: result.endpointCursor || result.snapshot.snapshotEndpointCursor,
    })
    this.syncOpenSocketResume('sync snapshot applied')
  }

  resetForSyncSnapshot(_reason = 'sync snapshot reset'): void {
    this.desired = false
    this.clearScheduledReopen()
    this.clearLiveness()
    this.closeOwnedSocket()
    this.reopenAttempt = 0
  }

  resetFromSyncSnapshot(result: SessionV3SyncSnapshot): void {
    this.resetForSyncSnapshot('sync snapshot applied')
    this.applySyncSnapshot(result)
  }

  reopen(reason = 'manual reopen'): void {
    if (!this.desired) {
      this.desired = true
    }
    this.forceReopen(reason)
  }

  reopenFromDurableCursor(reason: string): void {
    const durableCursor = normalizeString(this.options.getEndpointCursor?.())
    if (durableCursor) {
      this.endpointCursor = durableCursor
    }
    this.clearDurableMessageQueueForGeneration(this.generation)
    if (!this.desired) {
      this.desired = true
    }
    this.clearScheduledReopen()
    this.clearLiveness()
    this.closeOwnedSocket()
    this.emitStatus('reopening', reason)
    void this.connect().catch((error) => this.emitStatus('error', errorMessage(error, reason)))
  }

  diagnostics(): DesktopV3RealtimeTransportDiagnostics {
    return {
      desired: this.desired,
      status: this.status,
      socketState: socketStateName(this.socket),
      generation: this.generation,
      streamPath: '/v3/realtime/stream',
      endpointCursorPresent: this.currentEndpointCursor() !== '',
      reopenAttempt: this.reopenAttempt,
      reopenTimerActive: this.reopenTimer !== null,
      rehydrateInFlight: this.rehydrateInFlight !== null,
      durableQueueFrames: this.durableMessageQueue.length,
      durableQueueBytes: this.durableMessageQueueBytes,
      lastActivityAt: this.lastActivityAt,
      sessionSubscriptionCount: this.sessions.size,
      worksetSubscriptionCount: this.worksets.size,
      sessions: Array.from(this.sessions.values()).map((session) => ({ ...session })),
      worksets: Array.from(this.worksets.values()).map((workset) => ({ ...workset })),
    }
  }

  private async connect(): Promise<void> {
    if (!this.desired) return
    if (this.socket && (this.socket.readyState === WebSocket.OPEN || this.socket.readyState === WebSocket.CONNECTING)) return
    if (this.connecting) return this.connecting
    const endpointCursor = this.currentEndpointCursor()
    if (!endpointCursor) {
      await this.requestRehydrate('missing endpoint_cursor', null)
      return
    }
    this.connecting = this.openConnection(endpointCursor)
    try {
      await this.connecting
    } finally {
      this.connecting = null
    }
  }

  private async openConnection(endpointCursor: string): Promise<void> {
    this.clearScheduledReopen()
    this.generation += 1
    this.readySessionSubscriptions.clear()
    const generation = this.generation
    this.emitStatus('connecting', 'opening V3 realtime transport')
    try {
      const socket = await (this.options.openSocket ?? openDesktopV3RealtimeTransportSocket)({ endpointCursor })
      if (generation !== this.generation || !this.desired) {
        socket.close()
        return
      }
      this.socket = socket
      this.attachSocket(socket, generation)
      this.noteActivity(generation)
    } catch (error) {
      if (generation !== this.generation) return
      this.emitStatus('error', errorMessage(error, 'failed to open V3 realtime transport'))
      this.scheduleReopen('socket open failed')
    }
  }

  private attachSocket(socket: WebSocket, generation: number): void {
    const handleOpen = () => {
      if (generation !== this.generation || this.socket !== socket || !this.desired) {
        socket.close()
        return
      }
      this.reopenAttempt = 0
      this.clearScheduledReopen()
      this.noteActivity(generation)
      this.emitStatus('open', 'V3 realtime transport open')
      this.sendResume(socket)
    }

    socket.addEventListener('open', handleOpen)
    if (socket.readyState === WebSocket.OPEN) {
      queueMicrotask(handleOpen)
    }

    socket.addEventListener('message', (event) => {
      if (generation !== this.generation || this.socket !== socket) return
      this.noteActivity(generation)
      this.acceptSocketMessage(socket, event.data, generation)
    })

    socket.addEventListener('error', () => {
      if (generation !== this.generation || this.socket !== socket || !this.desired) return
      this.emitStatus('error', 'V3 realtime socket error')
      this.forceReopen('socket error')
    })

    socket.addEventListener('close', () => {
      this.clearLiveness()
      if (generation !== this.generation || this.socket !== socket) return
      this.socket = null
      this.readySessionSubscriptions.clear()
      if (!this.desired) {
        this.emitStatus('closed', 'V3 realtime transport closed')
        return
      }
      this.emitStatus('reopening', 'V3 realtime socket closed')
      this.scheduleReopen('socket closed')
    })
  }

  private acceptSocketMessage(socket: WebSocket, raw: unknown, generation: number): void {
    const bytes = realtimeFrameByteLength(raw)
    const frame = parseRealtimeFrame(raw)
    if (!frame || generation !== this.generation || this.socket !== socket) return

    if (frameKind(frame) === 'live.patch') {
      try {
        const patch = requireSessionV3RealtimeLivePatch(frame)
        if (this.options.livePatchEnabled === true) {
          this.options.onLivePatch?.({ patch, ...this.meta() })
        }
      } catch (error) {
        this.emitStatus('error', errorMessage(error, 'V3 realtime live patch validation failed'))
        void this.requestRehydrate('V3 realtime live patch validation failed', frame)
      }
      return
    }

    this.enqueueDurableMessage(socket, frame, bytes, generation)
  }

  private enqueueDurableMessage(socket: WebSocket, frame: SessionV3RealtimeFrameWire, bytes: number, generation: number): void {
    this.durableMessageQueue.push({ socket, frame, bytes, generation })
    this.durableMessageQueueBytes += bytes
    if (this.durableMessageQueue.length > DURABLE_QUEUE_MAX_FRAMES || this.durableMessageQueueBytes > DURABLE_QUEUE_MAX_BYTES) {
      this.clearDurableMessageQueueForGeneration(generation)
      this.emitStatus('error', 'V3 realtime durable queue overflow')
      this.forceReopen('V3 realtime durable queue overflow')
      return
    }
    if (!this.drainingDurableMessages) {
      void this.drainDurableMessages()
    }
  }

  private async drainDurableMessages(): Promise<void> {
    if (this.drainingDurableMessages) return
    this.drainingDurableMessages = true
    try {
      while (this.durableMessageQueue.length > 0) {
        const next = this.durableMessageQueue.shift()
        if (!next) continue
        this.durableMessageQueueBytes = Math.max(0, this.durableMessageQueueBytes - next.bytes)
        if (next.generation !== this.generation || this.socket !== next.socket) continue
        try {
          await this.handleDurableMessage(next.frame, next.generation)
        } catch (error) {
          if (next.generation !== this.generation || this.socket !== next.socket) continue
          this.clearDurableMessageQueueForGeneration(next.generation)
          this.emitStatus('error', errorMessage(error, 'V3 realtime frame handling failed'))
          this.forceReopen('frame handling failed')
          break
        }
      }
    } finally {
      this.drainingDurableMessages = false
      if (this.durableMessageQueue.length > 0) {
        void this.drainDurableMessages()
      }
    }
  }

  private async handleDurableMessage(frame: SessionV3RealtimeFrameWire, generation: number): Promise<void> {
    if (generation !== this.generation) return

    const kind = frameKind(frame)
    const sessionId = frameSessionId(frame)
    const endpointCursor = frameEndpointCursor(frame)
    const committedCursorBeforeFrame = this.currentEndpointCursor()

    const frameResult = this.options.onFrame?.({
      frame,
      kind,
      sessionId,
      endpointCursor,
      ...this.meta(),
    })
    if (isPromiseLike(frameResult)) {
      await frameResult
    }

    this.noteWorksetSessionFrame(frame, committedCursorBeforeFrame)

    if (endpointCursor && frameAdvancesEndpointCursor(kind)) {
      this.advanceEndpointCursor(endpointCursor, 'frame')
    }

    this.acknowledgeSessionSubscriptionIfComplete(kind, frame, sessionId)

    if (kind === 'cursor.error') {
      await this.requestRehydrate(
        frame.reason || frame.error || frame.error_code || 'V3 realtime cursor error',
        frame,
      )
      return
    }
    if (kind === 'slow_consumer.reconnect_required') {
      await this.requestRehydrate(
        frame.reason || frame.error || 'V3 realtime slow consumer',
        frame,
      )
      return
    }
    if (kind === 'auth.denied') {
      const reason = frame.reason || frame.error || 'V3 realtime auth denied'
      if (this.rejectDeniedSessionSubscription(frame, reason)) return
      this.markStale(reason)
    }
  }

  private sendResume(socket: WebSocket): void {
    if (socket.readyState !== WebSocket.OPEN) return
    const resume = this.resumePayload()
    socket.send(JSON.stringify(resume))
    for (const subscription of resume.subscriptions ?? []) {
      const pending = this.pendingSessionSubscriptions.get(subscription.session_id)
      if (pending?.subscriptionId === subscription.subscription_id) {
        pending.sentGeneration = this.generation
      }
    }
    this.options.onResumeSent?.(resume, this.meta())
  }

  private replaceRegistries(input: {
    subscriptions: SessionV3RealtimeSubscriptionRequestWire[]
    worksets: SessionV3RealtimeWorksetSubscriptionRequestWire[]
    replace?: boolean
    subscriptionFallbackEndpointCursor?: string | null
  }): void {
    if (input.replace) {
      this.assertRegistryReplacementAllowed()
      this.sessions.clear()
      this.worksets.clear()
    }
    const endpointCursor = this.currentEndpointCursor()
    const subscriptionFallbackEndpointCursor = normalizeString(input.subscriptionFallbackEndpointCursor) || endpointCursor
    for (const subscription of normalizeSessionSubscriptions(input.subscriptions, subscriptionFallbackEndpointCursor)) {
      this.sessions.set(subscription.session_id, {
        ...subscription,
        autoDiscovered: this.sessions.get(subscription.session_id)?.autoDiscovered ?? false,
        updatedAt: this.now(),
      })
    }
    for (const workset of normalizeWorksetSubscriptions(input.worksets)) {
      this.worksets.set(workset.workset_id, { ...workset, updatedAt: this.now() })
    }
  }

  private resumePayload(): SessionV3RealtimeResumeWire {
    const endpointCursor = this.currentEndpointCursor()
    return buildDesktopV3RealtimeResume({
      endpointCursor,
      subscriptions: Array.from(this.sessions.values()),
      worksets: Array.from(this.worksets.values()),
      capabilities: this.options.livePatchEnabled === true ? [SESSION_V3_REALTIME_LIVE_PATCH_CAPABILITY] : undefined,
    })
  }

  private syncOpenSocketResume(reason: string): void {
    const socket = this.socket
    if (socket?.readyState === WebSocket.OPEN) {
      try {
        this.sendResume(socket)
      } catch (error) {
        this.emitStatus('error', errorMessage(error, reason))
        this.forceReopen(reason)
      }
      return
    }
    if (this.desired) {
      void this.connect().catch((error) => this.emitStatus('error', errorMessage(error, reason)))
    }
  }

  private assertRegistryReplacementAllowed(): void {
    if (this.socket?.readyState === WebSocket.OPEN) {
      throw new Error('Realtime registry replacement is only allowed before socket open or during recovery')
    }
  }

  private acknowledgeSessionSubscriptionIfComplete(
    kind: string,
    frame: SessionV3RealtimeFrameWire,
    sessionId: string,
  ): void {
    if (kind !== 'replay.complete') return
    const pending = this.pendingSessionSubscriptions.get(sessionId)
    if (!pending || pending.subscriptionId !== normalizeString(frame.subscription_id)) return
    this.readySessionSubscriptions.set(sessionId, {
      subscriptionId: pending.subscriptionId,
      generation: this.generation,
    })
    this.resolvePendingSessionSubscription(sessionId)
  }

  private createPendingSessionSubscription(sessionId: string, subscriptionId: string): PendingSessionSubscription {
    const deferred = createDeferred<void>()
    const pending: PendingSessionSubscription = {
      subscriptionId,
      deferred,
    }
    pending.timeoutId = window.setTimeout(() => {
      const current = this.pendingSessionSubscriptions.get(sessionId)
      if (current !== pending) return
      this.rejectPendingSessionSubscription(
        sessionId,
        new Error('Desktop V3 session subscription acknowledgement timed out.'),
      )
    }, SESSION_CONNECT_ACK_TIMEOUT_MS)
    return pending
  }

  private resolvePendingSessionSubscription(sessionId: string): void {
    const pending = this.pendingSessionSubscriptions.get(sessionId)
    if (!pending) return
    this.clearPendingSessionSubscriptionTimeout(pending)
    this.pendingSessionSubscriptions.delete(sessionId)
    pending.deferred.resolve()
  }

  private rejectPendingSessionSubscription(sessionId: string, reason: unknown): void {
    const pending = this.pendingSessionSubscriptions.get(sessionId)
    if (!pending) return
    this.clearPendingSessionSubscriptionTimeout(pending)
    this.pendingSessionSubscriptions.delete(sessionId)
    pending.deferred.reject(reason)
  }

  private rejectAllPendingSessionSubscriptions(reason: unknown): void {
    for (const sessionId of Array.from(this.pendingSessionSubscriptions.keys())) {
      this.rejectPendingSessionSubscription(sessionId, reason)
    }
  }

  private rejectDeniedSessionSubscription(frame: SessionV3RealtimeFrameWire, reason: string): boolean {
    const sessionId = frameSessionId(frame)
    const subscriptionId = normalizeString(frame.subscription_id)
    if (sessionId) {
      this.sessions.delete(sessionId)
      this.readySessionSubscriptions.delete(sessionId)
      const pending = this.pendingSessionSubscriptions.get(sessionId)
      if (pending && (!subscriptionId || pending.subscriptionId === subscriptionId)) {
        this.rejectPendingSessionSubscription(sessionId, new Error(reason))
      }
      return true
    }
    if (!subscriptionId) return false
    for (const [pendingSessionId, pending] of this.pendingSessionSubscriptions) {
      if (pending.subscriptionId === subscriptionId) {
        this.sessions.delete(pendingSessionId)
        this.readySessionSubscriptions.delete(pendingSessionId)
        this.rejectPendingSessionSubscription(pendingSessionId, new Error(reason))
        return true
      }
    }
    return false
  }

  private clearPendingSessionSubscriptionTimeout(pending: PendingSessionSubscription): void {
    if (pending.timeoutId !== undefined) {
      window.clearTimeout(pending.timeoutId)
      pending.timeoutId = undefined
    }
  }

  private noteWorksetSessionFrame(
    frame: SessionV3RealtimeFrameWire,
    committedCursorBeforeFrame: string,
  ): void {
    const kind = frameKind(frame)
    if (kind !== 'workset.session.discovered' && kind !== 'workset.session.removed') return

    const sessionId = frameSessionId(frame)
    if (!sessionId) return

    if (kind === 'workset.session.removed') {
      const existing = this.sessions.get(sessionId)
      if (existing?.autoDiscovered) {
        this.sessions.delete(sessionId)
      }
      return
    }

    if (!frame.auto_subscribed) return
    const subscriptionId = normalizeString(frame.subscription_id)
    if (!subscriptionId) return

    const existing = this.sessions.get(sessionId)
    if (existing && !existing.autoDiscovered) return

    this.sessions.set(sessionId, {
      session_id: sessionId,
      subscription_id: subscriptionId,
      endpoint_cursor: committedCursorBeforeFrame || undefined,
      autoDiscovered: true,
      updatedAt: this.now(),
    })
  }

  private advanceEndpointCursor(endpointCursor: string | null | undefined, source: DesktopV3RealtimeTransportCursorEvent['source']): void {
    const next = normalizeString(endpointCursor)
    if (!next || next === this.endpointCursor) return
    const previous = this.endpointCursor
    this.endpointCursor = next
    for (const [sessionId, session] of this.sessions) {
      this.sessions.set(sessionId, { ...session, endpoint_cursor: next })
    }
    this.options.onCursor?.({
      endpointCursor: next,
      previousEndpointCursor: previous,
      source,
      ...this.meta(),
    })
  }

  private currentEndpointCursor(): string {
    return this.endpointCursor || normalizeString(this.options.getEndpointCursor?.())
  }

  private async requestRehydrate(reason: string, frame: SessionV3RealtimeFrameWire | null): Promise<void> {
    if (this.rehydrateInFlight) return this.rehydrateInFlight
    this.clearScheduledReopen()
    this.clearLiveness()
    this.closeOwnedSocket()
    const rehydrate = this.options.onRehydrateRequested
    if (!rehydrate) {
      this.markStale(reason)
      return
    }
    this.emitStatus('rehydrating', reason)
    this.rehydrateInFlight = (async () => {
      try {
        const result = await rehydrate(reason, frame, this.meta())
        if (result) {
          this.applyRehydrateResult(result)
        }
        if (!this.currentEndpointCursor()) {
          throw new Error('V3 realtime rehydrate did not return endpoint_cursor')
        }
        if (this.desired) {
          await this.connect()
        }
      } catch (error) {
        this.markStale(errorMessage(error, reason))
      } finally {
        this.rehydrateInFlight = null
      }
    })()
    return this.rehydrateInFlight
  }

  private applyRehydrateResult(result: DesktopV3RealtimeTransportRehydrateResult): void {
    this.advanceEndpointCursor(result.endpointCursor, 'snapshot')
    this.replaceRegistries({
      subscriptions: normalizeRehydrateSubscriptions(result.subscriptions ?? [], result.snapshotEndpointCursor ?? result.endpointCursor),
      worksets: result.worksets ?? [],
      replace: true,
    })
    this.syncOpenSocketResume('rehydrate result applied')
  }

  private forceReopen(reason: string): void {
    if (!this.desired) return
    this.clearScheduledReopen()
    this.clearLiveness()
    this.closeOwnedSocket()
    this.emitStatus('reopening', reason)
    this.scheduleReopen(reason)
  }

  private scheduleReopen(reason: string): void {
    if (!this.desired || this.reopenTimer !== null || this.rehydrateInFlight) return
    const attempt = this.reopenAttempt
    const delay = reopenDelayMs(attempt, this.options.reopenBaseDelayMs, this.options.reopenMaxDelayMs)
    this.reopenAttempt += 1
    this.reopenTimer = window.setTimeout(() => {
      this.reopenTimer = null
      void this.connect()
    }, delay)
    this.emitStatus('reopening', reason)
  }

  private markStale(reason: string): void {
    this.clearScheduledReopen()
    this.clearLiveness()
    this.closeOwnedSocket()
    this.rejectAllPendingSessionSubscriptions(new Error(reason))
    this.desired = false
    this.emitStatus('stale', reason)
  }

  private closeOwnedSocket(): void {
    const socket = this.socket
    this.socket = null
    this.connecting = null
    this.generation += 1
    this.clearAllDurableMessages()
    this.readySessionSubscriptions.clear()
    if (socket && socket.readyState !== WebSocket.CLOSED && socket.readyState !== WebSocket.CLOSING) {
      socket.close()
    }
  }

  private noteActivity(generation: number): void {
    this.lastActivityAt = this.now()
    this.armLiveness(generation)
  }

  private armLiveness(generation: number): void {
    this.clearLiveness()
    if (!this.desired) return
    const timeout = this.options.livenessTimeoutMs ?? LIVENESS_TIMEOUT_MS
    this.livenessTimer = window.setTimeout(() => {
      if (generation !== this.generation || !this.desired) return
      void this.requestRehydrate('V3 realtime inactivity timeout', null)
    }, timeout)
  }

  private clearScheduledReopen(): void {
    if (this.reopenTimer !== null) {
      window.clearTimeout(this.reopenTimer)
      this.reopenTimer = null
    }
  }

  private clearLiveness(): void {
    if (this.livenessTimer !== null) {
      window.clearTimeout(this.livenessTimer)
      this.livenessTimer = null
    }
  }

  private clearDurableMessageQueueForGeneration(generation: number): void {
    let bytes = 0
    const retained: typeof this.durableMessageQueue = []
    for (const queued of this.durableMessageQueue) {
      if (queued.generation === generation) {
        bytes += queued.bytes
      } else {
        retained.push(queued)
      }
    }
    this.durableMessageQueue = retained
    this.durableMessageQueueBytes = Math.max(0, this.durableMessageQueueBytes - bytes)
  }

  private clearAllDurableMessages(): void {
    this.durableMessageQueue = []
    this.durableMessageQueueBytes = 0
  }

  debugSnapshotForTests(): { durableQueueFrames: number; durableQueueBytes: number } {
    return {
      durableQueueFrames: this.durableMessageQueue.length,
      durableQueueBytes: this.durableMessageQueueBytes,
    }
  }

  private emitStatus(status: DesktopV3RealtimeTransportStatus, reason: string): void {
    this.status = status
    this.options.onStatus?.({ status, reason, ...this.meta() })
  }

  private meta(): DesktopV3RealtimeTransportMeta {
    return { generation: this.generation, at: this.now() }
  }

  private now(): number {
    return this.options.now?.() ?? Date.now()
  }
}

function normalizeSessionSubscriptions(subscriptions: SessionV3RealtimeSubscriptionRequestWire[], fallbackEndpointCursor: string): SessionV3RealtimeSubscriptionRequestWire[] {
  return subscriptions
    .map((subscription) => normalizeSessionSubscription(subscription, fallbackEndpointCursor))
    .filter((subscription): subscription is SessionV3RealtimeSubscriptionRequestWire => Boolean(subscription))
}

function normalizeRehydrateSubscriptions(subscriptions: SessionV3RealtimeSubscriptionRequestWire[], fallbackEndpointCursor: string | null | undefined): SessionV3RealtimeSubscriptionRequestWire[] {
  const fallback = normalizeString(fallbackEndpointCursor)
  return subscriptions
    .map((subscription) => normalizeSessionSubscription(subscription, fallback, true))
    .filter((subscription): subscription is SessionV3RealtimeSubscriptionRequestWire => Boolean(subscription))
}

function normalizeSessionSubscription(subscription: SessionV3RealtimeSubscriptionRequestWire, fallbackEndpointCursor: string, preserveEndpointCursor = true): SessionV3RealtimeSubscriptionRequestWire | null {
  const sessionId = normalizeString(subscription.session_id)
  const subscriptionId = normalizeString(subscription.subscription_id)
  if (!sessionId || !subscriptionId) return null
  const endpointCursor = preserveEndpointCursor
    ? normalizeString(subscription.endpoint_cursor) || fallbackEndpointCursor
    : fallbackEndpointCursor
  return {
    session_id: sessionId,
    subscription_id: subscriptionId,
    endpoint_cursor: endpointCursor || undefined,
  }
}

function normalizeWorksetSubscriptions(worksets: SessionV3RealtimeWorksetSubscriptionRequestWire[]): SessionV3RealtimeWorksetSubscriptionRequestWire[] {
  return worksets
    .map((workset) => normalizeWorksetSubscription(workset))
    .filter((workset): workset is SessionV3RealtimeWorksetSubscriptionRequestWire => Boolean(workset))
}

function normalizeWorksetSubscription(workset: SessionV3RealtimeWorksetSubscriptionRequestWire): SessionV3RealtimeWorksetSubscriptionRequestWire | null {
  const worksetId = normalizeString(workset.workset_id)
  const subscriptionId = normalizeString(workset.subscription_id)
  const selectorKind = normalizeString(workset.selector?.kind)
  if (!worksetId || !subscriptionId || !selectorKind) return null
  return {
    workset_id: worksetId,
    subscription_id: subscriptionId,
    surface: normalizeString(workset.surface) || undefined,
    selector: {
      ...workset.selector,
      kind: selectorKind,
    },
    resources: Array.isArray(workset.resources) ? workset.resources.map((resource) => normalizeString(resource)).filter(Boolean) : undefined,
    auto_subscribe_sessions: Boolean(workset.auto_subscribe_sessions),
  }
}

export function requireSessionV3RealtimeLivePatch(
  frame: SessionV3RealtimeFrameWire,
): SessionV3RealtimeLivePatchWire {
  const patch = frame.live
  if (!patch || typeof patch !== 'object') {
    throw new Error('live.patch frame is missing live payload')
  }
  const sessionId = normalizeString(patch.session_id)
  const runId = normalizeString(patch.run_id)
  const streamId = normalizeString(patch.stream_id)
  const stepId = normalizeString(patch.step_id)
  if (!sessionId || !runId || !streamId || !stepId) {
    throw new Error('live.patch requires nonempty session, run, stream, and step identities')
  }
  if (frameSessionId(frame) !== sessionId) {
    throw new Error('live.patch top-level session_id must match payload session_id')
  }
  if (patch.stream_kind !== 'assistant_text' || patch.operation !== 'append') {
    throw new Error('live.patch uses unsupported stream kind or operation')
  }
  if (!isPositiveInteger(patch.step)) {
    throw new Error('live.patch step must be positive')
  }
  if (!isPositiveInteger(patch.live_seq_start) || !isPositiveInteger(patch.live_seq_end) || patch.live_seq_end < patch.live_seq_start) {
    throw new Error('live.patch sequence range is invalid')
  }
  if (!Number.isSafeInteger(patch.offset_start) || !Number.isSafeInteger(patch.offset_end) || patch.offset_start < 0 || patch.offset_end <= patch.offset_start) {
    throw new Error('live.patch offset range is invalid')
  }
  if (typeof patch.text !== 'string') {
    throw new Error('live.patch text must be a string')
  }
  if (utf8ByteLength(patch.text) !== patch.offset_end - patch.offset_start) {
    throw new Error('live.patch text byte length must match offset range')
  }
  if (typeof patch.recorded_at !== 'number' || !Number.isFinite(patch.recorded_at)) {
    throw new Error('live.patch recorded_at must be numeric')
  }
  return {
    ...patch,
    session_id: sessionId,
    run_id: runId,
    stream_id: streamId,
    step_id: stepId,
  }
}

function realtimeFrameByteLength(raw: unknown): number {
  if (typeof raw === 'string') return utf8ByteLength(raw)
  return utf8ByteLength(JSON.stringify(raw))
}

function utf8ByteLength(text: string): number {
  return new TextEncoder().encode(text).byteLength
}

function isPositiveInteger(value: unknown): value is number {
  return Number.isSafeInteger(value) && typeof value === 'number' && value > 0
}

function parseRealtimeFrame(raw: unknown): SessionV3RealtimeFrameWire | null {
  try {
    const value = typeof raw === 'string' ? JSON.parse(raw) : typeof MessageEvent !== 'undefined' && raw instanceof MessageEvent ? JSON.parse(String(raw.data)) : JSON.parse(String(raw))
    if (!value || typeof value !== 'object') return null
    const frame = value as SessionV3RealtimeFrameWire
    const kind = frameKind(frame)
    if (!kind) return null
    return { ...frame, kind, type: kind }
  } catch (error) {
    console.error('[desktop-session-v3-transport] failed to parse realtime frame', error)
    return null
  }
}

function frameKind(frame: SessionV3RealtimeFrameWire): string {
  return normalizeString(frame.kind ?? frame.type)
}

function frameSessionId(frame: SessionV3RealtimeFrameWire): string {
  return normalizeString(frame.session_id) || normalizeString(frame.event?.session_id)
}

function frameEndpointCursor(frame: SessionV3RealtimeFrameWire): string {
  return normalizeString(frame.endpoint_cursor)
}

function frameAdvancesEndpointCursor(kind: string): boolean {
  return kind === 'hello'
    || kind === 'event'
    || kind === 'replay.complete'
    || kind === 'endpoint.watermark'
    || kind === 'keepalive'
}

function reopenDelayMs(attempt: number, baseOverride?: number, maxOverride?: number): number {
  const base = positiveNumber(baseOverride) ?? REOPEN_BASE_DELAY_MS
  const max = positiveNumber(maxOverride) ?? REOPEN_MAX_DELAY_MS
  const baseDelay = Math.min(max, base * (2 ** Math.max(0, attempt)))
  const jitterWindow = Math.max(1, Math.floor(baseDelay * REOPEN_JITTER_RATIO))
  const jitterOffset = Math.floor((Math.random() * (jitterWindow * 2 + 1)) - jitterWindow)
  return Math.max(base, baseDelay + jitterOffset)
}

function createDeferred<T>(): Deferred<T> {
  let resolve!: Deferred<T>['resolve']
  let reject!: Deferred<T>['reject']
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise
    reject = rejectPromise
  })
  return { promise, resolve, reject }
}

function isPromiseLike(value: unknown): value is PromiseLike<unknown> {
  return typeof (value as { then?: unknown } | undefined)?.then === 'function'
}

function socketStateName(socket: WebSocket | null): DesktopV3RealtimeTransportSocketState {
  switch (socket?.readyState) {
    case WebSocket.CONNECTING:
      return 'connecting'
    case WebSocket.OPEN:
      return 'open'
    case WebSocket.CLOSING:
      return 'closing'
    case WebSocket.CLOSED:
      return 'closed'
    default:
      return 'none'
  }
}

function normalizeString(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}

function positiveNumber(value: unknown): number | null {
  return typeof value === 'number' && Number.isFinite(value) && value > 0 ? value : null
}

function errorMessage(error: unknown, fallback: string): string {
  return error instanceof Error && error.message.trim() ? error.message : fallback
}
