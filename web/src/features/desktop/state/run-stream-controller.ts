import {
  openRunStream,
  startSessionRun,
  stopSessionRun,
  type DesktopRunAccepted,
  type DesktopBackgroundRunStartOptions,
} from '../chat/queries/chat-queries'

const RECONNECT_BASE_DELAY_MS = 1500
const RECONNECT_MAX_DELAY_MS = 15_000
const RECONNECT_JITTER_RATIO = 0.2

export type RunStreamEventMessage = {
  type?: string
  ok?: boolean
  session_id?: string
  run_id?: string
  seq?: number
  error?: string
  status?: string
  summary?: string
  delta?: string
  tool_name?: string
  call_id?: string
  arguments?: string
  step?: number
  output?: string
  raw_output?: string
  agent?: string
  background?: boolean
  target_kind?: string
  target_name?: string
  usage_summary?: {
    session_id?: string
    provider?: string
    model?: string
    source?: string
    context_window?: number
    total_tokens?: number
    remaining_tokens?: number
    updated_at?: number
  } | null
  lifecycle?: {
    session_id?: string
    run_id?: string
    active?: boolean
    phase?: string
    started_at?: number
    ended_at?: number
    updated_at?: number
    generation?: number
    stop_reason?: string
    error?: string
    owner_transport?: string
  }
  message?: {
    id?: string
    session_id?: string
    global_seq?: number
    role?: string
    content?: string
    created_at?: number
    metadata?: Record<string, unknown>
  }
}

type ResumeRequest = {
  sessionId: string
  runId: string
  lastSeq: number
}

type SessionControllerEntry = {
  sessionId: string
  desiredRunId: string | null
  socket: WebSocket | null
  socketRunId: string | null
  reconnectTimer: number | null
  reconnectAttempt: number
  generation: number
  closingSocket: WebSocket | null
}

