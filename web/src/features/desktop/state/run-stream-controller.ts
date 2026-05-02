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
const RUN_STREAM_LIVENESS_TIMEOUT_MS = 45_000
const RUN_STREAM_BROWSER_RESUME_STALE_MS = 20_000

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
  owner_transport?: string
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

type StartResolver = {
  resolve: (accepted: DesktopRunAccepted) => void
  reject: (error: Error) => void
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
  livenessTimer: number | null
  lastActivityAt: number
  pendingStart: DesktopBackgroundRunStartOptions | null
  startResolver: StartResolver | null
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

function isSessionAlreadyActiveRunError(message: string): boolean {
  return message.trim().toLowerCase() === 'session already has an active run'
}

export class DesktopRunStreamController {
  private readonly entries = new Map<string, SessionControllerEntry>()

  private readonly handleBrowserOnline = (): void => {
    this.refreshActiveEntries('browser online')
  }

  private readonly handleBrowserFocus = (): void => {
    this.refreshActiveEntries('window focus')
  }

  private readonly handleVisibilityChange = (): void => {
    if (typeof document !== 'undefined' && document.visibilityState === 'visible') {
      this.refreshActiveEntries('visibility restored')
    }
  }

  constructor(private readonly options: DesktopRunStreamControllerOptions) {
    if (typeof window !== 'undefined') {
      window.addEventListener('online', this.handleBrowserOnline)
      window.addEventListener('focus', this.handleBrowserFocus)
    }
    if (typeof document !== 'undefined') {
      document.addEventListener('visibilitychange', this.handleVisibilityChange)
    }
  }

  async start(options: DesktopBackgroundRunStartOptions): Promise<DesktopRunAccepted> {
    if (options.background) {
      const accepted = await startSessionRun(options)
      const sessionId = options.sessionId.trim()
      const runId = accepted.run_id?.trim() ?? ''
      if (!runId) {
        throw new Error('Run start response did not include a run id')
      }
      void this.ensure(sessionId, runId)
      return accepted
    }

    const sessionId = options.sessionId.trim()
    if (!sessionId) {
      throw new Error('session id is required')
    }
    const prompt = options.prompt.trim()
    if (!prompt && !options.compact) {
      throw new Error('prompt is required')
    }

    console.info(`[desktop-run-controller] websocket start opening session=${sessionId}`)
    return new Promise<DesktopRunAccepted>((resolve, reject) => {
      const entry = this.getOrCreateEntry(sessionId)
      this.cancelReconnect(entry)
      this.clearLiveness(entry)
      this.closeSocket(entry, false)
      entry.desiredRunId = null
      entry.pendingStart = options
      entry.startResolver = { resolve, reject }
      void this.openStart(entry)
    })
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
      entry.socket
      && entry.socketRunId === resumeRequest.runId
      && (entry.socket.readyState === WebSocket.OPEN || entry.socket.readyState === WebSocket.CONNECTING)
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
    this.rejectPendingStart(entry, new Error('run stream closed'))
    this.cancelReconnect(entry)
    this.clearLiveness(entry)
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
      livenessTimer: null,
      lastActivityAt: 0,
      pendingStart: null,
      startResolver: null,
    }
    this.entries.set(sessionId, created)
    return created
  }

  private async openStart(entry: SessionControllerEntry): Promise<void> {
    const startOptions = entry.pendingStart
    if (!startOptions) {
      return
    }
    entry.generation += 1
    const generation = entry.generation

    try {
      const socket = await openRunStream(entry.sessionId)
      if (entry.generation !== generation || entry.pendingStart !== startOptions) {
        socket.close()
        return
      }
      entry.socket = socket
      entry.socketRunId = null
      this.attachSocket(entry, socket, generation)
      this.noteActivity(entry, generation)
      if (socket.readyState === WebSocket.OPEN) {
        this.sendStart(entry, socket, startOptions)
      }
    } catch (error) {
      if (entry.generation !== generation) {
        return
      }
      this.rejectPendingStart(entry, error instanceof Error ? error : new Error('Failed to open run stream'))
      this.closeSocket(entry, true)
      this.maybeDeleteEntry(entry)
    }
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
      this.noteActivity(entry, generation)
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
      this.noteActivity(entry, generation)
      this.cancelReconnect(entry)
      if (entry.pendingStart) {
        this.sendStart(entry, socket, entry.pendingStart)
      } else {
        this.sendResume(entry, socket)
      }
    })

    socket.addEventListener('message', (event) => {
      if (entry.generation !== generation || entry.socket !== socket) {
        return
      }
      this.noteActivity(entry, generation)
      try {
        const payload = JSON.parse(String(event.data)) as RunStreamEventMessage
        const type = String(payload.type ?? '').trim()
        const ts = Date.now()
        this.options.onFrame(entry.sessionId, payload, ts)

        if (type === 'run.accepted') {
          const runId = String(payload.run_id ?? '').trim()
          if (!runId) {
            this.rejectPendingStart(entry, new Error('Run start response did not include a run id'))
            this.closeSocket(entry, true)
            this.maybeDeleteEntry(entry)
            return
          }
          console.info(`[desktop-run-controller] websocket start accepted session=${entry.sessionId} run=${runId}`)
          entry.desiredRunId = runId
          entry.socketRunId = runId
          entry.reconnectAttempt = 0
          this.cancelReconnect(entry)
          this.resolvePendingStart(entry, {
            ok: payload.ok,
            session_id: payload.session_id,
            run_id: runId,
            status: 'accepted',
            background: payload.background,
            target_kind: payload.target_kind,
            target_name: payload.target_name,
            owner_transport: payload.owner_transport,
          })
          return
        }

        if (type === 'resume.accepted') {
          entry.reconnectAttempt = 0
          this.cancelReconnect(entry)
          return
        }

        if (type === 'resume.error') {
          const message = String(payload.error ?? 'Run stream replay window expired')
          this.options.onResumeFailure(entry.sessionId, message, ts)
          this.rejectPendingStart(entry, new Error(message))
          entry.desiredRunId = null
          this.cancelReconnect(entry)
          this.closeSocket(entry, true)
          this.maybeDeleteEntry(entry)
          return
        }

        if (type === 'error') {
          const message = String(payload.error ?? 'Run stream failed')
          if (entry.pendingStart) {
            this.options.onResumeFailure(entry.sessionId, message, ts)
            this.rejectPendingStart(entry, new Error(message))
            entry.desiredRunId = null
            this.cancelReconnect(entry)
            this.closeSocket(entry, true)
            this.maybeDeleteEntry(entry)
            return
          }
          if (entry.desiredRunId && isSessionAlreadyActiveRunError(message)) {
            this.options.onReconnectPending(entry.sessionId, message, ts)
            this.refreshEntry(entry, message)
            return
          }
          this.options.onResumeFailure(entry.sessionId, message, ts)
          entry.desiredRunId = null
          this.cancelReconnect(entry)
          this.closeSocket(entry, true)
          this.maybeDeleteEntry(entry)
          return
        }

        if (
          type === 'turn.completed'
          || type === 'turn.error'
          || normalizeLifecycleInactive(payload)
          || (type === 'session.status' && isTerminalSessionStatus(payload))
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

    socket.addEventListener('error', () => {
      if (entry.generation !== generation || entry.socket !== socket) {
        return
      }
      if (entry.pendingStart) {
        this.rejectPendingStart(entry, new Error('socket error'))
        this.closeSocket(entry, true)
        this.maybeDeleteEntry(entry)
        return
      }
      if (!entry.desiredRunId) {
        return
      }
      const ts = Date.now()
      this.options.onReconnectPending(entry.sessionId, 'socket error', ts)
      this.refreshEntry(entry, 'socket error')
    })

    socket.addEventListener('close', () => {
      this.clearLiveness(entry)
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
        if (entry.pendingStart) {
          this.rejectPendingStart(entry, new Error('socket closed before run start was accepted'))
        }
        this.maybeDeleteEntry(entry)
        return
      }
      const ts = Date.now()
      this.options.onReconnectPending(entry.sessionId, 'socket closed', ts)
      this.scheduleReconnect(entry, 'socket closed')
    })
  }

  private sendStart(entry: SessionControllerEntry, socket: WebSocket, options: DesktopBackgroundRunStartOptions): void {
    try {
      console.info(`[desktop-run-controller] websocket start sending session=${entry.sessionId}`)
      socket.send(JSON.stringify({
        type: 'run.start',
        prompt: options.prompt.trim(),
        agent_name: options.agentName?.trim() ?? '',
        instructions: options.instructions?.trim() ?? '',
        compact: Boolean(options.compact),
        background: Boolean(options.background),
        target_kind: options.targetKind?.trim() ?? '',
        target_name: options.targetName?.trim() ?? '',
        tool_scope: options.toolScope,
        execution_context: options.executionContext,
      }))
    } catch (error) {
      this.rejectPendingStart(entry, error instanceof Error ? error : new Error('Failed to send run start'))
      this.closeSocket(entry, true)
      this.maybeDeleteEntry(entry)
    }
  }

  private resolvePendingStart(entry: SessionControllerEntry, accepted: DesktopRunAccepted): void {
    const resolver = entry.startResolver
    entry.pendingStart = null
    entry.startResolver = null
    resolver?.resolve(accepted)
  }

  private rejectPendingStart(entry: SessionControllerEntry, error: Error): void {
    const resolver = entry.startResolver
    entry.pendingStart = null
    entry.startResolver = null
    if (resolver) {
      resolver.reject(error)
    }
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
    this.clearLiveness(entry)
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

  private noteActivity(entry: SessionControllerEntry, generation: number): void {
    entry.lastActivityAt = Date.now()
    this.armLiveness(entry, generation)
  }

  private armLiveness(entry: SessionControllerEntry, generation: number): void {
    this.clearLiveness(entry)
    if (!entry.desiredRunId && !entry.pendingStart) {
      return
    }
    const timer = window.setTimeout(() => {
      if (entry.generation !== generation || entry.livenessTimer !== timer || !entry.desiredRunId || entry.pendingStart) {
        return
      }
      const ts = Date.now()
      this.options.onReconnectPending(entry.sessionId, 'stream inactivity timeout', ts)
      this.refreshEntry(entry, 'stream inactivity timeout')
    }, RUN_STREAM_LIVENESS_TIMEOUT_MS)
    entry.livenessTimer = timer
  }

  private clearLiveness(entry: SessionControllerEntry): void {
    if (entry.livenessTimer !== null) {
      window.clearTimeout(entry.livenessTimer)
      entry.livenessTimer = null
    }
  }

  private refreshActiveEntries(reason: string): void {
    if (typeof navigator !== 'undefined' && navigator.onLine === false) {
      return
    }
    const now = Date.now()
    for (const entry of this.entries.values()) {
      if (!entry.desiredRunId) {
        continue
      }
      const socketState = entry.socket?.readyState ?? WebSocket.CLOSED
      const activityStale = now - entry.lastActivityAt >= RUN_STREAM_BROWSER_RESUME_STALE_MS
      if (entry.reconnectTimer === null && socketState === WebSocket.OPEN && !activityStale) {
        continue
      }
      this.options.onReconnectPending(entry.sessionId, reason, now)
      this.refreshEntry(entry, reason)
    }
  }

  private refreshEntry(entry: SessionControllerEntry, reason: string): void {
    const request = this.options.getResumeRequest(entry.sessionId, entry.desiredRunId)
    if (!request) {
      entry.desiredRunId = null
      this.cancelReconnect(entry)
      this.clearLiveness(entry)
      this.closeSocket(entry, true)
      this.maybeDeleteEntry(entry)
      return
    }
    entry.desiredRunId = request.runId
    this.cancelReconnect(entry)
    this.clearLiveness(entry)
    this.closeSocket(entry, false)
    console.warn(`[desktop-run-controller] forcing run stream reconnect for session=${entry.sessionId} after ${reason}`)
    void this.open(entry, request)
  }

  private closeSocket(entry: SessionControllerEntry, clearActiveSocket: boolean): void {
    this.clearLiveness(entry)
    const socket = entry.socket
    if (!socket) {
      return
    }
    this.markSocketClosing(entry, socket)
    if (clearActiveSocket || entry.socket === socket) {
      entry.socket = null
      entry.socketRunId = null
    }
    socket.close()
  }

  private markSocketClosing(entry: SessionControllerEntry, socket: WebSocket): void {
    entry.closingSocket = socket
  }

  private maybeDeleteEntry(entry: SessionControllerEntry): void {
    if (entry.desiredRunId || entry.pendingStart || entry.startResolver || entry.socket || entry.reconnectTimer !== null || entry.closingSocket || entry.livenessTimer !== null) {
      return
    }
    this.entries.delete(entry.sessionId)
  }
}
