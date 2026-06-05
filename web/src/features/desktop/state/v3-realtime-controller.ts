import {
  openV3RealtimeStream,
} from '../chat/queries/chat-queries'
import {
  validateV3RealtimeMessage,
  V3_REALTIME_PROTOCOL,
  V3_REALTIME_PROTOCOL_VERSION,
  type V3RealtimeMessage,
} from '../realtime/v3-contract'

const RECONNECT_BASE_DELAY_MS = 1500
const RECONNECT_MAX_DELAY_MS = 15_000
const RECONNECT_JITTER_RATIO = 0.2
const V3_REALTIME_LIVENESS_TIMEOUT_MS = 45_000
const V3_REALTIME_BROWSER_RESUME_STALE_MS = 20_000

export type V3RealtimeControllerSubscription = {
  sessionId: string
  afterSeq: number
}

type V3RealtimeControllerOptions = {
  getSubscriptions: () => V3RealtimeControllerSubscription[]
  onFrame: (payload: V3RealtimeMessage, ts: number) => void
  onReconnectPending: (sessionId: string, reason: string, ts: number) => void
  onResumeFailure: (sessionId: string, message: string, ts: number) => void
}

function reconnectDelayMs(attempt: number): number {
  const exponent = Math.max(0, attempt)
  const baseDelay = Math.min(RECONNECT_MAX_DELAY_MS, RECONNECT_BASE_DELAY_MS * (2 ** exponent))
  const jitterWindow = Math.max(1, Math.floor(baseDelay * RECONNECT_JITTER_RATIO))
  const jitterOffset = Math.floor((Math.random() * (jitterWindow * 2 + 1)) - jitterWindow)
  return Math.max(RECONNECT_BASE_DELAY_MS, baseDelay + jitterOffset)
}

export class DesktopV3RealtimeController {
  private socket: WebSocket | null = null
  private reconnectTimer: number | null = null
  private reconnectAttempt = 0
  private generation = 0
  private livenessTimer: number | null = null
  private lastActivityAt = 0
  private endpointCursor: string | null = null
  private desired = false
  private subscribedSessions = new Set<string>()

  private readonly handleBrowserOnline = (): void => {
    this.refreshIfStale('browser online')
  }

  private readonly handleBrowserFocus = (): void => {
    this.refreshIfStale('window focus')
  }

  private readonly handleVisibilityChange = (): void => {
    if (typeof document !== 'undefined' && document.visibilityState === 'visible') {
      this.refreshIfStale('visibility restored')
    }
  }

  constructor(private readonly options: V3RealtimeControllerOptions) {
    if (typeof window !== 'undefined') {
      window.addEventListener('online', this.handleBrowserOnline)
      window.addEventListener('focus', this.handleBrowserFocus)
    }
    if (typeof document !== 'undefined') {
      document.addEventListener('visibilitychange', this.handleVisibilityChange)
    }
  }

  async ensure(): Promise<void> {
    this.desired = true
    if (this.socket && (this.socket.readyState === WebSocket.OPEN || this.socket.readyState === WebSocket.CONNECTING)) {
      if (this.socket.readyState === WebSocket.OPEN) {
        this.syncSubscriptions(this.socket)
      }
      return
    }
    this.cancelReconnect()
    await this.open()
  }

  close(): void {
    this.desired = false
    this.cancelReconnect()
    this.clearLiveness()
    this.subscribedSessions.clear()
    const socket = this.socket
    this.socket = null
    this.generation += 1
    socket?.close()
  }

  activeConnectionCount(): number {
    return this.socket ? 1 : 0
  }

  private async open(): Promise<void> {
    if (!this.desired) {
      return
    }
    this.generation += 1
    const generation = this.generation
    try {
      const socket = await openV3RealtimeStream({ endpointCursor: this.endpointCursor })
      if (this.generation !== generation || !this.desired) {
        socket.close()
        return
      }
      this.socket = socket
      this.subscribedSessions.clear()
      this.attachSocket(socket, generation)
      this.noteActivity(generation)
      if (socket.readyState === WebSocket.OPEN) {
        this.syncSubscriptions(socket)
      }
    } catch (error) {
      if (this.generation !== generation || !this.desired) {
        return
      }
      const message = error instanceof Error ? error.message : 'Failed to open V3 realtime stream'
      this.notifyReconnectPending(message)
      this.scheduleReconnect(message)
    }
  }

  private attachSocket(socket: WebSocket, generation: number): void {
    socket.addEventListener('open', () => {
      if (this.generation !== generation || this.socket !== socket || !this.desired) {
        socket.close()
        return
      }
      this.noteActivity(generation)
      this.cancelReconnect()
      this.reconnectAttempt = 0
      this.syncSubscriptions(socket)
    })

    socket.addEventListener('message', (event) => {
      if (this.generation !== generation || this.socket !== socket) {
        return
      }
      this.noteActivity(generation)
      try {
        const payload = JSON.parse(String(event.data)) as unknown
        validateV3RealtimeMessage(payload)
        const ts = Date.now()
        this.handleMessage(payload, ts)
      } catch (error) {
        console.error('[desktop-v3-realtime-controller] parse failed', error)
      }
    })

    socket.addEventListener('error', () => {
      if (this.generation !== generation || this.socket !== socket || !this.desired) {
        return
      }
      const ts = Date.now()
      this.notifyReconnectPending('socket error', ts)
      this.refresh('socket error')
    })

    socket.addEventListener('close', () => {
      this.clearLiveness()
      if (this.generation !== generation || this.socket !== socket) {
        return
      }
      this.socket = null
      this.subscribedSessions.clear()
      if (!this.desired) {
        return
      }
      const ts = Date.now()
      this.notifyReconnectPending('socket closed', ts)
      this.scheduleReconnect('socket closed')
    })
  }

