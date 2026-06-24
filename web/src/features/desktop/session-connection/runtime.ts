import type {
  SessionConnectResponse,
  SessionStartResponse,
  SessionReadyFrame,
  SessionReconnectRequiredFrame,
  SessionSnapshot,
  SessionStreamFrame,
} from './contract.generated'

export type SessionConnectionStatus =
  | 'connecting'
  | 'open'
  | 'ready'
  | 'reconnect_required'
  | 'closed'
  | 'error'

export interface SessionConnection {
  readonly sessionId: string
  readonly connectionId: string
  readonly snapshot: SessionSnapshot
  readonly response: SessionConnectResponse | SessionStartResponse
  readonly ready: Promise<SessionReadyFrame>
  readonly status: SessionConnectionStatus
  readonly resumeToken: string
  subscribe(listener: (frame: SessionStreamFrame) => void): () => void
  close(reason?: string): void
}

export interface SessionConnectionRuntimeOptions {
  openSocket?: (url: string) => WebSocket
  setTimeout?: typeof globalThis.setTimeout
  clearTimeout?: typeof globalThis.clearTimeout
}

export function createSessionConnectionRuntime(
  response: SessionConnectResponse | SessionStartResponse,
  options: SessionConnectionRuntimeOptions = {},
): SessionConnection {
  const runtime = new SessionConnectionRuntime(response, options)
  runtime.start()
  return runtime
}

class SessionConnectionRuntime implements SessionConnection {
  readonly sessionId: string
  readonly connectionId: string
  readonly snapshot: SessionSnapshot
  readonly response: SessionConnectResponse | SessionStartResponse
  readonly ready: Promise<SessionReadyFrame>

  private readonly openSocket: (url: string) => WebSocket
  private readonly setTimer: typeof globalThis.setTimeout
  private readonly clearTimer: typeof globalThis.clearTimeout
  private readonly listeners = new Set<(frame: SessionStreamFrame) => void>()
  private readonly backlog: SessionStreamFrame[] = []
  private socket?: WebSocket
  private readyTimer?: ReturnType<typeof globalThis.setTimeout>
  private readyResolved = false
  private closedByClient = false
  private statusValue: SessionConnectionStatus = 'connecting'
  private resumeTokenValue: string
  private resolveReady!: (frame: SessionReadyFrame) => void
  private rejectReady!: (error: Error) => void

  constructor(response: SessionConnectResponse, options: SessionConnectionRuntimeOptions) {
    this.response = response
    this.sessionId = response.session_id
    this.connectionId = response.connection.connection_id
    this.snapshot = response.snapshot
    this.resumeTokenValue = response.connection.resume_token
    this.openSocket = options.openSocket ?? ((url) => new WebSocket(url))
    this.setTimer = options.setTimeout ?? globalThis.setTimeout.bind(globalThis)
    this.clearTimer = options.clearTimeout ?? globalThis.clearTimeout.bind(globalThis)
    this.ready = new Promise<SessionReadyFrame>((resolve, reject) => {
      this.resolveReady = resolve
      this.rejectReady = reject
    })
  }

  get status(): SessionConnectionStatus {
    return this.statusValue
  }

  get resumeToken(): string {
    return this.resumeTokenValue
  }

  start(): void {
    if (this.socket) return
    const url = sessionConnectionWebSocketURL(this.response.connection.stream_url)
    const socket = this.openSocket(url)
    this.socket = socket
    this.readyTimer = this.setTimer(() => {
      if (this.readyResolved) return
      const error = new Error(`Session connection ${this.connectionId} did not become ready before timeout`)
      this.statusValue = 'error'
      this.rejectReady(error)
      try {
        socket.close(4000, 'session ready timeout')
      } catch {
        // The socket is already closing or closed.
      }
    }, this.response.connection.ready_timeout_ms)

    socket.onopen = () => {
      if (this.statusValue === 'connecting') this.statusValue = 'open'
    }
    socket.onerror = () => {
      const error = new Error(`Session connection ${this.connectionId} websocket error`)
      this.statusValue = 'error'
      this.rejectReadyIfPending(error)
    }
    socket.onclose = () => {
      if (this.closedByClient) {
        this.statusValue = 'closed'
        this.rejectReadyIfPending(new Error(`Session connection ${this.connectionId} closed before ready`))
        return
      }
      if (!this.readyResolved) {
        this.statusValue = 'error'
        this.rejectReadyIfPending(new Error(`Session connection ${this.connectionId} closed before ready`))
        return
      }
      this.statusValue = 'closed'
    }
    socket.onmessage = (event) => {
      void this.handleSocketMessage(event.data)
    }
  }

