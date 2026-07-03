import { ensureDesktopSession } from '../../../app/api'
import { queryClient } from '../../../app/query-client'
import { gitStatusQueryKey } from '../git/api'
import type { GitSnapshot, GitStatusRealtimePayload } from '../git/types'
import { DesktopV3RealtimeTransport, type DesktopV3RealtimeTransportStatus } from '../session-v3/transport'
import type { SessionV3RealtimeWorksetSubscriptionRequestWire } from '../session-v3/types'
import { DesktopV3LivePatchCoordinator, createDefaultDesktopV3LivePatchCoordinatorDeps } from './v3-live-patch-coordinator'
import { openDesktopV3RealtimeTransportSocket } from './client'
import { bootstrapDesktopV3SidebarMetadataOnly } from '../state/desktop-v3-bootstrap-controller'
import { desktopV3CacheReducer } from '../state/desktop-v3-cache-reducer'
import { dispatchDesktopV3Cache, getDesktopV3CacheSnapshot, commitDesktopV3CacheSnapshot, subscribeDesktopV3Cache, type DesktopV3CacheMutation } from '../state/desktop-v3-cache-store'
import { realtimeFrameToActions } from '../state/desktop-v3-cache-wire'
import {
  postDesktopV3Reconnect,
  type DesktopV3ReconnectInput,
} from '../state/desktop-v3-sync-api'
import type {
  DesktopV3CacheAction,
  DesktopV3CacheState,
  SyncSelector,
  RealtimeCache,
  RealtimeMessage,
  RealtimeSubscriptionRequest,
  RealtimeWorksetSubscriptionRequest,
  SessionsReconnectResponse,
  V3SessionRunIntent,
} from '../state/desktop-v3-cache-types'

const DESKTOP_V3_CLIENT_ID = `desktop:${crypto.randomUUID()}`
const ACTIVE_INTENT_STATUSES = new Set(['pending_executor', 'running', 'dispatch_blocked'])
export const DESKTOP_V3_LIVE_PATCH_ENABLED = true

export interface DesktopV3RealtimeController {
  ensureSessionConnected(sessionId: string): Promise<void>
  start(preferredSessionId?: string | null, bootstrapReady?: Promise<unknown>): Promise<void>
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
  ensureSession?: () => Promise<unknown>
  bootstrap?: (input?: { preferredSessionId?: string | null }) => Promise<unknown>
  openSocket?: (input: { endpointCursor: string }) => WebSocket | Promise<WebSocket>
  commitSnapshot?: (previousState: DesktopV3CacheState, nextState: DesktopV3CacheState, actions: DesktopV3CacheAction[]) => void
  streamCommit?: DesktopV3StreamCommitController
}

export class DesktopV3RealtimeControllerRuntime implements DesktopV3RealtimeController {
  private readonly getSnapshot: () => DesktopV3CacheState
  private readonly dispatch: (action: DesktopV3CacheAction) => void
  private readonly subscribe: (listener: (mutation?: DesktopV3CacheMutation) => void) => () => void
  private readonly reconnect: (input: DesktopV3ReconnectInput) => Promise<SessionsReconnectResponse>
  private readonly ensureSessionIdentity: () => Promise<unknown>
  private readonly bootstrap: (input?: { preferredSessionId?: string | null }) => Promise<unknown>
  private readonly transport: DesktopV3RealtimeTransport
  private readonly streamCommit: DesktopV3StreamCommitController
  private readonly livePatchCoordinator: DesktopV3LivePatchCoordinator
  private readonly subscribingSessionIds = new Set<string>()
  private firstResumeSent?: Deferred<void>
  private startupCancellation?: Deferred<never>
  private unsubscribeCache?: () => void
  private startPromise?: Promise<void>
  private stopped = false

