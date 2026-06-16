import { openDesktopV3RealtimeStream } from './client'
import type { RunStreamEventMessage } from '../state/run-stream-controller'

const RECONNECT_BASE_DELAY_MS = 1500
const RECONNECT_MAX_DELAY_MS = 15_000
const RECONNECT_JITTER_RATIO = 0.2
const LIVENESS_TIMEOUT_MS = 45_000
const BROWSER_RESUME_STALE_MS = 20_000

export type DesktopV3RealtimeFrame = RunStreamEventMessage & {
  protocol?: string
  protocol_version?: number
  kind?: string
  endpoint_cursor?: string
  event_type?: string
  subscription_id?: string
  rev?: number
  prevRev?: number
  error_code?: string
}

type DesktopV3RealtimeSubscription = {
  sessionId: string
  subscriptionId?: string | null
  endpointCursor?: string | null
}

export type DesktopV3RealtimeSubscriptionDiagnostics = {
  sessionId: string
  subscriptionId: string
  endpointCursorPresent: boolean
  subscribeSentAt: number
  subscribeSentCount: number
  lastFrameKind: string
  lastEventType: string
  lastFrameAt: number
  lastReplayStartedAt: number
  lastReplayCompleteAt: number
  lastEventAt: number
  lastEndpointCursorPresent: boolean
}

export type DesktopV3RealtimeSocketState = 'none' | 'connecting' | 'open' | 'closing' | 'closed'

export type DesktopV3RealtimeTraceEvent = {
  ts: number
  event: string
  generation: number
  desired: boolean
  socketState: DesktopV3RealtimeSocketState
  subscriptionCount: number
  connectBlockedReason: string
  sessionId?: string
  targetSessionId?: string
  traceId?: string
  source?: string
  reason?: string
  [key: string]: unknown
}

export type DesktopV3RealtimeDiagnostics = {
  desired: boolean
  socketState: DesktopV3RealtimeSocketState
  generation: number
  endpointCursorPresent: boolean
  reconnectAttempt: number
  lastActivityAt: number
  subscriptionCount: number
  connectBlockedReason: string
  subscriptions: DesktopV3RealtimeSubscriptionDiagnostics[]
  recentEvents: DesktopV3RealtimeTraceEvent[]
}

export type DesktopV3RealtimeTraceContext = {
  source?: string
  traceId?: string
  targetSessionId?: string
}

export type DesktopV3RealtimeSyncOptions = {
  resubscribe?: boolean
  replace?: boolean
} & DesktopV3RealtimeTraceContext

type DesktopV3RealtimeControllerOptions = {
  getEndpointCursor: () => string
  onFrame: (sessionId: string, payload: DesktopV3RealtimeFrame, ts: number) => boolean
  onReconnectPending?: (reason: string, ts: number) => void
  onCursorError?: (sessionId: string, payload: DesktopV3RealtimeFrame, ts: number) => void
}

type SubscriptionEntry = {
  sessionId: string
  subscriptionId: string
  endpointCursor: string
  subscribeSentAt: number
  subscribeSentCount: number
  lastFrameKind: string
  lastEventType: string
  lastFrameAt: number
  lastReplayStartedAt: number
  lastReplayCompleteAt: number
  lastEventAt: number
  lastEndpointCursorPresent: boolean
}

function reconnectDelayMs(attempt: number): number {
  const exponent = Math.max(0, attempt)
  const baseDelay = Math.min(RECONNECT_MAX_DELAY_MS, RECONNECT_BASE_DELAY_MS * (2 ** exponent))
  const jitterWindow = Math.max(1, Math.floor(baseDelay * RECONNECT_JITTER_RATIO))
  const jitterOffset = Math.floor((Math.random() * (jitterWindow * 2 + 1)) - jitterWindow)
  return Math.max(RECONNECT_BASE_DELAY_MS, baseDelay + jitterOffset)
}

function normalizedFrame(raw: unknown): DesktopV3RealtimeFrame | null {
  if (!raw || typeof raw !== 'object') {
    return null
  }
  const frame = raw as DesktopV3RealtimeFrame
  const kind = String(frame.kind ?? frame.type ?? '').trim()
  if (!kind) {
    return null
  }
  return { ...frame, type: kind, kind }
}

function frameSessionId(frame: DesktopV3RealtimeFrame): string {
  return String(frame.session_id ?? frame.event?.session_id ?? '').trim()
}

