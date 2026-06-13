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
  subscription_id?: string
  rev?: number
  prevRev?: number
  error_code?: string
}

type DesktopV3RealtimeSubscription = {
  sessionId: string
  endpointCursor?: string | null
}

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
  const cursor = String(frame.endpoint_cursor ?? '').trim()
  return cursor.startsWith('cursor-') ? cursor : ''
}

function endpointCursorSeq(cursor: string | null | undefined): number {
  const normalized = cursor?.trim() ?? ''
  if (!normalized.startsWith('cursor-')) {
    return 0
  }
  const parsed = Number.parseInt(normalized.slice('cursor-'.length), 10)
  return Number.isFinite(parsed) && parsed > 0 ? parsed : 0
}

function maxEndpointCursor(...cursors: Array<string | null | undefined>): string {
  let best = ''
  let bestSeq = 0
  for (const cursor of cursors) {
    const normalized = cursor?.trim() ?? ''
    const seq = endpointCursorSeq(normalized)
    if (seq >= bestSeq && normalized) {
      best = normalized
      bestSeq = seq
    }
  }
  return best
}

function shouldDeliverFrame(kind: string): boolean {
  return kind === 'event'
    || kind === 'replay.started'
    || kind === 'replay.complete'
    || kind === 'keepalive'
    || kind === 'cursor.error'
    || kind === 'auth.denied'
    || kind === 'slow_consumer.reconnect_required'
}

export class DesktopV3RealtimeController {
  private readonly subscriptions = new Map<string, SubscriptionEntry>()
  private socket: WebSocket | null = null
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

  subscribeSession(sessionId: string, endpointCursor?: string | null): void {
    const normalizedSessionId = sessionId.trim()
    if (!normalizedSessionId) {
      return
    }
    const existing = this.subscriptions.get(normalizedSessionId)
    const cursor = maxEndpointCursor(
      endpointCursor,
      existing?.endpointCursor,
      this.endpointCursor,
      this.options.getEndpointCursor(),
    )
    this.subscriptions.set(normalizedSessionId, {
      sessionId: normalizedSessionId,
      subscriptionId: existing?.subscriptionId || `desktop:${normalizedSessionId}`,
      endpointCursor: cursor,
    })
    this.desired = true
    if (existing && this.socket?.readyState === WebSocket.OPEN) {
      return
    }
    if (this.socket?.readyState === WebSocket.OPEN) {
      this.sendSubscribe(this.socket, this.subscriptions.get(normalizedSessionId)!)
      return
    }
    void this.connect()
  }

  syncSessions(subscriptions: DesktopV3RealtimeSubscription[]): void {
    for (const sub of subscriptions) {
      this.subscribeSession(sub.sessionId, sub.endpointCursor)
    }
  }

  setEndpointCursor(endpointCursor: string | null | undefined): void {
    const cursor = endpointCursor?.trim() ?? ''
    if (cursor) {
      this.endpointCursor = cursor
    }
  }

  closeAll(): void {
    this.desired = false
    this.subscriptions.clear()
    this.clearReconnect()
    this.clearLiveness()
    const socket = this.socket
    this.socket = null
    this.lastActivityAt = 0
    this.generation += 1
    socket?.close()
  }

  reconnectIfStale(reason: string): void {
    if (!this.desired || this.subscriptions.size === 0) {
      return
    }
    const socketState = this.socket?.readyState ?? WebSocket.CLOSED
    const activityStale = Date.now() - this.lastActivityAt >= BROWSER_RESUME_STALE_MS
    if (this.reconnectTimer === null && socketState === WebSocket.OPEN && !activityStale) {
      return
    }
    this.forceReconnect(reason)
  }