  constructor(deps: DesktopV3RealtimeControllerDeps = {}) {
    this.getSnapshot = deps.getSnapshot ?? getDesktopV3CacheSnapshot
    this.dispatch = deps.dispatch ?? dispatchDesktopV3Cache
    this.subscribe = deps.subscribe ?? subscribeDesktopV3Cache
    this.reconnect = deps.reconnect ?? postDesktopV3Reconnect
    this.ensureSessionIdentity = deps.ensureSession ?? ensureDesktopSession
    this.bootstrap = deps.bootstrap ?? (async (input) => {
      await bootstrapDesktopV3SidebarMetadataOnly({
        preferredSessionId: input?.preferredSessionId,
      })
    })
    this.streamCommit = deps.streamCommit ?? new DesktopV3StreamCommitController({
      getSnapshot: this.getSnapshot,
      dispatch: this.dispatch,
      commitSnapshot: deps.commitSnapshot
        ?? (deps.dispatch ? undefined : commitDesktopV3CacheSnapshot),
    })

    this.livePatchCoordinator = new DesktopV3LivePatchCoordinator(createDefaultDesktopV3LivePatchCoordinatorDeps({
      getSnapshot: this.getSnapshot,
      commitSnapshot: deps.commitSnapshot ?? commitDesktopV3CacheSnapshot,
    }))

    this.transport = new DesktopV3RealtimeTransport({
      getEndpointCursor: () => this.getSnapshot().realtime.endpointCursor,
      openSocket: deps.openSocket ?? (({ endpointCursor }) => openDesktopV3RealtimeTransportSocket({ endpointCursor })),
      livePatchEnabled: DESKTOP_V3_LIVE_PATCH_ENABLED,
      onLivePatch: ({ patch, generation }) => this.livePatchCoordinator.accept(patch, generation),
      onFrame: ({ frame }) => this.handleFrame(frame as RealtimeMessage),
      onStatus: ({ status, reason }) => {
        this.dispatch({
          type: 'realtime.statusChanged',
          status: mapTransportStatus(status),
          error: status === 'error' || status === 'stale' ? reason : undefined,
        })
      },
      onResumeSent: () => this.handleResumeSent(),
      onRehydrateRequested: async (_reason, frame) => {
        if ((frame as { bootstrap_required?: boolean } | null)?.bootstrap_required) {
          await this.bootstrap({
            preferredSessionId: this.getSnapshot().selectedSessionId,
          })
        }
        const reconnect = await this.reconnect(
          buildDesktopV3ReconnectInput(this.getSnapshot(), DESKTOP_V3_CLIENT_ID),
        )
        const cursorWasRejected = frame?.kind === 'cursor.error'
        const durableResumeCursor = cursorWasRejected
          ? undefined
          : this.getSnapshot().realtime.endpointCursor?.trim()
        this.livePatchCoordinator.resetGeneration(0)
        this.dispatch({ type: 'reconnect.applySnapshot', snapshot: reconnect })
        const resume = this.buildResume(
          reconnect,
          this.getSnapshot().selectedSessionId,
          durableResumeCursor,
        )
        this.dispatch({
          type: 'realtime.storeResume',
          streamPath: reconnect.realtime?.stream_path ?? '/v3/realtime/stream',
          resume,
        })

        return {
          endpointCursor: resume.endpoint_cursor,
          snapshotEndpointCursor: reconnect.snapshot_endpoint_cursor,
          subscriptions: resume.subscriptions,
          worksets: normalizeTransportWorksets(resume.worksets),
        }
      },
    })
  }

  start(preferredSessionId?: string | null, bootstrapReady?: Promise<unknown>): Promise<void> {
    if (this.startPromise) return this.startPromise
    this.stopped = false
    this.startupCancellation = createDeferred<never>()
    this.startupCancellation.promise.catch(() => undefined)
    this.firstResumeSent = createDeferred<void>()
    const startPromise = this.startUncached(preferredSessionId, bootstrapReady).finally(() => {
      if (this.startPromise === startPromise) {
        this.startupCancellation = undefined
      }
    })
    this.startPromise = startPromise
    return this.startPromise
  }

  ensureSessionConnected(sessionId: string): Promise<void> {
    const normalized = sessionId.trim()
    if (!normalized) return Promise.resolve()
    return this.subscribeSessionRealtime(normalized)
  }

  stop(reason = 'Desktop V3 realtime controller stopped'): void {
    this.stopped = true
    const stopError = new Error(reason)
    this.startupCancellation?.reject(stopError)
    this.startupCancellation = undefined
    this.unsubscribeCache?.()
    this.unsubscribeCache = undefined
    this.startPromise = undefined
    this.subscribingSessionIds.clear()
    this.firstResumeSent?.resolve()
    this.firstResumeSent = undefined
    this.livePatchCoordinator.dispose()
    this.transport.stop(reason)
  }

  currentEndpointCursor(): string {
    const cursor = this.getSnapshot().realtime.endpointCursor?.trim()
    if (!cursor) {
      throw new Error('Desktop V3 realtime has no durable endpoint cursor')
    }
    return cursor
  }

  private async subscribeSessionRealtime(sessionIdInput: string): Promise<void> {
    const sessionId = sessionIdInput.trim()
    if (!sessionId) {
      throw new Error('Desktop V3 realtime session subscription requires sessionId')
    }
    this.subscribingSessionIds.add(sessionId)
    try {
      await this.transport.subscribeSession({
        session_id: sessionId,
        subscription_id: `${DESKTOP_V3_CLIENT_ID}:session:${sessionId}`,
        endpoint_cursor: this.currentEndpointCursor(),
      })
    } finally {
      this.subscribingSessionIds.delete(sessionId)
      setTimeout(() => {
        if (!this.stopped) this.reconcileDesiredSessionConnections()
      }, 0)
    }
  }

