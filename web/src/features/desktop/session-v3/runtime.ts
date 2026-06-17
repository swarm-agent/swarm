import {
  createSessionV3,
  reconnectSessionV3,
  sendSessionV3Message,
  stopSessionV3Run,
  compactSessionV3,
  type SessionV3CompactOptions,
  type SessionV3CreateSessionInput,
  type SessionV3MessageOptions,
  type SessionV3ReconnectOptions,
  type SessionV3RequestOptions,
  type SessionV3StopRunInput,
} from './api'
import {
  createSessionV3ReducerInitialState,
  sessionV3Reducer,
  type SessionV3ReducerAction,
  type SessionV3ReducerResult,
  type SessionV3ReducerSnapshotMode,
  type SessionV3ReducerState,
} from './reducer'
import {
  DesktopV3RealtimeTransport,
  type DesktopV3RealtimeTransportDiagnostics,
  type DesktopV3RealtimeTransportOptions,
} from './transport'
import {
  SESSION_V3_REALTIME_PROTOCOL,
  SESSION_V3_REALTIME_PROTOCOL_VERSION,
  type SessionV3CreateSessionResponseWire,
  type SessionV3HydratedSessionResponseWire,
  type SessionV3CompactResponseWire,
  type SessionV3MessageCommitResponseWire,
  type SessionV3MessageRole,
  type SessionV3RealtimeSubscriptionRequestWire,
  type SessionV3RealtimeWorksetSubscriptionRequestWire,
  type SessionV3ReconnectSnapshot,
  type SessionV3ReconnectSubscriptionWire,
  type SessionV3WorksetRequestWire,
} from './types'
import type { DesktopState } from '../state/desktop-state'

const DEFAULT_WORKSET_ID = 'desktop-v3-runtime:global'
const DEFAULT_WORKSET_RECENT_LIMIT = 50
const RUNTIME_CLIENT_ID_STORAGE_KEY = 'swarm.desktop.session-v3.client-id'
const RUNTIME_CLIENT_ID_PREFIX = 'desktop-v3-runtime:'

export type SessionV3RuntimeApi = {
  reconnectSessionV3: typeof reconnectSessionV3
  createSessionV3: typeof createSessionV3
  sendSessionV3Message: typeof sendSessionV3Message
  stopSessionV3Run: typeof stopSessionV3Run
  compactSessionV3: typeof compactSessionV3
}

export type DesktopSessionV3RuntimeListener = (
  state: SessionV3ReducerState,
  result: SessionV3ReducerResult | null,
) => void

export interface DesktopSessionV3RuntimeOptions {
  clientId?: string
  workset?: SessionV3WorksetRequestWire | null
  wantedSessionIds?: string[]
  initialDesktopState?: DesktopState
  initialState?: SessionV3ReducerState
  api?: Partial<SessionV3RuntimeApi>
  transport?: DesktopV3RealtimeTransport
  transportFactory?: (options: DesktopV3RealtimeTransportOptions) => DesktopV3RealtimeTransport
  transportOptions?: Omit<DesktopV3RealtimeTransportOptions, 'getEndpointCursor' | 'onStatus' | 'onFrame' | 'onCursor' | 'onRehydrateRequested'>
  now?: () => number
}

export interface DesktopSessionV3RuntimeBootOptions extends SessionV3RequestOptions {
  clientId?: string
  workset?: SessionV3WorksetRequestWire | null
  mode?: SessionV3ReducerSnapshotMode
}

export interface DesktopSessionV3RuntimeRefreshOptions extends DesktopSessionV3RuntimeBootOptions {
  reason?: string
}

export interface DesktopSessionV3RuntimeSendMessageInput extends SessionV3MessageOptions {
  sessionId: string
  role?: SessionV3MessageRole
  content: string
}

export interface DesktopSessionV3RuntimeStopRunInput extends SessionV3RequestOptions, SessionV3StopRunInput {
  sessionId: string
}

export interface DesktopSessionV3RuntimeCompactSessionInput extends SessionV3CompactOptions {
  sessionId: string
}

export interface DesktopSessionV3RuntimeSetWantedSessionsOptions {
  replace?: boolean
  subscribe?: boolean
}

export interface DesktopSessionV3RuntimeSetWorksetOptions extends SessionV3RequestOptions {
  refresh?: boolean
}

export type DesktopSessionV3RuntimeCreateSessionInput = SessionV3CreateSessionInput