  private handleMessage(payload: V3RealtimeMessage, ts: number): void {
    if (payload.endpoint_cursor?.trim()) {
      this.endpointCursor = payload.endpoint_cursor.trim()
    }
    this.options.onFrame(payload, ts)
    if (payload.kind === 'replay.started' || payload.kind === 'replay.complete' || payload.kind === 'keepalive') {
      this.reconnectAttempt = 0
      this.cancelReconnect()
      return
    }
    if (payload.kind === 'cursor.error') {
      const sessionId = payload.session_id?.trim() ?? ''
      const message = payload.error?.trim() || 'V3 realtime cursor failed'
      if (sessionId) {
        this.subscribedSessions.delete(sessionId)
        this.options.onReconnectPending(sessionId, message, ts)
      } else {
        this.notifyReconnectPending(message, ts)
        this.refresh(message)
      }
      return
    }
    if (payload.kind === 'auth.denied') {
      const sessionId = payload.session_id?.trim() ?? ''
      const message = payload.error?.trim() || 'V3 realtime authorization denied'
      if (sessionId) {
        this.subscribedSessions.delete(sessionId)
        this.options.onResumeFailure(sessionId, message, ts)
      }
      return
    }
    if (payload.kind === 'slow_consumer.reconnect_required') {
      const message = payload.reason?.trim() || payload.error?.trim() || 'V3 realtime slow consumer reconnect required'
      this.notifyReconnectPending(message, ts)
      this.refresh(message)
    }
  }

  private syncSubscriptions(socket: WebSocket): void {
    const desired = this.options.getSubscriptions()
    const desiredSessionIds = new Set(desired.map((item) => item.sessionId))
    for (const sessionId of Array.from(this.subscribedSessions)) {
      if (!desiredSessionIds.has(sessionId)) {
        this.send(socket, {
          protocol: V3_REALTIME_PROTOCOL,
          protocol_version: V3_REALTIME_PROTOCOL_VERSION,
          kind: 'unsubscribe.session',
          session_id: sessionId,
          subscription_id: this.subscriptionId(sessionId),
        })
        this.subscribedSessions.delete(sessionId)
      }
    }
    for (const item of desired) {
      if (this.subscribedSessions.has(item.sessionId)) {
        continue
      }
      this.send(socket, {
        protocol: V3_REALTIME_PROTOCOL,
        protocol_version: V3_REALTIME_PROTOCOL_VERSION,
        kind: 'subscribe.session',
        session_id: item.sessionId,
        subscription_id: this.subscriptionId(item.sessionId),
        after_seq: item.afterSeq,
      })
      this.subscribedSessions.add(item.sessionId)
    }
  }

  private send(socket: WebSocket, payload: V3RealtimeMessage): void {
    try {
      socket.send(JSON.stringify(payload))
    } catch (error) {
      const message = error instanceof Error ? error.message : 'Failed to send V3 realtime control message'
      this.notifyReconnectPending(message)
      this.refresh(message)
    }
  }

  private subscriptionId(sessionId: string): string {
    return `desktop-v3:${sessionId}`
  }

  private notifyReconnectPending(reason: string, ts = Date.now()): void {
    const subscriptions = this.options.getSubscriptions()
    if (subscriptions.length === 0) {
      return
    }
    for (const item of subscriptions) {
      this.options.onReconnectPending(item.sessionId, reason, ts)
    }
  }

  private scheduleReconnect(reason: string): void {
    if (!this.desired || this.reconnectTimer !== null) {
      return
    }
    this.clearLiveness()
    const attempt = this.reconnectAttempt
    const delay = reconnectDelayMs(attempt)
    this.reconnectAttempt += 1
    this.reconnectTimer = window.setTimeout(() => {
      this.reconnectTimer = null
      void this.open()
    }, delay)
    console.warn(`[desktop-v3-realtime-controller] scheduled reconnect after ${reason}`)
  }

  private cancelReconnect(): void {
    if (this.reconnectTimer !== null) {
      window.clearTimeout(this.reconnectTimer)
      this.reconnectTimer = null
    }
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
    const timer = window.setTimeout(() => {
      if (this.generation !== generation || this.livenessTimer !== timer || !this.desired) {
        return
      }
      const ts = Date.now()
      this.notifyReconnectPending('stream inactivity timeout', ts)
      this.refresh('stream inactivity timeout')
    }, V3_REALTIME_LIVENESS_TIMEOUT_MS)
    this.livenessTimer = timer
  }

  private clearLiveness(): void {
    if (this.livenessTimer !== null) {
      window.clearTimeout(this.livenessTimer)
      this.livenessTimer = null
    }
  }

  private refreshIfStale(reason: string): void {
    if (!this.desired) {
      return
    }
    if (typeof navigator !== 'undefined' && navigator.onLine === false) {
      return
    }
    const now = Date.now()
    const socketState = this.socket?.readyState ?? WebSocket.CLOSED
    const activityStale = now - this.lastActivityAt >= V3_REALTIME_BROWSER_RESUME_STALE_MS
    if (this.reconnectTimer === null && this.socket && socketState === WebSocket.OPEN && !activityStale) {
      this.syncSubscriptions(this.socket)
      return
    }
    this.notifyReconnectPending(reason, now)
    this.refresh(reason)
  }

  private refresh(reason: string): void {
    if (!this.desired) {
      return
    }
    this.cancelReconnect()
    this.clearLiveness()
    const socket = this.socket
    this.socket = null
    this.subscribedSessions.clear()
    this.generation += 1
    console.warn(`[desktop-v3-realtime-controller] forcing reconnect after ${reason}`)
    socket?.close()
    void this.open()
  }
}