  private async startUncached(preferredSessionId?: string | null, bootstrapReady?: Promise<unknown>): Promise<void> {
    await this.awaitUnlessStopped(this.ensureSessionIdentity())
    this.assertNotStopped()

    await this.waitForSidebarBootstrap(preferredSessionId, bootstrapReady)
    this.assertNotStopped()

    const initial = buildDesktopV3InitialRealtimeResume(
      this.getSnapshot(),
      DESKTOP_V3_CLIENT_ID,
      preferredSessionId,
    )

    this.dispatch({
      type: 'realtime.storeResume',
      streamPath: '/v3/realtime/stream',
      resume: initial.resume,
    })

    this.transport.setEndpointCursor(initial.endpointCursor, 'snapshot')
    this.transport.setWorksets(normalizeTransportWorksets(initial.worksets), { replace: true })
    this.transport.setSessions(initial.subscriptions, { replace: true })

    this.unsubscribeCache?.()
    this.unsubscribeCache = this.subscribe(() => {
      this.reconcileDesiredSessionConnections()
    })
    this.reconcileDesiredSessionConnections()

    await this.awaitUnlessStopped(this.transport.start())
    await this.waitForFirstResumeSent()
    this.assertNotStopped()

  }

  private async waitForSidebarBootstrap(preferredSessionId?: string | null, bootstrapReady?: Promise<unknown>): Promise<void> {
    if (bootstrapReady) {
      await this.awaitUnlessStopped(bootstrapReady)
      return
    }
    if (this.getSnapshot().desktopSidebarBootstrap.scopeId?.trim()) return
    await this.awaitUnlessStopped(this.bootstrap({
      preferredSessionId,
    }))
  }

  private async awaitUnlessStopped<T>(promise: Promise<T>): Promise<T> {
    const cancellation = this.startupCancellation?.promise
    if (!cancellation) return promise
    return Promise.race([promise, cancellation])
  }

  private assertNotStopped(): void {
    if (this.stopped) {
      throw new Error('Desktop V3 realtime controller startup was cancelled')
    }
  }

  private buildResume(
    reconnect: SessionsReconnectResponse,
    preferredSessionId?: string | null,
    durableResumeCursor?: string,
  ): NonNullable<SessionsReconnectResponse['realtime']>['resume'] {
    if (!reconnect.realtime?.resume) {
      throw new Error('Desktop V3 reconnect response is missing realtime.resume')
    }

    const resume = structuredClone(reconnect.realtime.resume)
    const endpointCursor = durableResumeCursor?.trim() || reconnect.snapshot_endpoint_cursor
    resume.endpoint_cursor = endpointCursor
    resume.subscriptions = (resume.subscriptions ?? []).map((subscription) => ({
      ...subscription,
      endpoint_cursor: endpointCursor,
    }))

    const selectedSessionId = preferredSessionId === null
      ? undefined
      : preferredSessionId?.trim() || this.getSnapshot().selectedSessionId?.trim()
    const subscriptions = new Map<string, RealtimeSubscriptionRequest>()
    for (const subscription of resume.subscriptions ?? []) {
      const sessionId = subscription.session_id?.trim()
      if (!sessionId || subscriptions.has(sessionId)) continue
      subscriptions.set(sessionId, {
        ...subscription,
        session_id: sessionId,
        endpoint_cursor: endpointCursor,
      })
    }

    const addRecoverySubscription = (sessionId: string | null | undefined) => {
      const normalized = sessionId?.trim()
      if (!normalized || subscriptions.has(normalized)) return
      subscriptions.set(normalized, {
        session_id: normalized,
        subscription_id: `${DESKTOP_V3_CLIENT_ID}:session:${normalized}`,
        endpoint_cursor: endpointCursor,
      })
    }

    const state = this.getSnapshot()
    addRecoverySubscription(selectedSessionId)
    for (const sessionId of activeRealtimeSessionIds(state)) {
      addRecoverySubscription(sessionId)
    }
    for (const sessionId of taskChildRealtimeSessionIds(state)) {
      addRecoverySubscription(sessionId)
    }
    for (const pending of Object.values(state.pendingUserByClientRequestId)) {
      if (pending.status !== 'pending') continue
      addRecoverySubscription(pending.sessionId)
    }
    for (const sessionId of this.subscribingSessionIds) {
      addRecoverySubscription(sessionId)
    }

    resume.subscriptions = Array.from(subscriptions.values())
    return resume
  }

