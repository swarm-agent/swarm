import type { DesktopChatRoute } from '../chat/services/chat-routing'
import type { DesktopV3MediaReference } from '../state/desktop-v3-cache-types'
import type { ModelProfileChoice } from '../chat/types/chat'
import type { DesktopSessionMode } from '../settings/swarm/types/swarm-settings'
import { normalizeSessionMode } from '../settings/swarm/types/swarm-settings'
import { getDesktopSessionCreateTarget } from '../chat/services/chat-routing'
import { retainDesktopV3RealtimeController, requireDesktopV3RealtimeControllerReady } from '../realtime/v3-realtime-controller'
import { dispatchDesktopV3Cache, getDesktopV3CacheSnapshot } from '../state/desktop-v3-cache-store'
import type {
  MessageMutationConflictResponse,
  SessionCreateMutationResponse,
  SessionMessageMutationResponse,
  SessionMutationErrorResponse,
} from '../state/desktop-v3-cache-types'
import { bootstrapDesktopV3SidebarMetadataOnly } from '../state/desktop-v3-bootstrap-controller'
import {
  messageMutationResponseToAction,
  selectSession,
  sessionCreateResponseToAction,
} from '../state/desktop-v3-cache-wire'
import {
  postDesktopV3AppendMessage,
  desktopV3ModelProfileChoiceWire,
  postDesktopV3CreateSession,
  type DesktopV3AppendMessageRequest,
  type DesktopV3CreateSessionRequest,
} from './write-api'

export interface DesktopV3CreateOnlySessionOperation {
  version: 1
  operationId: string
  sessionId: string
  createRequest: DesktopV3CreateSessionRequest
  createdAt: number
}

export interface DesktopV3NewSessionOperation extends DesktopV3CreateOnlySessionOperation {
  firstMessageRequest: DesktopV3AppendMessageRequest
}

export interface DesktopV3NewSessionPreference {
  provider?: string
  model?: string
  thinking?: string
  serviceTier?: string
  contextMode?: string
}

export interface CreateDesktopV3NewSessionOperationInput {
  workspacePath: string
  workspaceName: string
  route: DesktopChatRoute
  prompt: string
  title?: string
  mode?: DesktopSessionMode
  agentName: string
  preference?: DesktopV3NewSessionPreference
  modelProfileChoice?: ModelProfileChoice
  sessionMetadata?: Record<string, unknown>
  messageMetadata?: Record<string, unknown>
  worktree?: {
    mode?: string
    useCurrentBranch?: boolean
    baseBranch?: string
    branchName?: string
    existingPath?: string
  }
}

function resolvedNewSessionPreference(
  preference: DesktopV3NewSessionPreference | undefined,
): NonNullable<DesktopV3CreateSessionRequest['preference']> | undefined {
  if (!preference) return undefined
  const provider = preference.provider?.trim() ?? ''
  const model = preference.model?.trim() ?? ''
  const thinking = preference.thinking?.trim() ?? ''
  if (!provider || !model || !thinking) {
    throw new Error('New Desktop V3 session preference requires resolved provider, model, and thinking')
  }
  return {
    provider,
    model,
    thinking,
    service_tier: preference.serviceTier?.trim() || undefined,
    context_mode: preference.contextMode?.trim() || undefined,
  }
}

export function createDesktopV3CreateOnlySessionOperation(
  input: Omit<CreateDesktopV3NewSessionOperationInput, 'prompt' | 'messageMetadata'>,
): DesktopV3CreateOnlySessionOperation {
  const workspacePath = input.workspacePath.trim()
  const agentName = input.agentName.trim()
  if (!workspacePath) throw new Error('New Desktop V3 session requires workspacePath')
  if (!agentName) throw new Error('New Desktop V3 session requires agent_name')

  const target = getDesktopSessionCreateTarget(input.route)
  if (target.endpoint !== '/v3/sessions') {
    throw new Error(target.unsupportedReason || 'Selected route cannot create a V3 session')
  }

  const workspaceBindingId = target.workspaceBindingId?.trim() ?? ''
  const swarmId = target.swarmId?.trim() ?? ''
  if (!workspaceBindingId || !swarmId) {
    throw new Error('New Desktop V3 session requires swarm_id and workspace_binding_id')
  }

  const targetKind = input.route.targetKind?.trim().toLowerCase() || 'host'
  if (targetKind !== 'host' && targetKind !== 'self') {
    throw new Error(`New Desktop V3 session has unsupported target_kind ${JSON.stringify(targetKind)}`)
  }
  const targetRelationship = input.route.targetRelationship?.trim().toLowerCase() || 'self'
  if (targetRelationship !== 'self') {
    throw new Error(`New Desktop V3 session has unsupported target_relationship ${JSON.stringify(targetRelationship)}`)
  }

  const preference = resolvedNewSessionPreference(input.preference)

  const operationId = crypto.randomUUID()
  const sessionId = crypto.randomUUID()
  const createClientRequestId = `desktop-v3-start:${operationId}:create`

  return {
    version: 1,
    operationId,
    sessionId,
    createdAt: Date.now(),
    createRequest: {
      session_id: sessionId,
      client_request_id: createClientRequestId,
      title: input.title?.trim() || undefined,
      workspace_path: workspacePath,
      workspace_name: input.workspaceName.trim() || undefined,
      workspace_binding_id: workspaceBindingId,
      swarm_id: swarmId,
      target_kind: targetKind,
      target_relationship: targetRelationship,
      host_workspace_path: input.route.hostWorkspacePath?.trim() || undefined,
      runtime_workspace_path: input.route.runtimeWorkspacePath?.trim() || undefined,
      mode: normalizeSessionMode(input.mode),
      agent_name: agentName,
      metadata: input.sessionMetadata,
      preference,
      model_profile: input.modelProfileChoice ? desktopV3ModelProfileChoiceWire(input.modelProfileChoice) : undefined,
      worktree_mode: input.worktree?.mode,
      worktree_use_current_branch: input.worktree?.useCurrentBranch,
      worktree_base_branch: input.worktree?.baseBranch,
      worktree_branch_name: input.worktree?.branchName,
      worktree_existing_path: input.worktree?.existingPath,
    },
  }
}