export class DesktopSessionV3Runtime {
  private state: SessionV3ReducerState
  private readonly listeners = new Set<DesktopSessionV3RuntimeListener>()
  private readonly api: SessionV3RuntimeApi
  private readonly transport: DesktopV3RealtimeTransport
  private readonly now: () => number
  private wantedSessionIds = new Set<string>()
  private clientId: string
  private workset: SessionV3WorksetRequestWire | null
  private bootInFlight: Promise<SessionV3ReducerState> | null = null
  private shutdownRequested = false

  constructor(options: DesktopSessionV3RuntimeOptions = {}) {
    this.now = options.now ?? (() => Date.now())
    this.state = options.initialState ?? createSessionV3ReducerInitialState(options.initialDesktopState)
    this.clientId = options.clientId?.trim() || runtimeClientId()
    this.workset = options.workset === null ? null : options.workset ?? defaultRuntimeWorkset()
    this.api = {
      reconnectSessionV3: options.api?.reconnectSessionV3 ?? reconnectSessionV3,
      createSessionV3: options.api?.createSessionV3 ?? createSessionV3,
      sendSessionV3Message: options.api?.sendSessionV3Message ?? sendSessionV3Message,
      stopSessionV3Run: options.api?.stopSessionV3Run ?? stopSessionV3Run,
      compactSessionV3: options.api?.compactSessionV3 ?? compactSessionV3,
    }
    const transportFactory = options.transportFactory ?? ((transportOptions: DesktopV3RealtimeTransportOptions) => new DesktopV3RealtimeTransport(transportOptions))
    this.transport = options.transport ?? transportFactory({
      ...options.transportOptions,
      getEndpointCursor: () => this.state.endpointCursor,
      onStatus: (event) => this.handleTransportStatus(event.status, event.reason),
      onFrame: (event) => {
        this.dispatch({ type: 'frame', frame: event.frame, receivedAt: event.at })
      },
      onRehydrateRequested: async (reason, _frame, meta) => this.loadReconnect({ reason, mode: 'merge' }, false, meta.at),
    })
    this.setWantedSessions(options.wantedSessionIds ?? [], { replace: true, subscribe: false })
  }

  getState(): SessionV3ReducerState {
    return this.state
  }

  diagnostics(): DesktopV3RealtimeTransportDiagnostics {
    return this.transport.diagnostics()
  }

  subscribe(listener: DesktopSessionV3RuntimeListener): () => void {
    this.listeners.add(listener)
    listener(this.state, null)
    return () => {
      this.listeners.delete(listener)
    }
  }

  async boot(options: DesktopSessionV3RuntimeBootOptions = {}): Promise<SessionV3ReducerState> {
    if (this.bootInFlight) return this.bootInFlight
    this.shutdownRequested = false
    this.bootInFlight = this.loadReconnect({ ...options, mode: options.mode ?? 'replace' }, true)
      .then(async () => {
        if (this.shutdownRequested) return this.state
        this.syncWantedSessionsToTransport()
        await this.transport.start()
        return this.state
      })
      .finally(() => {
        this.bootInFlight = null
      })
    return this.bootInFlight
  }

  async refresh(options: DesktopSessionV3RuntimeRefreshOptions = {}): Promise<SessionV3ReducerState> {
    this.shutdownRequested = false
    this.transport.resetForReconnectSnapshot(options.reason ?? 'refresh')
    const result = await this.loadReconnect({ ...options, mode: options.mode ?? 'merge' }, false)
    if (this.shutdownRequested) return this.state
    this.transport.applyReconnectSnapshot(result)
    this.syncWantedSessionsToTransport()
    await this.transport.start()
    return this.state
  }

  async createSession(input: DesktopSessionV3RuntimeCreateSessionInput): Promise<SessionV3CreateSessionResponseWire> {
    const response = await this.api.createSessionV3(input)
    this.applyMutation(response, responseSessionId(response), input.signal)
    return response
  }

  async sendMessage(input: DesktopSessionV3RuntimeSendMessageInput): Promise<SessionV3MessageCommitResponseWire> {
    const sessionId = normalizeString(input.sessionId)
    if (!sessionId) {
      throw new Error('Desktop session V3 runtime sendMessage requires sessionId.')
    }
    const response = await this.api.sendSessionV3Message(
      sessionId,
      input.role ?? 'user',
      input.content,
      input,
    )
    this.applyMutation(response, sessionId, input.signal)
    return response
  }