  private async waitForFirstResumeSent(): Promise<void> {
    const firstResumeSent = this.firstResumeSent
    if (!firstResumeSent) return
    await this.awaitUnlessStopped(firstResumeSent.promise)
  }

  private handleResumeSent(): void {
    this.firstResumeSent?.resolve()
    this.firstResumeSent = undefined
  }

  private async handleFrame(frame: RealtimeMessage): Promise<void> {
    try {
      this.livePatchCoordinator.beforeDurableFrame(frame)
      await commitDesktopV3StreamFrame(this.streamCommit, frame)
      this.applyGitStatusRealtimeFrame(frame)
      this.livePatchCoordinator.afterDurableFrame(frame)
      if (frame.kind === 'event' || frame.kind === 'workset.session.discovered' || frame.kind === 'workset.session.updated' || frame.kind === 'workset.session.removed') {
        // Reconcile after durable state commits. Workset frames update transport
        // discovery state, and task tool event frames can expose delegated child
        // V3 session IDs that must become direct session subscriptions.
        setTimeout(() => {
          if (!this.stopped) this.reconcileDesiredSessionConnections()
        }, 0)
      }
    } catch (error) {
      this.livePatchCoordinator.resetGeneration(0)
      this.handleDurableStreamCommitFailure(error)
      throw error
    }
  }

  private applyGitStatusRealtimeFrame(frame: RealtimeMessage): void {
    const payload = gitStatusRealtimePayloadFromFrame(frame)
    if (!payload) return
    const workspacePath = payload.workspace_path.trim()
    if (!workspacePath) return
    const response = { ok: true as const, status: payload.status }
    queryClient.setQueryData(gitStatusQueryKey(workspacePath), response)
    const snapshotWorkspacePath = payload.status.workspace_path?.trim()
    if (snapshotWorkspacePath && snapshotWorkspacePath !== workspacePath) {
      queryClient.setQueryData(gitStatusQueryKey(snapshotWorkspacePath), response)
    }
  }

  private handleDurableStreamCommitFailure(error: unknown): void {
    const message = error instanceof Error ? error.message : String(error)

    this.dispatch({
      type: 'realtime.statusChanged',
      status: 'stale',
      errorCode: 'runtime_store_commit_failed',
      error: message,
    })

    this.transport.reopenFromDurableCursor('Desktop V3 runtime store commit failed')
  }

  private reconcileDesiredSessionConnections(): void {
    if (this.stopped) return
    const state = this.getSnapshot()
    const desired = new Set<string>()
    const addDesired = (sessionId: string | null | undefined) => {
      const normalized = sessionId?.trim()
      if (normalized) desired.add(normalized)
    }

    addDesired(state.selectedSessionId)

    for (const sessionId of activeRealtimeSessionIds(state)) {
      addDesired(sessionId)
    }

    for (const sessionId of taskChildRealtimeSessionIds(state)) {
      addDesired(sessionId)
    }

    for (const pending of Object.values(state.pendingUserByClientRequestId)) {
      if (pending.status === 'pending') addDesired(pending.sessionId)
    }

    for (const sessionId of this.subscribingSessionIds) {
      addDesired(sessionId)
    }

    const diagnostics = this.transport.diagnostics()
    if (diagnostics.status === 'rehydrating' || diagnostics.status === 'stale') return

    const registered = new Map(diagnostics.sessions.map((session) => [session.session_id, session]))

    for (const sessionId of desired) {
      if (registered.has(sessionId) || this.subscribingSessionIds.has(sessionId)) continue
      const subscribe = this.subscribeSessionRealtime(sessionId)
      subscribe.catch((error) => {
        if (!this.stopped) {
          console.error('[desktop-v3] desired session subscription failed', error)
        }
      })
    }

    for (const sessionId of registered.keys()) {
      if (desired.has(sessionId)) continue
      this.transport.unsubscribeSession(sessionId)
    }
  }

}

export function commitDesktopV3StreamFrame(
  streamCommit: DesktopV3StreamCommitController,
  frame: RealtimeMessage,
): Promise<void> {
  return streamCommit.commitActions(realtimeFrameToActions(frame))
}

export class DesktopV3StreamCommitError extends Error {
  constructor(message: string) {
    super(message)
    this.name = 'DesktopV3StreamCommitError'
  }
}

interface DesktopV3StreamCommitControllerDeps {
  getSnapshot: () => DesktopV3CacheState
  dispatch: (action: DesktopV3CacheAction) => void
  commitSnapshot?: (previousState: DesktopV3CacheState, nextState: DesktopV3CacheState, actions: DesktopV3CacheAction[]) => void
}


export class DesktopV3StreamCommitController {
  constructor(private readonly deps: DesktopV3StreamCommitControllerDeps) {}