export function createDesktopV3NewSessionOperation(
  input: CreateDesktopV3NewSessionOperationInput,
): DesktopV3NewSessionOperation {
  const prompt = input.prompt.trim()
  if (!prompt) throw new Error('New Desktop V3 session requires a first prompt')

  const operation = createDesktopV3CreateOnlySessionOperation(input)
  const firstMessageClientRequestId = `desktop-v3-first-message:${operation.operationId}`
  const firstMessageId = `desktop-v3-message:${operation.operationId}`
  const firstRunId = `desktop-v3-run:${operation.operationId}`

  return {
    ...operation,
    firstMessageRequest: {
      client_request_id: firstMessageClientRequestId,
      message_id: firstMessageId,
      run_id: firstRunId,
      role: 'user',
      content: prompt,
      metadata: input.messageMetadata,
    },
  }
}

const NEW_SESSION_OPERATION_PREFIX = 'swarm.desktop.v3.pending-new-session.v1:'

function newSessionOperationKey(workspacePath: string): string {
  const normalized = workspacePath.trim()
  if (!normalized) throw new Error('New Desktop V3 operation key requires workspacePath')
  return `${NEW_SESSION_OPERATION_PREFIX}${encodeURIComponent(normalized)}`
}

function readStoredDesktopV3NewSessionOperation(
  workspacePath: string,
): DesktopV3NewSessionOperation | null {
  const normalizedWorkspacePath = workspacePath.trim()
  if (!normalizedWorkspacePath || typeof window === 'undefined') return null
  try {
    const raw = window.sessionStorage.getItem(newSessionOperationKey(normalizedWorkspacePath))
    if (!raw) return null
    const value = JSON.parse(raw) as DesktopV3NewSessionOperation
    if (value.version !== 1) return null
    if (!value.operationId?.trim() || !value.sessionId?.trim()) return null
    if (value.createRequest?.session_id !== value.sessionId) return null
    if (value.createRequest?.workspace_path !== normalizedWorkspacePath) return null
    if (!value.createRequest?.client_request_id?.trim()) return null
    const agentName = value.createRequest.agent_name?.trim() ?? ''
    const worktreeMode = value.createRequest.worktree_mode?.trim() ?? ''
    const worktreeBranch = value.createRequest.worktree_branch_name?.trim() ?? ''
    const invalidWorktree = (worktreeMode !== ''
      && worktreeMode !== 'on'
      && worktreeMode !== 'off'
      && worktreeMode !== 'inherit')
      || (worktreeMode === 'on' && !worktreeBranch)
      || (worktreeMode !== 'on' && Boolean(worktreeBranch))
    if (!agentName || invalidWorktree) {
      window.sessionStorage.removeItem(newSessionOperationKey(normalizedWorkspacePath))
      return null
    }
    if (!value.firstMessageRequest?.client_request_id?.trim()) return null
    if (!value.firstMessageRequest?.message_id?.trim()) return null
    if (!value.firstMessageRequest?.run_id?.trim()) return null
    return value
  } catch {
    return null
  }
}

export function persistDesktopV3NewSessionOperation(
  operation: DesktopV3NewSessionOperation,
): void {
  if (typeof window === 'undefined') return
  window.sessionStorage.setItem(
    newSessionOperationKey(operation.createRequest.workspace_path),
    JSON.stringify(operation),
  )
}