type DesktopRunStreamControllerOptions = {
  getResumeRequest: (sessionId: string, fallbackRunId?: string | null) => ResumeRequest | null
  onFrame: (sessionId: string, payload: RunStreamEventMessage, ts: number) => void
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

function normalizeLifecycleInactive(payload: RunStreamEventMessage): boolean {
  if (!payload.lifecycle || typeof payload.lifecycle !== 'object') {
    return false
  }
  return payload.lifecycle.active === false
}

function isTerminalSessionStatus(payload: RunStreamEventMessage): boolean {
  const normalized = String(payload.status ?? '').trim().toLowerCase()
  return normalized === 'idle' || normalized === 'error'
}

export class DesktopRunStreamController {
  private readonly entries = new Map<string, SessionControllerEntry>()

  constructor(private readonly options: DesktopRunStreamControllerOptions) {}

  async start(options: DesktopBackgroundRunStartOptions): Promise<DesktopRunAccepted> {
    const accepted = await startSessionRun(options)
    const sessionId = options.sessionId.trim()
    const runId = accepted.run_id?.trim() ?? ''
    if (!runId) {
      throw new Error('Run start response did not include a run id')
    }
    void this.ensure(sessionId, runId)
    return accepted
  }

  async stop(sessionId: string, runId: string): Promise<void> {
    await stopSessionRun(sessionId, runId)
  }

  async ensure(sessionId: string, runId?: string | null): Promise<void> {
    const normalizedSessionId = sessionId.trim()
    if (!normalizedSessionId) {
      return
    }
    const resumeRequest = this.options.getResumeRequest(normalizedSessionId, runId)
    if (!resumeRequest) {
      this.close(normalizedSessionId)
      return
    }
    const entry = this.getOrCreateEntry(normalizedSessionId)
    entry.desiredRunId = resumeRequest.runId

    if (
      entry.socket &&
      entry.socketRunId === resumeRequest.runId &&
      (entry.socket.readyState === WebSocket.OPEN || entry.socket.readyState === WebSocket.CONNECTING)
    ) {
      return
    }

    this.cancelReconnect(entry)
    this.closeSocket(entry, false)
    await this.open(entry, resumeRequest)
  }

  close(sessionId: string): void {
    const normalizedSessionId = sessionId.trim()
    if (!normalizedSessionId) {
      return
    }
    const entry = this.entries.get(normalizedSessionId)
    if (!entry) {
      return
    }
    entry.desiredRunId = null
    this.cancelReconnect(entry)
    this.closeSocket(entry, true)
    this.maybeDeleteEntry(entry)
  }

  closeAll(): void {
    Array.from(this.entries.keys()).forEach((sessionId) => this.close(sessionId))
  }

  activeSessionCount(): number {
    return this.entries.size
  }

  private getOrCreateEntry(sessionId: string): SessionControllerEntry {
    const existing = this.entries.get(sessionId)
    if (existing) {
      return existing
    }
    const created: SessionControllerEntry = {
      sessionId,
      desiredRunId: null,
      socket: null,
      socketRunId: null,
      reconnectTimer: null,
      reconnectAttempt: 0,
      generation: 0,
      closingSocket: null,
    }
    this.entries.set(sessionId, created)
    return created
  }

  private async open(entry: SessionControllerEntry, resumeRequest: ResumeRequest): Promise<void> {
    entry.generation += 1
    const generation = entry.generation

    try {
      const socket = await openRunStream(resumeRequest.sessionId)
      if (entry.generation !== generation || entry.desiredRunId !== resumeRequest.runId) {
        socket.close()
        return
      }
      entry.socket = socket
      entry.socketRunId = resumeRequest.runId
      this.attachSocket(entry, socket, generation)
      if (socket.readyState === WebSocket.OPEN) {
        this.sendResume(entry, socket)
      }
    } catch (error) {
      if (entry.generation !== generation) {
        return
      }
      const message = error instanceof Error ? error.message : 'Failed to open run stream'
      this.options.onReconnectPending(entry.sessionId, message, Date.now())
      this.scheduleReconnect(entry, message)
    }
  }

  private attachSocket(entry: SessionControllerEntry, socket: WebSocket, generation: number): void {
    socket.addEventListener('open', () => {
      if (entry.generation !== generation || entry.socket !== socket) {
        this.markSocketClosing(entry, socket)
        socket.close()
        return
      }
      this.cancelReconnect(entry)
      this.sendResume(entry, socket)
    })

    socket.addEventListener('message', (event) => {
      if (entry.generation !== generation || entry.socket !== socket) {
        return
      }
      try {
        const payload = JSON.parse(String(event.data)) as RunStreamEventMessage
        const type = String(payload.type ?? '').trim()
        const ts = Date.now()
        this.options.onFrame(entry.sessionId, payload, ts)

        if (type === 'resume.accepted') {
          entry.reconnectAttempt = 0
          this.cancelReconnect(entry)
          return
        }

        if (type === 'resume.error') {
          const message = String(payload.error ?? 'Run stream replay window expired')
          this.options.onResumeFailure(entry.sessionId, message, ts)
          entry.desiredRunId = null
          this.cancelReconnect(entry)
          this.closeSocket(entry, true)
          this.maybeDeleteEntry(entry)
          return
        }

        if (type === 'error') {
          const message = String(payload.error ?? 'Run stream failed')
          this.options.onResumeFailure(entry.sessionId, message, ts)
          entry.desiredRunId = null
          this.cancelReconnect(entry)
          this.closeSocket(entry, true)
          this.maybeDeleteEntry(entry)
          return
        }

        if (
          type === 'turn.completed' ||
          type === 'turn.error' ||
          normalizeLifecycleInactive(payload) ||
          (type === 'session.status' && isTerminalSessionStatus(payload))
        ) {
          entry.desiredRunId = null
          this.cancelReconnect(entry)
          this.closeSocket(entry, true)
          this.maybeDeleteEntry(entry)
        }
      } catch (error) {
        console.error('[desktop-run-controller] run stream parse failed', error)
      }
    })

    socket.addEventListener('close', () => {
      if (entry.closingSocket === socket) {
        entry.closingSocket = null
        this.maybeDeleteEntry(entry)
        return
      }
      if (entry.socket !== socket) {
        return
      }
      entry.socket = null
      entry.socketRunId = null
      if (!entry.desiredRunId) {
        this.maybeDeleteEntry(entry)
        return
      }
      const ts = Date.now()
      this.options.onReconnectPending(entry.sessionId, 'socket closed', ts)
      this.scheduleReconnect(entry, 'socket closed')
    })
  }

  private sendResume(entry: SessionControllerEntry, socket: WebSocket): void {
    const request = this.options.getResumeRequest(entry.sessionId, entry.desiredRunId)
    if (!request || request.runId !== entry.desiredRunId) {
      this.close(entry.sessionId)
      return
    }
    try {
      socket.send(JSON.stringify({
        type: 'run.resume',
        run_id: request.runId,
        last_seq: request.lastSeq,
      }))
    } catch (error) {
      const message = error instanceof Error ? error.message : 'Failed to send run resume'
      this.options.onReconnectPending(entry.sessionId, message, Date.now())
      this.scheduleReconnect(entry, message)
    }
  }

  private scheduleReconnect(entry: SessionControllerEntry, reason: string): void {
    if (!entry.desiredRunId || entry.reconnectTimer !== null) {
      return
    }
    const attempt = entry.reconnectAttempt
    const delay = reconnectDelayMs(attempt)
    entry.reconnectAttempt += 1
    entry.reconnectTimer = window.setTimeout(() => {
      entry.reconnectTimer = null
      const request = this.options.getResumeRequest(entry.sessionId, entry.desiredRunId)
      if (!request) {
        entry.desiredRunId = null
        this.maybeDeleteEntry(entry)
        return
      }
      void this.open(entry, request)
    }, delay)
    console.warn(`[desktop-run-controller] scheduled run stream reconnect for session=${entry.sessionId} after ${reason}`)
  }

  private cancelReconnect(entry: SessionControllerEntry): void {
    if (entry.reconnectTimer !== null) {
      window.clearTimeout(entry.reconnectTimer)
      entry.reconnectTimer = null
    }
  }

  private closeSocket(entry: SessionControllerEntry, clearActiveSocket: boolean): void {
    const socket = entry.socket
    if (!socket) {
      return
    }
    this.markSocketClosing(entry, socket)
    if (clearActiveSocket) {
      entry.socket = null
      entry.socketRunId = null
    }
    socket.close()
  }

  private markSocketClosing(entry: SessionControllerEntry, socket: WebSocket): void {
    entry.closingSocket = socket
  }

  private maybeDeleteEntry(entry: SessionControllerEntry): void {
    if (entry.desiredRunId || entry.socket || entry.reconnectTimer !== null || entry.closingSocket) {
      return
    }
    this.entries.delete(entry.sessionId)
  }
}