  commitActions(actions: DesktopV3CacheAction[]): Promise<void> {
    if (actions.length === 0) return Promise.resolve()

    const previousState = this.deps.getSnapshot()
    const nextState = reduceDesktopV3CacheActions(previousState, actions)
    if (this.deps.commitSnapshot) {
      this.deps.commitSnapshot(previousState, nextState, actions)
    } else {
      for (const action of actions) this.deps.dispatch(action)
    }
    return Promise.resolve()
  }
}


export function reduceDesktopV3CacheActions(
  state: DesktopV3CacheState,
  actions: DesktopV3CacheAction[],
): DesktopV3CacheState {
  let nextState: DesktopV3CacheState = { ...state }
  for (const action of actions) {
    nextState = desktopV3CacheReducer(nextState, action)
  }
  return nextState
}

export function buildDesktopV3InitialRealtimeResume(
  state: DesktopV3CacheState,
  clientId: string,
  preferredSessionId?: string | null,
): {
  endpointCursor: string
  subscriptions: RealtimeSubscriptionRequest[]
  worksets: RealtimeWorksetSubscriptionRequest[]
  resume: RealtimeMessage
} {
  const sidebarScopeId = state.desktopSidebarBootstrap.scopeId?.trim()
  if (!sidebarScopeId) {
    throw new Error('Desktop V3 initial realtime requires the bootstrapped sidebar scope')
  }

  const sidebarScope = state.syncScopesById[sidebarScopeId]
  if (!sidebarScope) {
    throw new Error(`Desktop V3 initial realtime missing scope ${sidebarScopeId}`)
  }

  const endpointCursor = sidebarScope.endpointCursor?.trim()
  if (!endpointCursor) {
    throw new Error('Desktop V3 initial realtime requires the bootstrap endpoint cursor')
  }

  const selectedSessionId = preferredSessionId === null
    ? undefined
    : preferredSessionId?.trim() || state.selectedSessionId?.trim()
  const activeSessionIDs = activeRealtimeSessionIds(state)
  const taskChildSessionIDs = taskChildRealtimeSessionIds(state)

  const sessionOrder = state.sessionOrderByScope[sidebarScopeId] ?? []
  const orderedSessionIDs: string[] = []
  const seen = new Set<string>()
  const append = (sessionId: string | null | undefined) => {
    const normalized = sessionId?.trim()
    if (!normalized || seen.has(normalized)) return
    seen.add(normalized)
    orderedSessionIDs.push(normalized)
  }

  append(selectedSessionId)
  for (const sessionId of sessionOrder) {
    const normalized = sessionId.trim()
    if (activeSessionIDs.has(normalized)) append(normalized)
  }
  for (const sessionId of [...activeSessionIDs].filter((sessionId) => !seen.has(sessionId)).sort()) {
    append(sessionId)
  }
  for (const sessionId of [...taskChildSessionIDs].filter((sessionId) => !seen.has(sessionId)).sort()) {
    append(sessionId)
  }

  const subscriptions = orderedSessionIDs.map((sessionId) => ({
    session_id: sessionId,
    subscription_id: `${clientId}:session:${sessionId}`,
    endpoint_cursor: endpointCursor,
  }))
  const worksets: RealtimeWorksetSubscriptionRequest[] = [{
    workset_id: sidebarScopeId,
    subscription_id: `${clientId}:workset:${sidebarScopeId}`,
    surface: 'desktop',
    selector: cloneDesktopV3SyncSelector(sidebarScope.selector),
    resources: ['membership', 'projections', 'current_run_state', 'permission_summaries', 'notifications', 'notification_summary', 'sessions', 'tombstones'],
    auto_subscribe_sessions: false,
  }]
  const resume: RealtimeMessage = {
    protocol: 'v3.realtime',
    protocol_version: 1,
    kind: 'resume',
    endpoint_cursor: endpointCursor,
    subscriptions,
    worksets,
  }

  return { endpointCursor, subscriptions, worksets, resume }
}

export function activeRealtimeSessionIds(state: DesktopV3CacheState): Set<string> {
  const activeSessionIDs = new Set<string>()
  for (const [sessionId, intent] of Object.entries(state.currentRunIntentBySession)) {
    if (!intent) continue
    if (!ACTIVE_INTENT_STATUSES.has(intent.status.trim().toLowerCase())) continue
    const normalized = intent.session_id?.trim() || sessionId.trim()
    if (normalized) activeSessionIDs.add(normalized)
  }
  return activeSessionIDs
}