export function loadDesktopV3NewSessionOperation(
  workspacePath: string,
): DesktopV3NewSessionOperation | null {
  return readStoredDesktopV3NewSessionOperation(workspacePath)
}

export function desktopV3NewSessionOperationMatchesRoute(
  operation: DesktopV3CreateOnlySessionOperation | null | undefined,
  route: DesktopChatRoute | null | undefined,
): boolean {
  if (!operation || !route) return false
  const target = getDesktopSessionCreateTarget(route)
  if (target.endpoint !== '/v3/sessions') return false
  const createRequest = operation.createRequest
  const requestSwarmId = createRequest.swarm_id?.trim() ?? ''
  const requestBindingId = createRequest.workspace_binding_id?.trim() ?? ''
  if (!requestSwarmId || !requestBindingId) return false
  if (requestSwarmId !== target.swarmId.trim()) return false
  if (requestBindingId !== target.workspaceBindingId.trim()) return false
  const requestTargetKind = createRequest.target_kind?.trim().toLowerCase() || 'host'
  const routeTargetKind = route.targetKind?.trim().toLowerCase() || 'host'
  if (requestTargetKind !== routeTargetKind) return false
  const requestRelationship = createRequest.target_relationship?.trim().toLowerCase() || 'self'
  const routeRelationship = route.targetRelationship?.trim().toLowerCase() || 'self'
  return requestRelationship === routeRelationship
}

export function clearDesktopV3NewSessionOperation(
  workspacePath: string,
  operationId: string,
): void {
  if (typeof window === 'undefined') return
  try {
    const current = readStoredDesktopV3NewSessionOperation(workspacePath)
    if (!current || current.operationId === operationId) {
      window.sessionStorage.removeItem(newSessionOperationKey(workspacePath))
    }
  } catch {
    // The in-memory operation is still cleared by the owning component.
  }
}

export interface StartNewDesktopV3SessionResult {
  sessionId: string
  createResponse: SessionCreateMutationResponse
  messageResponse: SessionMessageMutationResponse
}

export interface StartDesktopV3CreateOnlySessionResult {
  sessionId: string
  createResponse: SessionCreateMutationResponse
}

interface DesktopV3NewSessionFlowDeps {
  getSnapshot: typeof getDesktopV3CacheSnapshot
  requireControllerReady: typeof requireDesktopV3RealtimeControllerReady
  ensureSidebarBootstrap: typeof bootstrapDesktopV3SidebarMetadataOnly
  retainRealtimeController: typeof retainDesktopV3RealtimeController
  dispatch: typeof dispatchDesktopV3Cache
  postCreateSession: typeof postDesktopV3CreateSession
  postAppendMessage: typeof postDesktopV3AppendMessage
}

let flowDeps: DesktopV3NewSessionFlowDeps = {
  getSnapshot: getDesktopV3CacheSnapshot,
  requireControllerReady: requireDesktopV3RealtimeControllerReady,
  ensureSidebarBootstrap: bootstrapDesktopV3SidebarMetadataOnly,
  retainRealtimeController: retainDesktopV3RealtimeController,
  dispatch: dispatchDesktopV3Cache,
  postCreateSession: postDesktopV3CreateSession,
  postAppendMessage: postDesktopV3AppendMessage,
}

export function setDesktopV3NewSessionFlowDepsForTests(
  deps: Partial<DesktopV3NewSessionFlowDeps>,
): () => void {
  const previous = flowDeps
  flowDeps = { ...flowDeps, ...deps }
  return () => {
    flowDeps = previous
  }
}

export async function startDesktopV3CreateOnlySession(input: {
  operation: DesktopV3CreateOnlySessionOperation
  shouldSelectSession?: () => boolean
  onSessionStarted?: (sessionId: string) => void
}): Promise<StartDesktopV3CreateOnlySessionResult> {
  const operation = input.operation
  if (operation.createRequest.session_id !== operation.sessionId) {
    throw new Error('Desktop V3 new-session operation has inconsistent session identity')
  }

  let bootstrapLease: ReturnType<typeof flowDeps.retainRealtimeController> | undefined
  try {
    let stateBeforeCreate = flowDeps.getSnapshot()
    let sidebarScopeId = stateBeforeCreate.desktopSidebarBootstrap.scopeId?.trim()
    if (!sidebarScopeId) {
      const bootstrapReady = flowDeps.ensureSidebarBootstrap({
        preferredSessionId: operation.sessionId,
      })
      bootstrapLease = flowDeps.retainRealtimeController({
        ownerKey: `desktop-v3-new-session:${operation.operationId}`,
        preferredSessionId: operation.sessionId,
        bootstrap: bootstrapReady,
      })
      await bootstrapReady
      stateBeforeCreate = flowDeps.getSnapshot()
      sidebarScopeId = stateBeforeCreate.desktopSidebarBootstrap.scopeId?.trim()
    }
    if (!sidebarScopeId) {
      throw new Error('New Desktop V3 session requires a bootstrapped sidebar scope')
    }

    const controller = await flowDeps.requireControllerReady()

    const rawCreate: SessionCreateMutationResponse | SessionMutationErrorResponse = await flowDeps.postCreateSession(operation.createRequest)

    flowDeps.dispatch(sessionCreateResponseToAction(
      rawCreate,
      sidebarScopeId,
    ))

    if (rawCreate.ok === false) {
      const message = rawCreate.error || rawCreate.error_code || 'Desktop V3 session create failed'
      throw new Error(message)
    }

    if (rawCreate.session_id !== operation.sessionId) {
      throw new Error('Desktop V3 create returned a different session_id')
    }

    if (input.shouldSelectSession?.() ?? true) {
      flowDeps.dispatch(selectSession(operation.sessionId))
    }

    await controller.ensureSessionConnected(operation.sessionId)

    input.onSessionStarted?.(operation.sessionId)

    return {
      sessionId: operation.sessionId,
      createResponse: rawCreate,
    }
  } finally {
    bootstrapLease?.release()
  }
}