  async connect(): Promise<void> {
    if (!this.desired || this.subscriptions.size === 0) {
      return
    }
    if (this.socket && (this.socket.readyState === WebSocket.OPEN || this.socket.readyState === WebSocket.CONNECTING)) {
      return
    }
    this.clearReconnect()
    this.generation += 1
    const generation = this.generation
    try {
      const socket = await openDesktopV3RealtimeStream({ endpointCursor: this.endpointCursor || this.options.getEndpointCursor() })
      if (generation !== this.generation || !this.desired) {
        socket.close()
        return
      }
      this.socket = socket
      this.attachSocket(socket, generation)
      this.noteActivity(generation)
    } catch (error) {
      if (generation !== this.generation) {
        return
      }
      const message = error instanceof Error ? error.message : 'Failed to open V3 realtime stream'
      this.options.onReconnectPending?.(message, Date.now())
      this.scheduleReconnect(message)
    }
  }

  private attachSocket(socket: WebSocket, generation: number): void {
    let subscriptionsSent = false
    const handleOpen = () => {
      if (generation !== this.generation || this.socket !== socket || !this.desired) {
        socket.close()
        return
      }
      this.reconnectAttempt = 0
      this.clearReconnect()
      this.noteActivity(generation)
      if (!subscriptionsSent) {
        subscriptionsSent = true
        this.sendAllSubscriptions(socket)
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
          return
        }
        const kind = String(frame.kind ?? frame.type ?? '').trim()
        const sessionId = frameSessionId(frame)
        let applied = false
        if (shouldDeliverFrame(kind)) {
          const ts = Date.now()
          applied = this.options.onFrame(sessionId, frame, ts)
          if (kind === 'cursor.error' || kind === 'auth.denied') {
            this.options.onCursorError?.(sessionId, frame, ts)
          }
        }
        const cursor = frameEndpointCursor(frame)
        if (cursor && applied) {
          this.advanceEndpointCursor(cursor)
        }
        if (kind === 'slow_consumer.reconnect_required') {
          this.forceReconnect('slow consumer')
        }
      } catch (error) {
        console.error('[desktop-v3-realtime] frame parse failed', error)
      }
    })

    socket.addEventListener('error', () => {
      if (generation !== this.generation || this.socket !== socket || !this.desired) {
        return
      }
      this.options.onReconnectPending?.('V3 realtime socket error', Date.now())
      this.forceReconnect('socket error')
    })

    socket.addEventListener('close', () => {
      this.clearLiveness()
      if (generation !== this.generation || this.socket !== socket) {
        return
      }
      this.socket = null
      if (!this.desired || this.subscriptions.size === 0) {
        return
      }
      this.options.onReconnectPending?.('V3 realtime socket closed', Date.now())
      this.scheduleReconnect('socket closed')
    })
  }

  private sendAllSubscriptions(socket: WebSocket): void {
    for (const sub of this.subscriptions.values()) {
      this.sendSubscribe(socket, sub)
    }
  }

  private sendSubscribe(socket: WebSocket, sub: SubscriptionEntry): void {
    socket.send(JSON.stringify({
      protocol: 'v3.realtime',
      protocol_version: 1,
      kind: 'subscribe.session',
      session_id: sub.sessionId,
      subscription_id: sub.subscriptionId,
      endpoint_cursor: sub.endpointCursor || this.endpointCursor || this.options.getEndpointCursor(),
    }))
  }

  private advanceEndpointCursor(cursor: string): void {
    const nextCursor = maxEndpointCursor(cursor, this.endpointCursor)
    if (!nextCursor) {
      return
    }
    this.endpointCursor = nextCursor
    for (const sub of this.subscriptions.values()) {
      sub.endpointCursor = maxEndpointCursor(nextCursor, sub.endpointCursor)
    }
  }

  private forceReconnect(reason: string): void {
    this.clearReconnect()
    this.clearLiveness()
    const socket = this.socket
    this.socket = null
    this.lastActivityAt = 0
    this.generation += 1
    socket?.close()
    this.scheduleReconnect(reason)
  }

  private scheduleReconnect(reason: string): void {
    if (!this.desired || this.subscriptions.size === 0 || this.reconnectTimer !== null) {
      return
    }
    const attempt = this.reconnectAttempt
    const delay = reconnectDelayMs(attempt)
    this.reconnectAttempt += 1
    this.reconnectTimer = window.setTimeout(() => {
      this.reconnectTimer = null
      void this.connect()
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