export function taskChildRealtimeSessionIds(state: DesktopV3CacheState): Set<string> {
  const childSessionIDs = new Set<string>()

  for (const runs of Object.values(state.liveRunsBySession)) {
    for (const run of Object.values(runs)) {
      const parentRunActive = ACTIVE_INTENT_STATUSES.has(run.status.trim().toLowerCase())
      for (const tool of Object.values(run.toolCallsByCallId)) {
        if (tool.taskStream) collectTaskStreamChildSessionIds(tool.taskStream, childSessionIDs, parentRunActive)
        const payload = parseTaskPayloadFromToolText(tool.outputText)
        if (payload) collectTaskPayloadChildSessionIds(payload, childSessionIDs, parentRunActive)
      }
    }
  }

  for (const record of Object.values(state.sessionsById)) {
    if (record.kind !== 'full') continue
    const taskLaunches = realtimeRecordValue(record.session.metadata?.task_launches)
    if (!taskLaunches) continue
    for (const launch of Object.values(taskLaunches)) {
      const payload = realtimeRecordValue(launch)
      if (payload) collectTaskPayloadChildSessionIds(payload, childSessionIDs, false)
    }
  }

  for (const list of Object.values(state.messagesBySession)) {
    for (const message of list.items) {
      const payload = parseTaskPayloadFromToolText(message.content)
      if (payload) collectTaskPayloadChildSessionIds(payload, childSessionIDs, false)
    }
  }

  return childSessionIDs
}

function parseTaskPayloadFromToolText(text: string | undefined): Record<string, unknown> | undefined {
  const direct = parseRealtimeJsonRecord(text)
  if (!direct) return undefined
  if (isTaskPayloadRecord(direct)) return direct

  const wrappedTool = firstRealtimeString(direct.tool, direct.tool_name)
  if (wrappedTool.trim().toLowerCase() !== 'task') return undefined
  for (const key of ['output', 'completed_output']) {
    const nested = parseRealtimeJsonRecord(realtimeStringValue(direct[key]))
    if (nested && isTaskPayloadRecord(nested)) return nested
  }
  return undefined
}

function isTaskPayloadRecord(value: Record<string, unknown>): boolean {
  const tool = realtimeStringValue(value.tool).trim().toLowerCase()
  const pathID = realtimeStringValue(value.path_id)
  if (tool !== 'task') return false
  return pathID === 'tool.task.stream.v1' || pathID === 'tool.task.v1'
}

function collectTaskPayloadChildSessionIds(payload: Record<string, unknown>, childSessionIDs: Set<string>, includeTerminal: boolean): void {
  if (!includeTerminal && !taskPayloadHasActiveLaunch(payload)) return
  addRealtimeSessionID(childSessionIDs, payload.child_session_id)
  addRealtimeSessionID(childSessionIDs, payload.session_id)
  for (const launch of realtimeRecordArray(payload.launches)) {
    addRealtimeSessionID(childSessionIDs, launch.child_session_id)
    addRealtimeSessionID(childSessionIDs, launch.session_id)
  }
}

function collectTaskStreamChildSessionIds(
  taskStream: NonNullable<DesktopV3CacheState['liveRunsBySession'][string][string]['toolCallsByCallId'][string]['taskStream']>,
  childSessionIDs: Set<string>,
  includeTerminal: boolean,
): void {
  for (const launchKey of taskStream.launchOrder) {
    const launch = taskStream.launchesByKey[launchKey]
    if (!launch) continue
    if (!includeTerminal && isTerminalTaskStatus(realtimeStringValue(launch.status))) continue
    addRealtimeSessionID(childSessionIDs, launch.child_session_id)
    addRealtimeSessionID(childSessionIDs, launch.session_id)
  }
}

function taskPayloadHasActiveLaunch(payload: Record<string, unknown>): boolean {
  const topLevelStatus = realtimeStringValue(payload.status)
  if (topLevelStatus && isTerminalTaskStatus(topLevelStatus)) return false
  const launches = realtimeRecordArray(payload.launches)
  if (launches.length > 0) {
    return launches.some((launch) => !isTerminalTaskStatus(realtimeStringValue(launch.status)))
  }
  return !topLevelStatus || !isTerminalTaskStatus(topLevelStatus)
}

function isTerminalTaskStatus(status: string): boolean {
  switch (status.trim().toLowerCase()) {
    case 'ok':
    case 'done':
    case 'success':
    case 'completed':
    case 'complete':
    case 'error':
    case 'failed':
    case 'cancelled':
    case 'canceled':
      return true
    default:
      return false
  }
}

function addRealtimeSessionID(sessionIDs: Set<string>, value: unknown): void {
  const sessionID = realtimeStringValue(value).trim()
  if (sessionID) sessionIDs.add(sessionID)
}

function firstRealtimeString(...values: unknown[]): string {
  for (const value of values) {
    const text = realtimeStringValue(value).trim()
    if (text) return text
  }
  return ''
}