export async function appendFirstDesktopV3Message(input: {
  operation: DesktopV3NewSessionOperation
  media?: DesktopV3AppendMessageRequest['media']
  onSessionStarted?: (sessionId: string) => void
}): Promise<SessionMessageMutationResponse> {
  const operation = input.operation
  const firstMessageRequest = input.media?.length
    ? { ...operation.firstMessageRequest, media: input.media }
    : operation.firstMessageRequest
  flowDeps.dispatch({
    type: 'pendingUser.upsert',
    input: {
      clientRequestId: firstMessageRequest.client_request_id,
      messageId: firstMessageRequest.message_id,
      sessionId: operation.sessionId,
      content: firstMessageRequest.content,
      metadata: firstMessageRequest.metadata,
      runId: firstMessageRequest.run_id,
      createdAt: operation.createdAt,
    },
  })

  let rawMessage: SessionMessageMutationResponse | MessageMutationConflictResponse
  try {
    rawMessage = await flowDeps.postAppendMessage(operation.sessionId, firstMessageRequest)
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error)
    flowDeps.dispatch(messageMutationResponseToAction(
      { ok: false, error: message },
      firstMessageRequest.client_request_id,
      firstMessageRequest.message_id,
    ))
    throw error
  }
  flowDeps.dispatch(messageMutationResponseToAction(
    rawMessage,
    firstMessageRequest.client_request_id,
    firstMessageRequest.message_id,
  ))
  if (rawMessage.ok === false) {
    throw new Error(rawMessage.error || rawMessage.error_code || 'Desktop V3 first message failed')
  }
  const firstRunStatus = acceptedRunPhase(rawMessage)
  if (firstRunStatus !== 'accepted' && firstRunStatus !== 'pending_executor') {
    throw new Error(`Desktop V3 first run was not accepted: ${firstRunStatus || 'missing phase'}`)
  }
  input.onSessionStarted?.(operation.sessionId)
  return rawMessage
}

export async function startNewDesktopV3Session(input: {
  operation: DesktopV3NewSessionOperation
  shouldSelectSession?: () => boolean
  onSessionStarted?: (sessionId: string) => void
}): Promise<StartNewDesktopV3SessionResult> {
  const operation = input.operation
  const createOnlyResult = await startDesktopV3CreateOnlySession({
    operation,
    shouldSelectSession: input.shouldSelectSession,
  })

  const rawMessage = await appendFirstDesktopV3Message({
    operation,
    onSessionStarted: input.onSessionStarted,
  })

  return {
    sessionId: operation.sessionId,
    createResponse: createOnlyResult.createResponse,
    messageResponse: rawMessage,
  }
}

function acceptedRunPhase(raw: SessionMessageMutationResponse | MessageMutationConflictResponse): string {
  if (raw.ok === false) return ''
  return raw.run_intent?.status.trim().toLowerCase() ?? ''
}

/**
 * Local-only state for the canonical routed Desktop start. None of the draft,
 * primed, routing, or failed variants is a durable session and callers must not
 * publish one into the V3 cache, sidebar, realtime controller, or URL.
 */
export interface DesktopV3RoutedMediaInput {
  staging_id: string
  modality?: string
  file_type?: string
}

/**
 * Serializable composer state captured before the local composer is cleared.
 * Action and skill records intentionally remain opaque here: the controller
 * owns exact rollback, not interpretation of either workspace-owned record.
 */
export interface DesktopV3RoutedComposerSnapshot {
  prompt: string
  attachments: DesktopV3RoutedMediaInput[]
  selectedAction: unknown | null
  selectedSkill: unknown | null
  worktreePrimed: boolean
}

