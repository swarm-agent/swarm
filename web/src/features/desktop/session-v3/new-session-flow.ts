import type { DesktopChatRoute } from '../chat/services/chat-routing'
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

  flowDeps.dispatch({
    type: 'pendingUser.upsert',
    input: {
      clientRequestId: operation.firstMessageRequest.client_request_id,
      messageId: operation.firstMessageRequest.message_id,
      sessionId: operation.sessionId,
      content: operation.firstMessageRequest.content,
      metadata: operation.firstMessageRequest.metadata,
      createdAt: operation.createdAt,
    },
  })

  let rawMessage: SessionMessageMutationResponse | MessageMutationConflictResponse
  try {
    rawMessage = await flowDeps.postAppendMessage(operation.sessionId, operation.firstMessageRequest)
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error)
    flowDeps.dispatch(messageMutationResponseToAction(
      { ok: false, error: message },
      operation.firstMessageRequest.client_request_id,
      operation.firstMessageRequest.message_id,
    ))
    throw error
  }

  flowDeps.dispatch(messageMutationResponseToAction(
    rawMessage,
    operation.firstMessageRequest.client_request_id,
    operation.firstMessageRequest.message_id,
  ))

  if (rawMessage.ok === false) {
    throw new Error(rawMessage.error || rawMessage.error_code || 'Desktop V3 first message failed')
  }
  const firstRunStatus = acceptedRunPhase(rawMessage)
  if (firstRunStatus !== 'accepted' && firstRunStatus !== 'pending_executor') {
    throw new Error(`Desktop V3 first run was not accepted: ${firstRunStatus || 'missing phase'}`)
  }

  // Only now may the new-session pane unmount and become Path B.
  input.onSessionStarted?.(operation.sessionId)

  return {
    sessionId: operation.sessionId,
    createResponse: createOnlyResult.createResponse,
    messageResponse: rawMessage,
  }
}

function acceptedRunPhase(raw: SessionMessageMutationResponse | MessageMutationConflictResponse): string {
  if (raw.ok === false) return ''
  return raw.run_intent?.status.trim().toLowerCase() ?? ''
  return ''
}