function realtimeStringValue(value: unknown): string {
  return typeof value === 'string' ? value : ''
}

function gitStatusRealtimePayloadFromFrame(frame: RealtimeMessage): GitStatusRealtimePayload | undefined {
  if (frame.kind !== 'event') return undefined
  const eventType = frame.event_type || frame.event?.event_type || ''
  if (eventType !== 'workspace.git.status.updated') return undefined
  const raw = frame.event?.payload ?? frame.payload
  const payload = typeof raw === 'string'
    ? parseRealtimeJsonRecord(raw)
    : realtimeRecordValue(raw)
  if (!payload) return undefined
  const workspacePath = realtimeStringValue(payload.workspace_path).trim()
  const status = gitSnapshotValue(payload.status)
  if (!workspacePath || !status) return undefined
  return { workspace_path: workspacePath, status }
}

function gitSnapshotValue(value: unknown): GitSnapshot | undefined {
  const snapshot = realtimeRecordValue(value)
  if (!snapshot) return undefined
  if (!realtimeStringValue(snapshot.workspace_path).trim()) return undefined
  if (!Array.isArray(snapshot.files)) return undefined
  return snapshot as unknown as GitSnapshot
}

function realtimeRecordValue(value: unknown): Record<string, unknown> | undefined {
  return value && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : undefined
}

function realtimeRecordArray(value: unknown): Record<string, unknown>[] {
  return Array.isArray(value)
    ? value.map((item) => realtimeRecordValue(item)).filter((item): item is Record<string, unknown> => Boolean(item))
    : []
}

function parseRealtimeJsonRecord(value: string | undefined): Record<string, unknown> | undefined {
  const trimmed = value?.trim() ?? ''
  if (!trimmed.startsWith('{') || !trimmed.endsWith('}')) return undefined
  try {
    return realtimeRecordValue(JSON.parse(trimmed) as unknown)
  } catch {
    return undefined
  }
}

export function cloneDesktopV3SyncSelector(selector: SyncSelector): SyncSelector {
  const clone: SyncSelector = { ...selector }
  if (selector.workspace_paths) clone.workspace_paths = [...selector.workspace_paths]
  if (selector.session_ids) clone.session_ids = [...selector.session_ids]
  if (selector.recent) clone.recent = { ...selector.recent }
  if (selector.attention) clone.attention = { ...selector.attention }
  return clone
}