export interface DesktopV3RoutedDraftState {
  phase: 'draft'
  prompt: string
  snapshot: DesktopV3RoutedComposerSnapshot
}

export interface DesktopV3RoutedWorktreePrimedState {
  phase: 'worktree-primed'
  prompt: string
  snapshot: DesktopV3RoutedComposerSnapshot
}

export interface DesktopV3RoutedStartRequest {
  input: string
  client_request_id: string
  idempotency_key: string
  agent_name?: string
  metadata?: Record<string, unknown>
  media?: DesktopV3RoutedMediaInput[]
}

export interface DesktopV3RoutedStartOperation {
  version: 1
  operationId: string
  createdAt: number
  snapshot: DesktopV3RoutedComposerSnapshot
  request: DesktopV3RoutedStartRequest
}

export interface DesktopV3RoutedStartResult {
  ok: true
  session_id: string
  title: string
  starting_mode: string
  replayed: boolean
  session: { id?: string; session_id?: string }
  session_view: unknown
  first_message: { id?: string; session_id?: string; media?: DesktopV3MediaReference[] }
  projection: { session_id?: string }
  mutation: { session_id?: string }
}

export interface DesktopV3RoutedRoutingState {
  phase: 'routing'
  prompt: string
  snapshot: DesktopV3RoutedComposerSnapshot
  operation: DesktopV3RoutedStartOperation
}

export interface DesktopV3RoutedResolvedState {
  phase: 'resolved'
  prompt: string
  snapshot: DesktopV3RoutedComposerSnapshot
  operation: DesktopV3RoutedStartOperation
  result: DesktopV3RoutedStartResult
}

export interface DesktopV3RoutedFailedState {
  phase: 'failed'
  prompt: string
  snapshot: DesktopV3RoutedComposerSnapshot
  operation: DesktopV3RoutedStartOperation
  error: string
}

export type DesktopV3RoutedNewSessionState =
  | DesktopV3RoutedDraftState
  | DesktopV3RoutedWorktreePrimedState
  | DesktopV3RoutedRoutingState
  | DesktopV3RoutedResolvedState
  | DesktopV3RoutedFailedState

export interface DesktopV3RoutedOperationIdentity {
  operationId: string
  clientRequestId: string
}

export interface CreateDesktopV3RoutedStartOperationInput {
  prompt?: string
  snapshot?: DesktopV3RoutedComposerSnapshot
  agentName?: string
  metadata?: Record<string, unknown>
  media?: DesktopV3RoutedMediaInput[]
  selectedAction?: unknown | null
  selectedSkill?: unknown | null
  worktreePrimed?: boolean
  /** Reserved before media staging so uploads and the routed start share one identity. */
  identity?: DesktopV3RoutedOperationIdentity
}

export type PostDesktopV3RoutedStart = (
  request: DesktopV3RoutedStartRequest,
) => Promise<DesktopV3RoutedStartResult>

const ROUTED_NEW_SESSION_OPERATION_KEY = 'swarm.desktop.v3.routed-new-session.v1'

function normalizedRoutedMedia(media: DesktopV3RoutedMediaInput[] | undefined): DesktopV3RoutedMediaInput[] | undefined {
  if (!media?.length) return undefined
  return media.map((item) => {
    const stagingID = item.staging_id.trim()
    if (!stagingID) throw new Error('Routed Desktop media requires staging_id')
    return {
      staging_id: stagingID,
      modality: item.modality?.trim() || undefined,
      file_type: item.file_type?.trim() || undefined,
    }
  })
}

export function createDesktopV3RoutedComposerSnapshot(
  input: Partial<DesktopV3RoutedComposerSnapshot> & Pick<DesktopV3RoutedComposerSnapshot, 'prompt'>,
): DesktopV3RoutedComposerSnapshot {
  return {
    prompt: input.prompt,
    attachments: input.attachments?.map((attachment) => ({ ...attachment })) ?? [],
    selectedAction: input.selectedAction === undefined || input.selectedAction === null
      ? null
      : cloneRoutedComposerSelection(input.selectedAction),
    selectedSkill: input.selectedSkill === undefined || input.selectedSkill === null
      ? null
      : cloneRoutedComposerSelection(input.selectedSkill),
    worktreePrimed: input.worktreePrimed === true,
  }
}

function cloneRoutedComposerSelection(selection: unknown): unknown {
  try {
    return JSON.parse(JSON.stringify(selection)) as unknown
  } catch {
    throw new Error('Routed Desktop composer selection must be serializable')
  }
}

export function createDesktopV3RoutedDraftState(
  prompt = '',
  snapshot = createDesktopV3RoutedComposerSnapshot({ prompt }),
): DesktopV3RoutedDraftState {
  if (prompt !== snapshot.prompt) throw new Error('Routed Desktop draft prompt must match its composer snapshot')
  return { phase: 'draft', prompt: snapshot.prompt, snapshot }
}