  async stopRun(input: DesktopSessionV3RuntimeStopRunInput): Promise<void> {
    const sessionId = normalizeString(input.sessionId)
    if (!sessionId) {
      throw new Error('Desktop session V3 runtime stopRun requires sessionId.')
    }
    const response = await this.api.stopSessionV3Run(sessionId, input, input)
    if (response) {
      this.applyMutation(response, sessionId, input.signal)
      return
    }
    this.setWantedSessions([sessionId])
    if (!input.signal?.aborted && typeof window !== 'undefined') {
      void this.transport.start()
    }
  }

  async compactSession(input: DesktopSessionV3RuntimeCompactSessionInput): Promise<SessionV3CompactResponseWire> {
    const sessionId = normalizeString(input.sessionId)
    if (!sessionId) {
      throw new Error('Desktop session V3 runtime compactSession requires sessionId.')
    }
    const response = await this.api.compactSessionV3(sessionId, input)
    this.applyMutation(response, sessionId, input.signal)
    return response
  }

  closeSession(sessionId: string): void {
    const normalizedSessionId = normalizeString(sessionId)
    if (!normalizedSessionId) return
    this.wantedSessionIds.delete(normalizedSessionId)
    this.transport.unregisterSession(normalizedSessionId)
  }

  setWantedSessions(sessionIds: string[], options: DesktopSessionV3RuntimeSetWantedSessionsOptions = {}): void {
    if (options.replace) {
      this.wantedSessionIds.clear()
    }
    for (const sessionId of sessionIds) {
      const normalizedSessionId = normalizeString(sessionId)
      if (normalizedSessionId) {
        this.wantedSessionIds.add(normalizedSessionId)
      }
    }
    if (options.subscribe !== false) {
      this.syncWantedSessionsToTransport()
    }
  }

  async setWorkset(workset: SessionV3WorksetRequestWire | null, options: DesktopSessionV3RuntimeSetWorksetOptions = {}): Promise<void> {
    this.workset = workset
    if (options.refresh !== false) {
      await this.refresh({ signal: options.signal, reason: 'workset updated' })
    }
  }

  stop(reason = 'runtime stopped'): void {
    this.shutdownRequested = true
    this.bootInFlight = null
    this.transport.stop(reason)
  }

  shutdown(): void {
    this.stop('runtime shutdown')
    this.transport.dispose()
    this.listeners.clear()
  }

  private async loadReconnect(
    options: DesktopSessionV3RuntimeRefreshOptions,
    applyTransport: boolean,
    receivedAt = this.now(),
  ): Promise<SessionV3ReconnectSnapshot> {
    const result = this.augmentReconnectSnapshot(await this.api.reconnectSessionV3(this.reconnectOptions(options)))
    if (this.shutdownRequested) return result
    this.dispatch({ type: 'reconnect', result, mode: options.mode ?? 'merge', receivedAt })
    if (applyTransport) {
      this.transport.applyReconnectSnapshot(result)
      this.syncWantedSessionsToTransport()
    }
    return result
  }

  private augmentReconnectSnapshot(result: SessionV3ReconnectSnapshot): SessionV3ReconnectSnapshot {
    const endpointCursor = result.endpointCursor || result.realtimeResume?.endpoint_cursor || this.state.endpointCursor
    const wantedSubscriptions = Array.from(this.wantedSessionIds)
      .map((sessionId) => wantedSessionSubscription(sessionId, endpointCursor))
      .filter((subscription): subscription is SessionV3ReconnectSubscriptionWire => Boolean(subscription))
    const resumeSubscriptions = (result.realtimeResume?.subscriptions ?? [])
      .map((subscription) => reconnectSubscriptionFromResume(subscription, endpointCursor))
      .filter((subscription): subscription is SessionV3ReconnectSubscriptionWire => Boolean(subscription))
    const subscriptions = mergeSubscriptions(
      mergeSubscriptions(result.subscriptions, resumeSubscriptions),
      wantedSubscriptions,
    )
    const worksets = mergeWorksets(result.worksets, result.realtimeResume?.worksets ?? [])
    return {
      ...result,
      subscriptions,
      worksets,
      realtimeResume: result.realtimeResume
        ? {
            ...result.realtimeResume,
            subscriptions,
            worksets,
          }
        : result.realtimeResume,
    }
  }