export function isDesktopV3BoundedSidebarSelector(selector: SyncSelector): boolean {
  const kind = selector.kind?.trim().toLowerCase()
  if (kind === 'global' && selector.global && !selector.recent?.limit && !selector.session_ids?.length && !selector.workspace_path?.trim() && !selector.workspace_paths?.length) {
    return false
  }
  return Boolean(selector.recent?.limit || selector.session_ids?.length || selector.workspace_path?.trim() || selector.workspace_paths?.length || kind === 'workspace' || kind === 'session_ids' || kind === 'recent' || kind === 'tui')
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

  if (!isDesktopV3BoundedSidebarSelector(sidebarScope.selector)) {
    throw new Error('Desktop V3 reconnect requires a bounded sidebar selector')
  }

  return {
    surface: 'desktop',
    client_id: clientId,
    workset: {
      workset_id: sidebarScopeId,
      selector: cloneDesktopV3SyncSelector(sidebarScope.selector),
      history: {
        mode: 'none',
      },
      resources: {
        messages: false,
        events: false,
        run_intents: false,
        current_run_state: true,
        permission_summaries: true,
        notifications: true,
        notification_summary: true,
        active_plan: true,
        plan_revisions: false,
      },
      include_active: true,
      auto_subscribe_sessions: false,
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
  ownerKey?: string
  preferredSessionId?: string | null
  bootstrap?: Promise<unknown>
} = {}): DesktopV3RealtimeLease {
  const ownerKey = input.ownerKey?.trim()
  let retained = retainedDesktopV3RealtimeController
  if (retained) {
    clearPendingDesktopV3RealtimeRelease(retained)
  }

  if (!retained) {
    const controller = desktopV3RealtimeControllerFactory()
    const generation = desktopV3RealtimeGeneration
    const created: RetainedDesktopV3RealtimeController = {
      controller,
      ready: Promise.resolve(),
      ownerTokens: new Map<string, symbol>(),
      anonymousRetainCount: 0,
    }
    const bootstrapReady = input.bootstrap?.then((value) => {
      if (retainedDesktopV3RealtimeController !== created || retainedDesktopV3RealtimeCount(created) <= 0) {
        throw new Error('Desktop V3 realtime lease released')
      }
      return value
    })
    created.ready = controller.start(input.preferredSessionId, bootstrapReady).catch((error) => {
      const stillCurrent = retainedDesktopV3RealtimeController === created && desktopV3RealtimeGeneration === generation
      if (stillCurrent) {
        controller.stop('Desktop V3 realtime controller startup failed')
        if (retainedDesktopV3RealtimeCount(created) <= 0) {
          retainedDesktopV3RealtimeController = undefined
          desktopV3RealtimeGeneration += 1
        }
      }
      throw error
    })
    retained = created
    retainedDesktopV3RealtimeController = created
  }

  const ownerToken = ownerKey ? Symbol(ownerKey) : undefined
  if (ownerKey && ownerToken) {
    retained.ownerTokens.set(ownerKey, ownerToken)
  } else {
    retained.anonymousRetainCount += 1
  }

  let released = false
  return {
    ready: retained.ready,
    release: () => {
      if (released) return
      released = true
      const current = retainedDesktopV3RealtimeController
      if (current !== retained) return
      if (ownerKey) {
        if (ownerToken && current.ownerTokens.get(ownerKey) === ownerToken) {
          current.ownerTokens.delete(ownerKey)
        }
      } else if (current.anonymousRetainCount > 0) {
        current.anonymousRetainCount -= 1
      }
      releaseDesktopV3RealtimeControllerIfUnused(
        current,
        'Desktop V3 realtime lease released',
        Boolean(ownerKey),
      )
    },
  }
}

export async function requireDesktopV3RealtimeControllerReady(): Promise<DesktopV3RealtimeController> {
  const retained = retainedDesktopV3RealtimeController
  if (!retained || retainedDesktopV3RealtimeCount(retained) <= 0) {
    throw new Error('Desktop V3 realtime controller is not retained by the desktop page')
  }
  await retained.ready
  return retained.controller
}

export function resetDesktopV3RealtimeControllerForTests(): void {
  if (retainedDesktopV3RealtimeController) {
    clearPendingDesktopV3RealtimeRelease(retainedDesktopV3RealtimeController)
    retainedDesktopV3RealtimeController.controller.stop('Desktop V3 realtime controller reset')
  }
  retainedDesktopV3RealtimeController = undefined
  desktopV3RealtimeGeneration += 1
  desktopV3RealtimeControllerFactory = () => new DesktopV3RealtimeControllerRuntime()
}

export function setDesktopV3RealtimeControllerFactoryForTests(factory: () => DesktopV3RealtimeControllerRuntime): void {
  resetDesktopV3RealtimeControllerForTests()
  desktopV3RealtimeControllerFactory = factory
}

let retainedDesktopV3RealtimeController: RetainedDesktopV3RealtimeController | undefined
let desktopV3RealtimeGeneration = 0
let desktopV3RealtimeControllerFactory = () => new DesktopV3RealtimeControllerRuntime()

interface RetainedDesktopV3RealtimeController {
  controller: DesktopV3RealtimeControllerRuntime
  ready: Promise<void>
  ownerTokens: Map<string, symbol>
  anonymousRetainCount: number
  releaseTimer?: ReturnType<typeof setTimeout>
}

function retainedDesktopV3RealtimeCount(retained: RetainedDesktopV3RealtimeController): number {
  return retained.ownerTokens.size + retained.anonymousRetainCount
}

function clearPendingDesktopV3RealtimeRelease(retained: RetainedDesktopV3RealtimeController): void {
  if (!retained.releaseTimer) return
  clearTimeout(retained.releaseTimer)
  retained.releaseTimer = undefined
}

function releaseDesktopV3RealtimeControllerIfUnused(
  retained: RetainedDesktopV3RealtimeController,
  reason: string,
  delayed: boolean,
): void {
  if (retainedDesktopV3RealtimeCount(retained) > 0) return

  const stop = () => {
    if (retainedDesktopV3RealtimeController !== retained) return
    clearPendingDesktopV3RealtimeRelease(retained)
    if (retainedDesktopV3RealtimeCount(retained) > 0) return
    desktopV3RealtimeGeneration += 1
    retained.controller.stop(reason)
    if (retainedDesktopV3RealtimeController === retained) {
      retainedDesktopV3RealtimeController = undefined
    }
  }

  if (!delayed) {
    stop()
    return
  }

  clearPendingDesktopV3RealtimeRelease(retained)
  retained.releaseTimer = setTimeout(stop, 0)
}

interface Deferred<T> {
  promise: Promise<T>
  resolve: (value: T | PromiseLike<T>) => void
  reject: (reason?: unknown) => void
}

function createDeferred<T>(): Deferred<T> {
  let resolve!: (value: T | PromiseLike<T>) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((promiseResolve, promiseReject) => {
    resolve = promiseResolve
    reject = promiseReject
  })
  return { promise, resolve, reject }
}

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