export function createDesktopV3RoutedWorktreePrimedState(
  prompt = '',
  snapshot = createDesktopV3RoutedComposerSnapshot({ prompt, worktreePrimed: true }),
): DesktopV3RoutedWorktreePrimedState {
  if (prompt !== snapshot.prompt) throw new Error('Routed Desktop worktree prompt must match its composer snapshot')
  if (!snapshot.worktreePrimed) throw new Error('Routed Desktop worktree state requires a primed composer snapshot')
  return { phase: 'worktree-primed', prompt: snapshot.prompt, snapshot }
}

export function createDesktopV3RoutedOperationIdentity(): DesktopV3RoutedOperationIdentity {
  const operationId = crypto.randomUUID()
  return { operationId, clientRequestId: `desktop-v3-routed:${operationId}` }
}

export function desktopV3RoutedRequestInput(snapshot: DesktopV3RoutedComposerSnapshot): string {
  const prompt = snapshot.prompt.trim()
  if (!prompt) throw new Error('Routed Desktop start requires a prompt')
  return snapshot.worktreePrimed
    ? `${prompt}\n\nUse a managed worktree for this session.`
    : prompt
}

export function createDesktopV3RoutedStartOperation(
  input: CreateDesktopV3RoutedStartOperationInput,
): DesktopV3RoutedStartOperation {
  if (input.snapshot && input.media !== undefined) {
    throw new Error('Routed Desktop start accepts snapshot attachments or media, not both')
  }
  if (input.snapshot && input.prompt !== undefined && input.prompt !== input.snapshot.prompt) {
    throw new Error('Routed Desktop start prompt must match the captured composer snapshot')
  }
  const snapshot = createDesktopV3RoutedComposerSnapshot(input.snapshot ?? {
    prompt: input.prompt ?? '',
    attachments: input.media,
    selectedAction: input.selectedAction,
    selectedSkill: input.selectedSkill,
    worktreePrimed: input.worktreePrimed,
  })
  const requestInput = desktopV3RoutedRequestInput(snapshot)
  const identity = input.identity ?? createDesktopV3RoutedOperationIdentity()
  const operationId = identity.operationId.trim()
  const clientRequestID = identity.clientRequestId.trim()
  if (!operationId || clientRequestID !== `desktop-v3-routed:${operationId}`) {
    throw new Error('Routed Desktop operation identity is invalid')
  }
  return {
    version: 1,
    operationId,
    createdAt: Date.now(),
    snapshot,
    request: {
      input: requestInput,
      client_request_id: clientRequestID,
      idempotency_key: clientRequestID,
      ...(input.agentName?.trim() ? { agent_name: input.agentName.trim() } : {}),
      metadata: input.metadata ? { ...input.metadata } : undefined,
      media: normalizedRoutedMedia(snapshot.attachments),
    },
  }
}

function isStoredDesktopV3RoutedStartOperation(value: unknown): value is DesktopV3RoutedStartOperation {
  if (!value || typeof value !== 'object') return false
  const operation = value as Partial<DesktopV3RoutedStartOperation>
  if (operation.version !== 1 || !operation.operationId?.trim() || typeof operation.createdAt !== 'number' || !Number.isFinite(operation.createdAt)) return false
  const snapshot = operation.snapshot
  if (!isStoredDesktopV3RoutedComposerSnapshot(snapshot)) return false
  const request = operation.request
  if (!request || !request.input?.trim() || !request.client_request_id?.trim()) return false
  if (request.idempotency_key !== request.client_request_id) return false
  if (request.client_request_id !== `desktop-v3-routed:${operation.operationId}`) return false
  if (request.media && (!Array.isArray(request.media) || request.media.some((item) => !item?.staging_id?.trim()))) return false
  if (request.input !== desktopV3RoutedRequestInput(snapshot)) return false
  if (JSON.stringify(request.media ?? []) !== JSON.stringify(normalizedRoutedMedia(snapshot.attachments) ?? [])) return false
  return true
}

function isStoredDesktopV3RoutedComposerSnapshot(value: unknown): value is DesktopV3RoutedComposerSnapshot {
  if (!value || typeof value !== 'object') return false
  const snapshot = value as Partial<DesktopV3RoutedComposerSnapshot>
  if (typeof snapshot.prompt !== 'string' || typeof snapshot.worktreePrimed !== 'boolean') return false
  if (!Array.isArray(snapshot.attachments) || snapshot.attachments.some((item) => !item?.staging_id?.trim())) return false
  if (snapshot.selectedAction === undefined || snapshot.selectedSkill === undefined) return false
  return true
}

export function persistDesktopV3RoutedStartOperation(operation: DesktopV3RoutedStartOperation): void {
  if (typeof window === 'undefined') return
  window.sessionStorage.setItem(ROUTED_NEW_SESSION_OPERATION_KEY, JSON.stringify(operation))
}