  private reconnectOptions(options: DesktopSessionV3RuntimeBootOptions): SessionV3ReconnectOptions {
    const workset = options.workset === null ? null : options.workset ?? this.workset
    if (options.clientId?.trim()) {
      this.clientId = options.clientId.trim()
    }
    return {
      signal: options.signal,
      clientId: workset ? this.clientId : undefined,
      workset: workset ?? undefined,
    }
  }

  private applyMutation(response: SessionV3HydratedSessionResponseWire | SessionV3CompactResponseWire, fallbackSessionId: string, signal?: AbortSignal): void {
    const sessionId = responseSessionId(response) || fallbackSessionId
    const result = this.dispatch({ type: 'mutation', response, sessionId, receivedAt: this.now() })
    const cursor = result.state.endpointCursor
    if (cursor) {
      this.transport.setEndpointCursor(cursor, 'snapshot')
    }
    if (sessionId) {
      this.setWantedSessions([sessionId])
    }
    if (!signal?.aborted && typeof window !== 'undefined') {
      void this.transport.start()
    }
  }

  private syncWantedSessionsToTransport(): void {
    const endpointCursor = this.state.endpointCursor
    const subscriptions = Array.from(this.wantedSessionIds)
      .map((sessionId) => wantedSessionSubscription(sessionId, endpointCursor))
      .filter((subscription): subscription is SessionV3ReconnectSubscriptionWire => Boolean(subscription))
    if (subscriptions.length > 0) {
      this.transport.setSessions(subscriptions, { replace: false })
    }
  }

  private handleTransportStatus(status: string, reason: string): void {
    switch (status) {
      case 'open':
        this.dispatch({ type: 'status', status: 'ready', receivedAt: this.now() })
        return
      case 'stale':
        this.dispatch({ type: 'stale', reason, receivedAt: this.now() })
        return
      case 'error':
        this.dispatch({ type: 'status', status: 'error', error: reason, receivedAt: this.now() })
        return
      default:
        return
    }
  }

  private dispatch(action: SessionV3ReducerAction): SessionV3ReducerResult {
    const result = sessionV3Reducer(this.state, action)
    if (result.state !== this.state) {
      this.state = result.state
      this.emit(result)
    }
    return result
  }

  private emit(result: SessionV3ReducerResult): void {
    for (const listener of this.listeners) {
      listener(this.state, result)
    }
  }
}

export function createDesktopSessionV3Runtime(options: DesktopSessionV3RuntimeOptions = {}): DesktopSessionV3Runtime {
  return new DesktopSessionV3Runtime(options)
}

function defaultRuntimeWorkset(): SessionV3WorksetRequestWire {
  return {
    workset_id: DEFAULT_WORKSET_ID,
    selector: { kind: 'global', global: true },
    recent: { limit: DEFAULT_WORKSET_RECENT_LIMIT },
    history: { mode: 'none', max_events_per_session: 0, manifest_policy: 'manifest', include_events: false },
    resources: { messages: true, events: true, run_intents: true, active_plan: true, plan_revisions: true },
    include_active: true,
    auto_subscribe_sessions: true,
  }
}

function wantedSessionSubscription(sessionId: string, endpointCursor: string): SessionV3ReconnectSubscriptionWire | null {
  const normalizedSessionId = normalizeString(sessionId)
  const normalizedEndpointCursor = normalizeString(endpointCursor)
  if (!normalizedSessionId || !normalizedEndpointCursor) return null
  return {
    protocol: SESSION_V3_REALTIME_PROTOCOL,
    protocol_version: SESSION_V3_REALTIME_PROTOCOL_VERSION,
    kind: 'subscribe.session',
    session_id: normalizedSessionId,
    subscription_id: `desktop-v3-runtime:session:${normalizedSessionId}`,
    endpoint_cursor: normalizedEndpointCursor,
  }
}

