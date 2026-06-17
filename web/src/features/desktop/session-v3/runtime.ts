import {
  createSessionV3,
  bootstrapSessionV3Sync,
  hydrateSessionV3Sync,
  streamSessionV3Sync,
  sendSessionV3Message,
  stopSessionV3Run,
  compactSessionV3,
  type SessionV3CompactOptions,
  type SessionV3CreateSessionInput,
  type SessionV3MessageOptions,
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
  type SessionV3RealtimeWorksetSubscriptionRequestWire,
  type SessionV3SnapshotResult,
  type SessionV3StateSnapshotRequest,
  type SessionV3SyncSnapshot,
  type SessionV3SyncSubscriptionWire,
  type SessionV3WorksetRequestWire,
} from './types'
import type { DesktopState } from '../state/desktop-state'

const DEFAULT_WORKSET_ID = 'desktop-v3-runtime:global'
const DEFAULT_WORKSET_RECENT_LIMIT = 50
const RUNTIME_CLIENT_ID_STORAGE_KEY = 'swarm.desktop.session-v3.client-id'
const RUNTIME_CLIENT_ID_PREFIX = 'desktop-v3-runtime:'

export type SessionV3RuntimeApi = {
  bootstrapSessionV3Sync: typeof bootstrapSessionV3Sync
  hydrateSessionV3Sync: typeof hydrateSessionV3Sync
  streamSessionV3Sync: typeof streamSessionV3Sync
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

export interface DesktopSessionV3RuntimeBootOptions extends SessionV3RequestOptions, SessionV3StateSnapshotRequest {
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

export interface DesktopSessionV3RuntimeHydrateOptions extends DesktopSessionV3RuntimeBootOptions {
  replaceWanted?: boolean
}

interface DesktopSessionV3RuntimeResumeState {
  subscriptions: SessionV3SyncSubscriptionWire[]
  worksets: SessionV3RealtimeWorksetSubscriptionRequestWire[]
}

interface DesktopSessionV3RuntimeSyncLoad extends DesktopSessionV3RuntimeResumeState {
  result: SessionV3SyncSnapshot
}

export type DesktopSessionV3RuntimeCreateSessionInput = SessionV3CreateSessionInput

export class DesktopSessionV3Runtime {
  private state: SessionV3ReducerState
  private readonly listeners = new Set<DesktopSessionV3RuntimeListener>()
  private readonly api: SessionV3RuntimeApi
  private readonly transport: DesktopV3RealtimeTransport
  private readonly now: () => number
  private wantedSessionIds = new Set<string>()
  private workset: SessionV3WorksetRequestWire | null
  private bootInFlight: Promise<SessionV3ReducerState> | null = null
  private shutdownRequested = false

  constructor(options: DesktopSessionV3RuntimeOptions = {}) {
    this.now = options.now ?? (() => Date.now())
    this.state = options.initialState ?? createSessionV3ReducerInitialState(options.initialDesktopState)
    if (options.clientId?.trim()) {
      writeRuntimeClientId(options.clientId.trim())
    } else {
      runtimeClientId()
    }
    this.workset = options.workset === null ? null : options.workset ?? defaultRuntimeWorkset()
    this.api = {
      bootstrapSessionV3Sync: options.api?.bootstrapSessionV3Sync ?? bootstrapSessionV3Sync,
      hydrateSessionV3Sync: options.api?.hydrateSessionV3Sync ?? hydrateSessionV3Sync,
      streamSessionV3Sync: options.api?.streamSessionV3Sync ?? streamSessionV3Sync,
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
      onRehydrateRequested: async (reason, _frame, meta) => {
        const syncLoad = await this.loadSyncSnapshot({ reason, mode: 'merge' }, false, meta.at)
        return {
          endpointCursor: syncLoad.result.endpointCursor,
          subscriptions: syncLoad.subscriptions,
          worksets: syncLoad.worksets,
        }
      },
    })
    this.setWantedSessions([
      ...activeRuntimeSessionIds(this.state.desktop, options.initialDesktopState),
      ...(options.wantedSessionIds ?? []),
    ], { replace: true, subscribe: false })
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
    this.bootInFlight = this.loadSyncSnapshot({ ...options, mode: options.mode ?? 'replace' }, true)
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
    this.transport.resetForSyncSnapshot(options.reason ?? 'refresh')
    const syncLoad = await this.loadSyncSnapshot({ ...options, mode: options.mode ?? 'replace' }, false)
    if (this.shutdownRequested) return this.state
    this.transport.applySyncSnapshot(syncLoad.result, syncLoad)
    this.syncWantedSessionsToTransport()
    await this.transport.start()
    return this.state
  }

  async hydrateSessions(sessionIds: string[], options: DesktopSessionV3RuntimeHydrateOptions = {}): Promise<SessionV3ReducerState> {
    const normalizedSessionIds = normalizeSessionIds(sessionIds)
    if (normalizedSessionIds.length === 0) return this.state
    this.shutdownRequested = false
    this.setWantedSessions(normalizedSessionIds, { replace: options.replaceWanted, subscribe: false })
    const syncLoad = await this.loadSyncSnapshot({ ...options, sessionIds: normalizedSessionIds, mode: options.mode ?? 'merge' }, false)
    if (this.shutdownRequested) return this.state
    this.transport.applySyncSnapshot(syncLoad.result, syncLoad)
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

  private async loadSyncSnapshot(
    options: DesktopSessionV3RuntimeRefreshOptions,
    applyTransport: boolean,
    receivedAt = this.now(),
  ): Promise<DesktopSessionV3RuntimeSyncLoad> {
    const request = this.syncOptions(options)
    const result: SessionV3SyncSnapshot = (request.sessionIds ?? []).length > 0
      ? await this.api.hydrateSessionV3Sync(request, { signal: options.signal })
      : await this.api.bootstrapSessionV3Sync(request, { signal: options.signal })
    const resumeState = this.resumeStateForSyncSnapshot(result, options)
    const syncLoad = { result, ...resumeState }
    if (this.shutdownRequested) return syncLoad
    this.dispatch({
      type: 'sync-snapshot',
      result,
      subscriptions: resumeState.subscriptions,
      worksets: resumeState.worksets,
      mode: options.mode ?? 'merge',
      receivedAt,
    })
    if (applyTransport) {
      this.transport.applySyncSnapshot(result, resumeState)
      this.syncWantedSessionsToTransport()
    }
    return syncLoad
  }

  private resumeStateForSyncSnapshot(
    result: SessionV3SnapshotResult,
    options: DesktopSessionV3RuntimeBootOptions,
  ): DesktopSessionV3RuntimeResumeState {
    const endpointCursor = result.endpointCursor || result.snapshot.snapshotEndpointCursor || this.state.endpointCursor
    const request = this.syncOptions(options)
    const requestedSubscriptions = (request.sessionIds ?? [])
      .map((sessionId) => wantedSessionSubscription(sessionId, endpointCursor))
      .filter((subscription): subscription is SessionV3SyncSubscriptionWire => Boolean(subscription))
    const wantedSubscriptions = Array.from(this.wantedSessionIds)
      .map((sessionId) => wantedSessionSubscription(sessionId, endpointCursor))
      .filter((subscription): subscription is SessionV3SyncSubscriptionWire => Boolean(subscription))
    const workset = options.workset === null ? null : options.workset ?? this.workset
    const worksetSubscription = worksetSubscriptionFromWorkset(workset)
    return {
      subscriptions: mergeSubscriptions(requestedSubscriptions, wantedSubscriptions),
      worksets: worksetSubscription ? [worksetSubscription] : [],
    }
  }

  private syncOptions(options: DesktopSessionV3RuntimeBootOptions): SessionV3StateSnapshotRequest {
    const workset = options.workset === null ? null : options.workset ?? this.workset
    if (options.clientId?.trim()) {
      writeRuntimeClientId(options.clientId.trim())
    }
    return mergeSyncRequests(syncRequestFromWorkset(workset), options)
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
      .filter((subscription): subscription is SessionV3SyncSubscriptionWire => Boolean(subscription))
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

function syncRequestFromWorkset(workset: SessionV3WorksetRequestWire | null): SessionV3StateSnapshotRequest {
  const selector = workset?.selector ?? {}
  const workspacePaths = normalizeStringArray(workset?.workspace?.workspace_paths ?? selector.workspace_paths)
  const workspacePath = normalizeString(workset?.workspace?.workspace_path ?? selector.workspace_path)
  const sessionIds = normalizeSessionIds(workset?.session_ids ?? selector.session_ids)
  const recent = normalizeRecentRequest(workset?.recent ?? selector.recent)
  const selectorKind = normalizeString(selector.kind)
  const isWorkspaceSelector = selectorKind === 'workspace' || Boolean(workspacePath || workspacePaths.length > 0)
  const isExplicitSessionSelector = sessionIds.length > 0
  const isRecentSelector = !isExplicitSessionSelector && !isWorkspaceSelector && (selectorKind === 'recent' || Boolean(recent?.limit))
  const history = normalizeHistoryRequest(workset?.history)
  const resources = normalizeResourcesRequest(workset?.resources)
  if (isExplicitSessionSelector) {
    return {
      sessionIds,
      global: false,
      history,
      resources,
      includeActive: workset?.include_active,
    }
  }
  return {
    sessionIds,
    global: !isWorkspaceSelector && (workset?.global || selector.global || selectorKind === 'global' || !isRecentSelector),
    workspacePath: workspacePath || undefined,
    workspacePaths: workspacePaths.length > 0 ? workspacePaths : undefined,
    recent,
    history,
    resources,
    includeActive: workset?.include_active,
  }
}

function mergeSyncRequests(base: SessionV3StateSnapshotRequest, options: DesktopSessionV3RuntimeBootOptions): SessionV3StateSnapshotRequest {
  const sessionIds = normalizeSessionIds(options.sessionIds ?? base.sessionIds)
  const history = options.history ?? base.history
  const resources = options.resources ?? base.resources
  const includeActive = options.includeActive ?? base.includeActive
  const knownSessions = options.knownSessions ?? base.knownSessions
  if (sessionIds.length > 0) {
    return {
      sessionIds,
      global: false,
      history,
      resources,
      includeActive,
      knownSessions,
    }
  }
  const workspacePaths = normalizeStringArray(options.workspacePaths ?? base.workspacePaths)
  const workspacePath = normalizeString(options.workspacePath ?? base.workspacePath)
  return {
    ...base,
    sessionIds,
    global: options.global ?? base.global,
    workspacePath: workspacePath || undefined,
    workspacePaths: workspacePaths.length > 0 ? workspacePaths : undefined,
    recent: options.recent ?? base.recent,
    history,
    resources,
    includeActive,
    knownSessions,
  }
}

function normalizeRecentRequest(recent: SessionV3WorksetRequestWire['recent'] | NonNullable<SessionV3WorksetRequestWire['selector']>['recent'] | undefined): SessionV3StateSnapshotRequest['recent'] {
  if (!recent) return undefined
  return {
    limit: recent.limit,
    beforeUpdatedAt: recent.before_updated_at,
    beforeSessionId: recent.before_session_id,
  }
}

function normalizeHistoryRequest(history: SessionV3WorksetRequestWire['history'] | undefined): SessionV3StateSnapshotRequest['history'] {
  if (!history) return undefined
  return {
    mode: history.mode === 'tail' || history.mode === 'full' ? history.mode : 'none',
    maxMessagesPerSession: history.max_messages_per_session,
    maxEventsPerSession: history.max_events_per_session,
    manifestPolicy: history.manifest_policy === 'error' || history.manifest_policy === 'omit' ? history.manifest_policy : 'manifest',
    includeEvents: history.include_events,
  }
}

function normalizeResourcesRequest(resources: SessionV3WorksetRequestWire['resources'] | undefined): SessionV3StateSnapshotRequest['resources'] {
  if (!resources) return undefined
  return {
    messages: resources.messages,
    events: resources.events,
    runIntents: resources.run_intents,
    activePlan: resources.active_plan,
    planRevisions: resources.plan_revisions,
  }
}

function wantedSessionSubscription(sessionId: string, endpointCursor: string): SessionV3SyncSubscriptionWire | null {
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

function worksetSubscriptionFromWorkset(workset: SessionV3WorksetRequestWire | null): SessionV3RealtimeWorksetSubscriptionRequestWire | null {
  if (!workset) return null
  const worksetId = normalizeString(workset.workset_id) || DEFAULT_WORKSET_ID
  const selector = workset.selector ?? { kind: workset.global ? 'global' : 'recent', global: workset.global || undefined }
  const selectorKind = normalizeString(selector.kind)
  if (!worksetId || !selectorKind) return null
  return {
    workset_id: worksetId,
    subscription_id: `desktop-v3-runtime:workset:${worksetId}`,
    surface: 'desktop',
    selector: {
      ...selector,
      kind: selectorKind,
    },
    resources: workset.resources
      ? Object.entries(workset.resources)
          .filter(([, enabled]) => Boolean(enabled))
          .map(([resource]) => resource)
      : undefined,
    auto_subscribe_sessions: workset.auto_subscribe_sessions !== false,
  }
}

function activeRuntimeSessionIds(...desktops: Array<DesktopState | null | undefined>): string[] {
  const sessionIds = new Set<string>()
  for (const desktop of desktops) {
    if (!desktop) continue
    for (const [sessionId, runIntent] of Object.entries(desktop.runIntentsBySessionId)) {
      const normalizedSessionId = normalizeString(sessionId)
      const status = normalizeString(runIntent?.status).toLowerCase()
      if (normalizedSessionId && status !== 'completed' && status !== 'failed' && status !== 'cancelled' && status !== 'expired') {
        sessionIds.add(normalizedSessionId)
      }
    }
    for (const session of Object.values(desktop.sessionsById)) {
      const sessionId = normalizeString(session.id)
      if (!sessionId) continue
      const runIntent = desktop.runIntentsBySessionId[sessionId] ?? session.runIntent ?? null
      const status = normalizeString(runIntent?.status).toLowerCase()
      const liveStatus = normalizeString(session.live.status).toLowerCase()
      if ((runIntent && status !== 'completed' && status !== 'failed' && status !== 'cancelled' && status !== 'expired') || (liveStatus && liveStatus !== 'idle')) {
        sessionIds.add(sessionId)
      }
    }
  }
  return Array.from(sessionIds)
}

function normalizeSessionIds(sessionIds: readonly string[] | null | undefined): string[] {
  return uniqueStrings(sessionIds ?? [])
}

function normalizeStringArray(values: readonly string[] | null | undefined): string[] {
  return uniqueStrings(values ?? [])
}

function uniqueStrings(values: readonly string[]): string[] {
  const seen = new Set<string>()
  const output: string[] = []
  for (const value of values) {
    const normalized = normalizeString(value)
    if (!normalized || seen.has(normalized)) continue
    seen.add(normalized)
    output.push(normalized)
  }
  return output
}

function mergeSubscriptions(
  base: SessionV3SyncSubscriptionWire[],
  extra: SessionV3SyncSubscriptionWire[],
): SessionV3SyncSubscriptionWire[] {
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

function responseSessionId(response: SessionV3CreateSessionResponseWire | SessionV3HydratedSessionResponseWire): string {
  const hydrated = response as SessionV3HydratedSessionResponseWire
  return normalizeString(response.session_id)
    || normalizeString(response.session?.id)
    || normalizeString(response.projection?.session_id)
    || normalizeString(hydrated.active_run_intent?.session_id)
    || normalizeString(hydrated.run_intent?.session_id)
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