export function loadDesktopV3RoutedStartOperation(): DesktopV3RoutedStartOperation | null {
  if (typeof window === 'undefined') return null
  try {
    const raw = window.sessionStorage.getItem(ROUTED_NEW_SESSION_OPERATION_KEY)
    if (!raw) return null
    const operation: unknown = JSON.parse(raw)
    if (isStoredDesktopV3RoutedStartOperation(operation)) return operation
    window.sessionStorage.removeItem(ROUTED_NEW_SESSION_OPERATION_KEY)
    return null
  } catch {
    window.sessionStorage.removeItem(ROUTED_NEW_SESSION_OPERATION_KEY)
    return null
  }
}

export function clearDesktopV3RoutedStartOperation(operationId?: string): void {
  if (typeof window === 'undefined') return
  if (operationId) {
    const current = loadDesktopV3RoutedStartOperation()
    if (current && current.operationId !== operationId) return
  }
  window.sessionStorage.removeItem(ROUTED_NEW_SESSION_OPERATION_KEY)
}

export function restoreDesktopV3RoutedNewSessionState(): DesktopV3RoutedNewSessionState {
  const operation = loadDesktopV3RoutedStartOperation()
  if (!operation) return createDesktopV3RoutedDraftState()
  return {
    phase: 'failed',
    prompt: operation.snapshot.prompt,
    snapshot: operation.snapshot,
    operation,
    error: 'Routing was interrupted. Retry to resume the same routed start.',
  }
}

function routedResultRecordSessionID(record: { id?: unknown; session_id?: unknown }): string {
  const value = record.session_id ?? record.id
  return typeof value === 'string' ? value.trim() : ''
}

export function validateDesktopV3RoutedStartResult(result: DesktopV3RoutedStartResult): DesktopV3RoutedStartResult {
  if (!result || result.ok !== true) throw new Error('Routed Desktop start returned an unsuccessful response')
  const sessionID = result.session_id?.trim()
  if (!sessionID) throw new Error('Routed Desktop start returned no canonical session_id')
  if (!result.title?.trim() || !result.starting_mode?.trim()) {
    throw new Error('Routed Desktop start returned incomplete routed authority')
  }
  if (routedResultRecordSessionID(result.session) !== sessionID) {
    throw new Error('Routed Desktop start returned a mismatched session')
  }
  for (const [label, record] of [
    ['first message', result.first_message],
    ['projection', result.projection],
    ['mutation', result.mutation],
  ] as const) {
    if (!record || routedResultRecordSessionID(record) !== sessionID) {
      throw new Error(`Routed Desktop start returned a mismatched ${label}`)
    }
  }
  return result
}

/**
 * Small framework-neutral controller consumed by the new-chat UI. The injected
 * transport is the only side effect besides retaining retry identity in
 * sessionStorage. Canonical V3 activation is deliberately left to the owner of
 * the resolved state.
 */
export class DesktopV3RoutedNewSessionController {
  private state: DesktopV3RoutedNewSessionState
  private generation = 0
  private activeRun: Promise<DesktopV3RoutedNewSessionState> | null = null
  private reservedIdentity: DesktopV3RoutedOperationIdentity | null = null
  private readonly listeners = new Set<(state: DesktopV3RoutedNewSessionState) => void>()

  constructor(
    private readonly postRoutedStart: PostDesktopV3RoutedStart,
    initialState: DesktopV3RoutedNewSessionState = restoreDesktopV3RoutedNewSessionState(),
  ) {
    this.state = initialState
  }

  getState(): DesktopV3RoutedNewSessionState {
    return this.state
  }

  subscribe(listener: (state: DesktopV3RoutedNewSessionState) => void): () => void {
    this.listeners.add(listener)
    return () => this.listeners.delete(listener)
  }

  prepareOperationIdentity(): DesktopV3RoutedOperationIdentity {
    if (this.state.phase === 'failed' || this.state.phase === 'routing' || this.state.phase === 'resolved') {
      return {
        operationId: this.state.operation.operationId,
        clientRequestId: this.state.operation.request.client_request_id,
      }
    }
    if (!this.reservedIdentity) this.reservedIdentity = createDesktopV3RoutedOperationIdentity()
    return { ...this.reservedIdentity }
  }

  startDraft(
    prompt = '',
    snapshot = createDesktopV3RoutedComposerSnapshot({ prompt }),
  ): DesktopV3RoutedDraftState {
    this.invalidateCurrentOperation()
    const state = createDesktopV3RoutedDraftState(prompt, snapshot)
    this.publish(state)
    return state
  }