  subscribe(listener: (frame: SessionStreamFrame) => void): () => void {
    this.listeners.add(listener)
    if (this.backlog.length > 0) {
      const frames = this.backlog.splice(0, this.backlog.length)
      for (const frame of frames) listener(frame)
    }
    return () => {
      this.listeners.delete(listener)
    }
  }

  close(reason = 'session connection closed'): void {
    this.closedByClient = true
    this.clearReadyTimer()
    this.socket?.close(1000, reason)
    this.statusValue = 'closed'
    this.rejectReadyIfPending(new Error(reason))
  }

  private async handleSocketMessage(data: unknown): Promise<void> {
    const frame = parseSessionStreamFrame(await messageDataToText(data))
    if (frame.type === 'session.ready') {
      this.resumeTokenValue = frame.resume_token
      this.statusValue = 'ready'
      this.readyResolved = true
      this.clearReadyTimer()
      this.resolveReady(frame)
    } else if (frame.type === 'session.event') {
      this.resumeTokenValue = frame.resume_token
    } else if (frame.type === 'session.reconnect_required') {
      this.statusValue = 'reconnect_required'
      this.clearReadyTimer()
      this.rejectReadyIfPending(sessionReconnectRequiredError(frame))
    }
    this.emit(frame)
  }

  private emit(frame: SessionStreamFrame): void {
    if (this.listeners.size === 0) {
      this.backlog.push(frame)
      return
    }
    for (const listener of this.listeners) listener(frame)
  }

  private clearReadyTimer(): void {
    if (!this.readyTimer) return
    this.clearTimer(this.readyTimer)
    this.readyTimer = undefined
  }

  private rejectReadyIfPending(error: Error): void {
    if (this.readyResolved) return
    this.clearReadyTimer()
    this.rejectReady(error)
  }
}

export function sessionConnectionWebSocketURL(streamURL: string): string {
  const raw = streamURL.trim()
  if (!raw) throw new Error('Session connection stream_url is empty')
  if (raw.startsWith('ws://') || raw.startsWith('wss://')) return raw

  const location = window.location
  const httpBase = `${location.protocol}//${location.host}`
  const url = new URL(raw, httpBase)
  url.protocol = location.protocol === 'https:' ? 'wss:' : 'ws:'
  return url.toString()
}

async function messageDataToText(data: unknown): Promise<string> {
  if (typeof data === 'string') return data
  if (data instanceof Blob) return data.text()
  if (data instanceof ArrayBuffer) return new TextDecoder().decode(data)
  if (ArrayBuffer.isView(data)) return new TextDecoder().decode(data)
  throw new Error('Session connection received unsupported websocket message data')
}

function parseSessionStreamFrame(text: string): SessionStreamFrame {
  const value = JSON.parse(text) as Partial<SessionStreamFrame>
  if (!value || typeof value !== 'object' || typeof value.type !== 'string') {
    throw new Error('Session connection stream frame is missing required type')
  }
  switch (value.type) {
    case 'session.ready':
    case 'session.event':
    case 'run.phase':
    case 'session.reconnect_required':
      return value as SessionStreamFrame
    default:
      throw new Error(`Session connection stream frame has unknown type ${value.type}`)
  }
}

function sessionReconnectRequiredError(frame: SessionReconnectRequiredFrame): Error {
  return new Error(`Session reconnect required: ${frame.reason}`)
}