function frameEndpointCursor(frame: DesktopV3RealtimeFrame): string {
  return String(frame.endpoint_cursor ?? '').trim()
}

function firstEndpointCursor(...cursors: Array<string | null | undefined>): string {
  for (const cursor of cursors) {
    const normalized = cursor?.trim() ?? ''
    if (normalized) {
      return normalized
    }
  }
  return ''
}

function shouldDeliverFrame(kind: string): boolean {
  return kind === 'event'
    || kind === 'replay.started'
    || kind === 'replay.complete'
    || kind === 'keepalive'
    || kind === 'endpoint.watermark'
    || kind === 'cursor.error'
    || kind === 'auth.denied'
    || kind === 'slow_consumer.reconnect_required'
}

function shouldAdvanceCursorForControlFrame(kind: string): boolean {
  return kind === 'endpoint.watermark'
    || kind === 'replay.complete'
    || kind === 'keepalive'
}

function socketStateName(socket: WebSocket | null): DesktopV3RealtimeDiagnostics['socketState'] {
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

function subscriptionDiagnostics(sub: SubscriptionEntry): DesktopV3RealtimeSubscriptionDiagnostics {
  return {
    sessionId: sub.sessionId,
    subscriptionId: sub.subscriptionId,
    endpointCursorPresent: sub.endpointCursor.trim() !== '',
    subscribeSentAt: sub.subscribeSentAt,
    subscribeSentCount: sub.subscribeSentCount,
    lastFrameKind: sub.lastFrameKind,
    lastEventType: sub.lastEventType,
    lastFrameAt: sub.lastFrameAt,
    lastReplayStartedAt: sub.lastReplayStartedAt,
    lastReplayCompleteAt: sub.lastReplayCompleteAt,
    lastEventAt: sub.lastEventAt,
    lastEndpointCursorPresent: sub.lastEndpointCursorPresent,
  }
}

export class DesktopV3RealtimeController {
  private readonly subscriptions = new Map<string, SubscriptionEntry>()
  private readonly recentEvents: DesktopV3RealtimeTraceEvent[] = []
  private socket: WebSocket | null = null
  private connecting: Promise<void> | null = null
  private reconnectTimer: number | null = null
  private livenessTimer: number | null = null
  private reconnectAttempt = 0
  private generation = 0
  private lastActivityAt = 0
  private endpointCursor = ''
  private desired = false

  constructor(private readonly options: DesktopV3RealtimeControllerOptions) {
    this.endpointCursor = options.getEndpointCursor().trim()
  }

  subscribeSession(sessionId: string, endpointCursor?: string | null, subscriptionId?: string | null, context: DesktopV3RealtimeTraceContext = {}): Promise<void> {
    const normalizedSessionId = sessionId.trim()
    if (!normalizedSessionId) {
      this.trace('subscribeSession.skipped', { ...context, reason: 'empty-session-id' })
      return Promise.resolve()
    }
    const existing = this.subscriptions.get(normalizedSessionId)
    const cursor = existing
      ? firstEndpointCursor(existing.endpointCursor, this.endpointCursor, endpointCursor, this.options.getEndpointCursor())
      : firstEndpointCursor(endpointCursor, this.endpointCursor, this.options.getEndpointCursor())
    const normalizedSubscriptionId = subscriptionId?.trim() || existing?.subscriptionId || `desktop:${normalizedSessionId}`
    this.subscriptions.set(normalizedSessionId, {
      sessionId: normalizedSessionId,
      subscriptionId: normalizedSubscriptionId,
      endpointCursor: cursor,
      subscribeSentAt: existing?.subscribeSentAt ?? 0,
      subscribeSentCount: existing?.subscribeSentCount ?? 0,
      lastFrameKind: existing?.lastFrameKind ?? '',
      lastEventType: existing?.lastEventType ?? '',
      lastFrameAt: existing?.lastFrameAt ?? 0,
      lastReplayStartedAt: existing?.lastReplayStartedAt ?? 0,
      lastReplayCompleteAt: existing?.lastReplayCompleteAt ?? 0,
      lastEventAt: existing?.lastEventAt ?? 0,
      lastEndpointCursorPresent: existing?.lastEndpointCursorPresent ?? false,
    })
    this.desired = true
    this.trace('subscribeSession.upserted', {
      ...context,
      sessionId: normalizedSessionId,
      subscriptionId: normalizedSubscriptionId,
      hadExistingSubscription: Boolean(existing),
      endpointCursorPresent: cursor.trim() !== '',
    })
    if (existing && this.socket?.readyState === WebSocket.OPEN) {
      this.trace('subscribeSession.no_send_existing_socket_open', { ...context, sessionId: normalizedSessionId })
      return Promise.resolve()
    }
    if (this.socket?.readyState === WebSocket.OPEN) {
      this.sendSubscribe(this.socket, this.subscriptions.get(normalizedSessionId)!, context)
      return Promise.resolve()
    }
    this.trace('subscribeSession.connect_requested', { ...context, sessionId: normalizedSessionId })
    return this.connect(context)
  }

  syncSessions(subscriptions: DesktopV3RealtimeSubscription[], options: DesktopV3RealtimeSyncOptions = {}): void {
    const normalized = subscriptions
      .map((sub) => ({
        sessionId: sub.sessionId.trim(),
        subscriptionId: sub.subscriptionId?.trim() || null,
        endpointCursor: sub.endpointCursor,
      }))
      .filter((sub) => sub.sessionId)
    this.trace('syncSessions.start', {
      ...options,
      replace: Boolean(options.replace),
      resubscribe: Boolean(options.resubscribe),
      incomingSessionIds: normalized.map((sub) => sub.sessionId),
      existingSessionIds: Array.from(this.subscriptions.keys()),
      targetPresentInIncoming: options.targetSessionId ? normalized.some((sub) => sub.sessionId === options.targetSessionId) : undefined,
    })
    const removedSessionIds: string[] = []
    if (options.replace) {
      const keep = new Set(normalized.map((sub) => sub.sessionId))
      for (const sessionId of Array.from(this.subscriptions.keys())) {
        if (!keep.has(sessionId)) {
          this.subscriptions.delete(sessionId)
          removedSessionIds.push(sessionId)
        }
      }
      if (removedSessionIds.length > 0) {
        this.trace('syncSessions.removed_missing_subscriptions', {
          ...options,
          removedSessionIds,
          targetRemoved: options.targetSessionId ? removedSessionIds.includes(options.targetSessionId) : undefined,
          reason: 'replace=true target missing from incoming reconnect subscriptions',
        })
      }
    }
    for (const sub of normalized) {
      this.subscribeSession(sub.sessionId, sub.endpointCursor, sub.subscriptionId, options)
    }
    if (this.subscriptions.size === 0) {
      this.desired = false
      this.trace('syncSessions.desired_false_no_subscriptions', {
        ...options,
        removedSessionIds,
        reason: 'subscriptions.size=0 after syncSessions',
      })
      return
    }
    if (normalized.length > 0 && options.resubscribe && this.socket?.readyState === WebSocket.OPEN) {
      for (const sub of normalized) {
        const entry = this.subscriptions.get(sub.sessionId)
        if (entry) {
          this.sendSubscribe(this.socket, entry, options)
        }
      }
      return
    }
    if (normalized.length > 0 && this.desired && (!this.socket || this.socket.readyState === WebSocket.CLOSED)) {
      this.trace('syncSessions.connect_requested', options)
      void this.connect(options)
    }
  }

  setEndpointCursor(endpointCursor: string | null | undefined): void {
    const cursor = endpointCursor?.trim() ?? ''
    if (cursor) {
      this.advanceEndpointCursor(cursor)
    }
  }

  diagnostics(sessionId?: string | null): DesktopV3RealtimeDiagnostics {
    const normalizedSessionId = sessionId?.trim() ?? ''
    const allSubscriptions = Array.from(this.subscriptions.values())
    const subscriptions = normalizedSessionId
      ? allSubscriptions.filter((sub) => sub.sessionId === normalizedSessionId)
      : allSubscriptions
    const socketState = socketStateName(this.socket)
    const connectBlockedReason = !this.desired
      ? 'controller.desired=false'
      : allSubscriptions.length === 0
        ? 'controller.subscriptions=0'
        : socketState === 'open' || socketState === 'connecting'
          ? `controller.socket=${socketState}`
          : 'controller.ready-to-connect'
    return {
      desired: this.desired,
      socketState,
      generation: this.generation,
      endpointCursorPresent: this.endpointCursor.trim() !== '',
      reconnectAttempt: this.reconnectAttempt,
      lastActivityAt: this.lastActivityAt,
      subscriptionCount: allSubscriptions.length,
      connectBlockedReason,
      subscriptions: subscriptions.map(subscriptionDiagnostics),
      recentEvents: this.recentEvents.slice(-40),
    }
  }

  closeAll(context: DesktopV3RealtimeTraceContext = {}): void {
    this.trace('closeAll.start', context)
    this.desired = false
    this.subscriptions.clear()
    this.clearReconnect()
    this.clearLiveness()
    const socket = this.socket
    this.socket = null
    this.connecting = null
    this.lastActivityAt = 0
    this.generation += 1
    socket?.close()
    this.trace('closeAll.complete', context)
  }

  reconnectIfStale(reason: string, context: DesktopV3RealtimeTraceContext = {}): void {
    if (!this.desired || this.subscriptions.size === 0) {
      this.trace('reconnectIfStale.skipped', { ...context, reason })
      return
    }
    const socketState = this.socket?.readyState ?? WebSocket.CLOSED
    const activityStale = Date.now() - this.lastActivityAt >= BROWSER_RESUME_STALE_MS
    if (this.reconnectTimer === null && socketState === WebSocket.OPEN && !activityStale) {
      return
    }
    this.forceReconnect(reason, context)
  }

  async connect(context: DesktopV3RealtimeTraceContext = {}): Promise<void> {
    if (!this.desired || this.subscriptions.size === 0) {
      this.trace('connect.skipped', {
        ...context,
        reason: !this.desired ? 'controller.desired=false' : 'controller.subscriptions=0',
      })
      return
    }
    if (this.socket && (this.socket.readyState === WebSocket.OPEN || this.socket.readyState === WebSocket.CONNECTING)) {
      this.trace('connect.skipped', { ...context, reason: `socket=${socketStateName(this.socket)}` })
      return
    }
    if (this.connecting) {
      this.trace('connect.await_existing', context)
      return this.connecting
    }
    this.trace('connect.start', context)
    this.connecting = this.openConnection(context)
    try {
      await this.connecting
    } finally {
      this.connecting = null
    }
  }

  private async openConnection(context: DesktopV3RealtimeTraceContext = {}): Promise<void> {
    this.clearReconnect()
    this.generation += 1
    const generation = this.generation
    this.trace('openConnection.start', { ...context, generation })
    try {
      const socket = await openDesktopV3RealtimeStream({ endpointCursor: this.endpointCursor || this.options.getEndpointCursor() })
      if (generation !== this.generation || !this.desired) {
        this.trace('openConnection.close_stale_or_undesired', {
          ...context,
          generation,
          reason: generation !== this.generation ? 'generation-changed' : 'controller.desired=false',
        })
        socket.close()
        return
      }
      this.socket = socket
      this.attachSocket(socket, generation, context)
      this.noteActivity(generation)
      this.trace('openConnection.attached', { ...context, generation })
    } catch (error) {
      if (generation !== this.generation) {
        return
      }
      const message = error instanceof Error ? error.message : 'Failed to open V3 realtime stream'
      this.trace('openConnection.failed', { ...context, generation, reason: message })
      this.options.onReconnectPending?.(message, Date.now())
      this.scheduleReconnect(message, context)
    }
  }

  private attachSocket(socket: WebSocket, generation: number, context: DesktopV3RealtimeTraceContext = {}): void {
    let subscriptionsSent = false
    const handleOpen = () => {
      if (generation !== this.generation || this.socket !== socket || !this.desired) {
        this.trace('socket.open.close_stale_or_undesired', {
          ...context,
          generation,
          reason: generation !== this.generation || this.socket !== socket ? 'generation-or-socket-changed' : 'controller.desired=false',
        })
        socket.close()
        return
      }
      this.reconnectAttempt = 0
      this.clearReconnect()
      this.noteActivity(generation)
      this.trace('socket.open', { ...context, generation })
      if (!subscriptionsSent) {
        subscriptionsSent = true
        this.sendAllSubscriptions(socket, context)
      }
    }

    socket.addEventListener('open', handleOpen)
    if (socket.readyState === WebSocket.OPEN) {
      queueMicrotask(handleOpen)
    }

    socket.addEventListener('message', (event) => {
      if (generation !== this.generation || this.socket !== socket) {
        return
      }
      this.noteActivity(generation)
      try {
        const frame = normalizedFrame(JSON.parse(String(event.data)))
        if (!frame) {
          this.trace('socket.message.ignored', { ...context, generation, reason: 'missing-kind' })
          return
        }
        const kind = String(frame.kind ?? frame.type ?? '').trim()
        const sessionId = frameSessionId(frame)
        const eventType = String(frame.event?.event_type ?? frame.event_type ?? '').trim()
        let applied = false
        if (shouldDeliverFrame(kind)) {
          const ts = Date.now()
          applied = this.options.onFrame(sessionId, frame, ts)
          if (kind === 'cursor.error' || kind === 'auth.denied') {
            this.options.onCursorError?.(sessionId, frame, ts)
          }
        }
        const cursor = frameEndpointCursor(frame)
        this.trace('socket.message', {
          ...context,
          generation,
          sessionId,
          kind,
          eventType,
          applied,
          endpointCursorPresent: cursor !== '',
          matchingSubscriptionPresent: sessionId ? this.subscriptions.has(sessionId) : false,
        })
        this.noteSubscriptionFrame(sessionId, kind, eventType, cursor, Date.now(), context)
        if (cursor && (applied || shouldAdvanceCursorForControlFrame(kind))) {
          this.advanceEndpointCursor(cursor)
        }
        if (kind === 'slow_consumer.reconnect_required') {
          this.forceReconnect('slow consumer', context)
        }
      } catch (error) {
        console.error('[desktop-v3-realtime] frame parse failed', error)
      }
    })

    socket.addEventListener('error', () => {
      if (generation !== this.generation || this.socket !== socket || !this.desired) {
        this.trace('socket.error.ignored', { ...context, generation, reason: 'stale-socket-or-undesired' })
        return
      }
      this.trace('socket.error', { ...context, generation, reason: 'WebSocket error event' })
      this.options.onReconnectPending?.('V3 realtime socket error', Date.now())
      this.forceReconnect('socket error', context)
    })

    socket.addEventListener('close', () => {
      this.clearLiveness()
      if (generation !== this.generation || this.socket !== socket) {
        this.trace('socket.close.ignored', { ...context, generation, reason: 'stale-socket' })
        return
      }
      this.socket = null
      this.trace('socket.close', { ...context, generation })
      if (!this.desired || this.subscriptions.size === 0) {
        this.trace('socket.close.no_reconnect', {
          ...context,
          generation,
          reason: !this.desired ? 'controller.desired=false' : 'controller.subscriptions=0',
        })
        return
      }
      this.options.onReconnectPending?.('V3 realtime socket closed', Date.now())
      this.scheduleReconnect('socket closed', context)
    })
  }

  private sendAllSubscriptions(socket: WebSocket, context: DesktopV3RealtimeTraceContext = {}): void {
    this.trace('sendAllSubscriptions.start', { ...context, sessionIds: Array.from(this.subscriptions.keys()) })
    for (const sub of this.subscriptions.values()) {
      this.sendSubscribe(socket, sub, context)
    }
  }

  private sendSubscribe(socket: WebSocket, sub: SubscriptionEntry, context: DesktopV3RealtimeTraceContext = {}): void {
    sub.subscribeSentAt = Date.now()
    sub.subscribeSentCount += 1
    const endpointCursor = sub.endpointCursor || this.endpointCursor || this.options.getEndpointCursor()
    this.trace('subscribe.frame_sent', {
      ...context,
      sessionId: sub.sessionId,
      subscriptionId: sub.subscriptionId,
      endpointCursorPresent: endpointCursor.trim() !== '',
      subscribeSentCount: sub.subscribeSentCount,
    })
    socket.send(JSON.stringify({
      protocol: 'v3.realtime',
      protocol_version: 1,
      kind: 'subscribe.session',
      session_id: sub.sessionId,
      subscription_id: sub.subscriptionId,
      endpoint_cursor: endpointCursor,
    }))
  }

  private noteSubscriptionFrame(sessionId: string, kind: string, eventType: string, endpointCursor: string, ts: number, context: DesktopV3RealtimeTraceContext = {}): void {
    const sub = this.subscriptions.get(sessionId)
    if (!sub) {
      this.trace('frame.no_matching_subscription', {
        ...context,
        sessionId,
        kind,
        eventType,
        endpointCursorPresent: endpointCursor.trim() !== '',
        reason: sessionId ? 'session-not-in-subscription-map' : 'frame-has-no-session-id',
      })
      return
    }
    sub.lastFrameKind = kind
    sub.lastEventType = eventType
    sub.lastFrameAt = ts
    sub.lastEndpointCursorPresent = endpointCursor.trim() !== ''
    if (kind === 'event') {
      sub.lastEventAt = ts
    } else if (kind === 'replay.started') {
      sub.lastReplayStartedAt = ts
    } else if (kind === 'replay.complete') {
      sub.lastReplayCompleteAt = ts
    }
  }

  private advanceEndpointCursor(cursor: string): void {
    const nextCursor = firstEndpointCursor(cursor, this.endpointCursor)
    if (!nextCursor) {
      return
    }
    this.endpointCursor = nextCursor
    for (const sub of this.subscriptions.values()) {
      sub.endpointCursor = firstEndpointCursor(nextCursor, sub.endpointCursor)
    }
  }

  private forceReconnect(reason: string, context: DesktopV3RealtimeTraceContext = {}): void {
    this.trace('forceReconnect.start', { ...context, reason })
    this.clearReconnect()
    this.clearLiveness()
    const socket = this.socket
    this.socket = null
    this.connecting = null
    this.lastActivityAt = 0
    this.generation += 1
    socket?.close()
    this.scheduleReconnect(reason, context)
  }

  private scheduleReconnect(reason: string, context: DesktopV3RealtimeTraceContext = {}): void {
    if (!this.desired || this.subscriptions.size === 0 || this.reconnectTimer !== null) {
      this.trace('scheduleReconnect.skipped', {
        ...context,
        reason,
        skipReason: !this.desired
          ? 'controller.desired=false'
          : this.subscriptions.size === 0
            ? 'controller.subscriptions=0'
            : 'reconnect-timer-active',
      })
      return
    }
    const attempt = this.reconnectAttempt
    const delay = reconnectDelayMs(attempt)
    this.reconnectAttempt += 1
    this.trace('scheduleReconnect.scheduled', { ...context, reason, attempt, delay })
    this.reconnectTimer = window.setTimeout(() => {
      this.reconnectTimer = null
      void this.connect(context)
    }, delay)
    console.warn(`[desktop-v3-realtime] scheduled reconnect after ${reason}`)
  }

  private noteActivity(generation: number): void {
    this.lastActivityAt = Date.now()
    this.armLiveness(generation)
  }

  private armLiveness(generation: number): void {
    this.clearLiveness()
    if (!this.desired) {
      return
    }
    this.livenessTimer = window.setTimeout(() => {
      if (generation !== this.generation || !this.desired) {
        return
      }
      this.options.onReconnectPending?.('V3 realtime inactivity timeout', Date.now())
      this.forceReconnect('stream inactivity timeout')
    }, LIVENESS_TIMEOUT_MS)
  }

  private trace(event: string, details: Record<string, unknown> = {}): void {
    const snapshot = this.diagnosticsSnapshot()
    const record: DesktopV3RealtimeTraceEvent = {
      ts: Date.now(),
      event,
      generation: this.generation,
      desired: this.desired,
      socketState: socketStateName(this.socket),
      subscriptionCount: this.subscriptions.size,
      connectBlockedReason: snapshot.connectBlockedReason,
      ...details,
    }
    this.recentEvents.push(record)
    if (this.recentEvents.length > 80) {
      this.recentEvents.splice(0, this.recentEvents.length - 80)
    }
    if (typeof console !== 'undefined') {
      console.info('[desktop-v3-realtime-trace]', record)
    }
  }

  private diagnosticsSnapshot(): Pick<DesktopV3RealtimeDiagnostics, 'socketState' | 'connectBlockedReason'> {
    const allSubscriptions = Array.from(this.subscriptions.values())
    const socketState = socketStateName(this.socket)
    const connectBlockedReason = !this.desired
      ? 'controller.desired=false'
      : allSubscriptions.length === 0
        ? 'controller.subscriptions=0'
        : socketState === 'open' || socketState === 'connecting'
          ? `controller.socket=${socketState}`
          : 'controller.ready-to-connect'
    return { socketState, connectBlockedReason }
  }

  private clearReconnect(): void {
    if (this.reconnectTimer !== null) {
      window.clearTimeout(this.reconnectTimer)
      this.reconnectTimer = null
    }
  }

  private clearLiveness(): void {
    if (this.livenessTimer !== null) {
      window.clearTimeout(this.livenessTimer)
      this.livenessTimer = null
    }
  }
}