  primeWorktree(
    prompt = '',
    snapshot = createDesktopV3RoutedComposerSnapshot({ prompt, worktreePrimed: true }),
  ): DesktopV3RoutedWorktreePrimedState {
    this.invalidateCurrentOperation()
    const state = createDesktopV3RoutedWorktreePrimedState(prompt, snapshot)
    this.publish(state)
    return state
  }

  submit(input?: CreateDesktopV3RoutedStartOperationInput): Promise<DesktopV3RoutedNewSessionState> {
    if (this.state.phase === 'routing' && this.activeRun) return this.activeRun

    let operation: DesktopV3RoutedStartOperation
    if (this.state.phase === 'failed') {
      operation = this.state.operation
    } else if (this.state.phase === 'draft' || this.state.phase === 'worktree-primed') {
      const operationInput: CreateDesktopV3RoutedStartOperationInput = input ?? { snapshot: this.state.snapshot }
      operation = createDesktopV3RoutedStartOperation({
        ...operationInput,
        identity: operationInput.identity ?? this.prepareOperationIdentity(),
      })
      this.reservedIdentity = null
    } else {
      return Promise.reject(new Error('Resolved routed Desktop start cannot be submitted again'))
    }

    persistDesktopV3RoutedStartOperation(operation)
    const runGeneration = ++this.generation
    this.publish({ phase: 'routing', prompt: operation.snapshot.prompt, snapshot: operation.snapshot, operation })
    const run = this.run(operation, runGeneration)
    this.activeRun = run
    void run.finally(() => {
      if (this.activeRun === run) this.activeRun = null
    }).catch(() => undefined)
    return run
  }

  retry(): Promise<DesktopV3RoutedNewSessionState> {
    if (this.state.phase !== 'failed') {
      return Promise.reject(new Error('Only a failed routed Desktop start can be retried'))
    }
    return this.submit()
  }

  /**
   * Completes the local handoff only after the app-level owner has published the
   * durable routed result, connected realtime, and navigated successfully.
   */
  acknowledgeResolved(operationId: string): void {
    const normalizedOperationId = operationId.trim()
    if (!normalizedOperationId) throw new Error('Resolved routed Desktop start requires operation identity')
    if (this.state.phase !== 'resolved' || this.state.operation.operationId !== normalizedOperationId) {
      throw new Error('Only the current resolved routed Desktop start can be acknowledged')
    }
    clearDesktopV3RoutedStartOperation(normalizedOperationId)
    this.reservedIdentity = null
    this.generation += 1
  }

  /** Restores the exact routed operation when canonical activation fails. */
  rejectResolved(operationId: string, error: unknown): DesktopV3RoutedFailedState {
    if (this.state.phase !== 'resolved' || this.state.operation.operationId !== operationId) {
      throw new Error('Only the current resolved routed Desktop start can be rejected')
    }
    const operation = this.state.operation
    persistDesktopV3RoutedStartOperation(operation)
    const failed: DesktopV3RoutedFailedState = {
      phase: 'failed',
      prompt: operation.snapshot.prompt,
      snapshot: operation.snapshot,
      operation,
      error: error instanceof Error ? error.message : String(error),
    }
    this.generation += 1
    this.publish(failed)
    return failed
  }

  private async run(
    operation: DesktopV3RoutedStartOperation,
    runGeneration: number,
  ): Promise<DesktopV3RoutedNewSessionState> {
    try {
      const result = validateDesktopV3RoutedStartResult(await this.postRoutedStart(operation.request))
      if (!this.isCurrent(operation, runGeneration)) return this.state
      const resolved: DesktopV3RoutedResolvedState = {
        phase: 'resolved',
        prompt: operation.snapshot.prompt,
        snapshot: operation.snapshot,
        operation,
        result,
      }
      this.publish(resolved)
      return resolved
    } catch (error) {
      if (!this.isCurrent(operation, runGeneration)) return this.state
      const failed: DesktopV3RoutedFailedState = {
        phase: 'failed',
        prompt: operation.snapshot.prompt,
        snapshot: operation.snapshot,
        operation,
        error: error instanceof Error ? error.message : String(error),
      }
      this.publish(failed)
      return failed
    }
  }

  private isCurrent(operation: DesktopV3RoutedStartOperation, generation: number): boolean {
    return generation === this.generation
      && (this.state.phase === 'routing')
      && this.state.operation.operationId === operation.operationId
  }

  private invalidateCurrentOperation(): void {
    const operation = this.state.phase === 'routing'
      || this.state.phase === 'failed'
      || this.state.phase === 'resolved'
      ? this.state.operation
      : null
    this.generation += 1
    this.activeRun = null
    this.reservedIdentity = null
    if (operation) clearDesktopV3RoutedStartOperation(operation.operationId)
  }

  private publish(state: DesktopV3RoutedNewSessionState): void {
    this.state = state
    for (const listener of this.listeners) listener(state)
  }
}