function reconnectSubscriptionFromResume(
  subscription: SessionV3RealtimeSubscriptionRequestWire,
  endpointCursor: string,
): SessionV3ReconnectSubscriptionWire | null {
  const normalizedSessionId = normalizeString(subscription.session_id)
  const normalizedSubscriptionId = normalizeString(subscription.subscription_id)
  const normalizedEndpointCursor = normalizeString(subscription.endpoint_cursor) || normalizeString(endpointCursor)
  if (!normalizedSessionId || !normalizedSubscriptionId || !normalizedEndpointCursor) return null
  return {
    protocol: SESSION_V3_REALTIME_PROTOCOL,
    protocol_version: SESSION_V3_REALTIME_PROTOCOL_VERSION,
    kind: 'subscribe.session',
    session_id: normalizedSessionId,
    subscription_id: normalizedSubscriptionId,
    endpoint_cursor: normalizedEndpointCursor,
  }
}

function mergeSubscriptions(
  base: SessionV3ReconnectSubscriptionWire[],
  extra: SessionV3ReconnectSubscriptionWire[],
): SessionV3ReconnectSubscriptionWire[]
function mergeSubscriptions(
  base: SessionV3RealtimeSubscriptionRequestWire[],
  extra: SessionV3RealtimeSubscriptionRequestWire[],
): SessionV3RealtimeSubscriptionRequestWire[]
function mergeSubscriptions(
  base: SessionV3RealtimeSubscriptionRequestWire[],
  extra: SessionV3RealtimeSubscriptionRequestWire[],
): SessionV3RealtimeSubscriptionRequestWire[] {
  const output = [...base]
  const indexes = new Map(output.map((subscription, index) => [subscription.session_id, index]))
  for (const subscription of extra) {
    const index = indexes.get(subscription.session_id)
    if (index === undefined) {
      indexes.set(subscription.session_id, output.length)
      output.push(subscription)
    } else {
      output[index] = { ...output[index], ...subscription }
    }
  }
  return output
}

function mergeWorksets(
  base: SessionV3RealtimeWorksetSubscriptionRequestWire[],
  extra: SessionV3RealtimeWorksetSubscriptionRequestWire[],
): SessionV3RealtimeWorksetSubscriptionRequestWire[] {
  const output = [...base]
  const indexes = new Map(output.map((workset, index) => [workset.workset_id, index]))
  for (const workset of extra) {
    const index = indexes.get(workset.workset_id)
    if (index === undefined) {
      indexes.set(workset.workset_id, output.length)
      output.push(workset)
    } else {
      output[index] = { ...output[index], ...workset }
    }
  }
  return output
}

function responseSessionId(response: SessionV3CreateSessionResponseWire | SessionV3HydratedSessionResponseWire): string {
  return normalizeString(response.session_id)
    || normalizeString(response.session?.id)
    || normalizeString(response.projection?.session_id)
    || normalizeString(response.active_run_intent?.session_id)
    || normalizeString(response.run_intent?.session_id)
}

export function runtimeClientId(): string {
  const persisted = normalizeRuntimeClientId(readRuntimeClientId())
  if (persisted) return persisted
  const generated = `${RUNTIME_CLIENT_ID_PREFIX}${randomRuntimeClientIdSegment()}`
  writeRuntimeClientId(generated)
  return generated
}

function readRuntimeClientId(): string {
  try {
    return runtimeClientIdStorage()?.getItem(RUNTIME_CLIENT_ID_STORAGE_KEY) ?? ''
  } catch {
    return ''
  }
}

function writeRuntimeClientId(clientId: string): void {
  try {
    runtimeClientIdStorage()?.setItem(RUNTIME_CLIENT_ID_STORAGE_KEY, clientId)
  } catch {
    // localStorage can be unavailable in private/sandboxed contexts; runtimeClientId still returns a generated id.
  }
}

function runtimeClientIdStorage(): Storage | null {
  try {
    const windowStorage = typeof window !== 'undefined' ? window.localStorage : null
    if (windowStorage) return windowStorage
  } catch {
    // Ignore denied window.localStorage access.
  }
  try {
    const globalStorage = (globalThis as typeof globalThis & { localStorage?: Storage }).localStorage
    return globalStorage ?? null
  } catch {
    return null
  }
}

function normalizeRuntimeClientId(value: unknown): string {
  const normalized = normalizeString(value)
  if (!normalized.startsWith(RUNTIME_CLIENT_ID_PREFIX)) return ''
  if (normalized.length <= RUNTIME_CLIENT_ID_PREFIX.length || normalized.length > 200) return ''
  return normalized
}

function randomRuntimeClientIdSegment(): string {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
    return crypto.randomUUID()
  }
  return `${Date.now()}:${Math.random().toString(16).slice(2)}`
}

function normalizeString(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}
