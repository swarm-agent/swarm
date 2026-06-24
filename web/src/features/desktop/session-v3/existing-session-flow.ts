import { requireDesktopV3RealtimeControllerReady } from '../realtime/v3-realtime-controller'
import { dispatchDesktopV3Cache, getDesktopV3CacheSnapshot } from '../state/desktop-v3-cache-store'
import { selectAndHydrateDesktopV3Session } from '../state/desktop-v3-session-hydrator'
import type {
  MessageMutationConflictResponse,
  SessionMessageMutationResponse,
} from '../state/desktop-v3-cache-types'
import { messageMutationResponseToAction } from '../state/desktop-v3-cache-wire'
import {
  postDesktopV3AppendMessage,
  type DesktopV3AppendMessageRequest,
} from './write-api'

export interface DesktopV3ExistingMessageOperation {
  version: 1
  operationId: string
  sessionId: string
  request: DesktopV3AppendMessageRequest
  createdAt: number
}

export function createDesktopV3ExistingMessageOperation(input: {
  sessionId: string
  prompt: string
  metadata?: Record<string, unknown>
}): DesktopV3ExistingMessageOperation {
  const sessionId = input.sessionId.trim()
  const content = input.prompt.trim()
  if (!sessionId) throw new Error('Existing Desktop V3 message requires sessionId')
  if (!content) throw new Error('Existing Desktop V3 message requires prompt')

  const operationId = crypto.randomUUID()
  return {
    version: 1,
    operationId,
    sessionId,
    createdAt: Date.now(),
    request: {
      client_request_id: `desktop-v3-existing-message:${sessionId}:${operationId}`,
      message_id: `desktop-v3-message:${operationId}`,
      run_id: `desktop-v3-run:${operationId}`,
      role: 'user',
      content,
      metadata: input.metadata,
    },
  }
}

const EXISTING_MESSAGE_OPERATION_PREFIX = 'swarm.desktop.v3.pending-existing-message.v1:'

function existingMessageOperationKey(sessionId: string): string {
  const normalized = sessionId.trim()
  if (!normalized) throw new Error('Existing Desktop V3 operation key requires sessionId')
  return `${EXISTING_MESSAGE_OPERATION_PREFIX}${encodeURIComponent(normalized)}`
}

export function persistDesktopV3ExistingMessageOperation(
  operation: DesktopV3ExistingMessageOperation,
): void {
  if (typeof window === 'undefined') return
  window.sessionStorage.setItem(
    existingMessageOperationKey(operation.sessionId),
    JSON.stringify(operation),
  )
}

export function loadDesktopV3ExistingMessageOperation(
  sessionId: string,
): DesktopV3ExistingMessageOperation | null {
  const normalized = sessionId.trim()
  if (!normalized || typeof window === 'undefined') return null
  try {
    const raw = window.sessionStorage.getItem(existingMessageOperationKey(normalized))
    if (!raw) return null
    const value = JSON.parse(raw) as DesktopV3ExistingMessageOperation
    if (value.version !== 1 || value.sessionId !== normalized) return null
    if (!value.operationId?.trim()) return null
    if (!value.request?.client_request_id?.trim()) return null
    if (!value.request?.message_id?.trim()) return null
    if (!value.request?.run_id?.trim()) return null
    if (value.request.role !== 'user') return null
    if (!value.request.content?.trim()) return null
    return value
  } catch {
    return null
  }
}

export function clearDesktopV3ExistingMessageOperation(
  sessionId: string,
  operationId: string,
): void {
  if (typeof window === 'undefined') return
  try {
    const current = loadDesktopV3ExistingMessageOperation(sessionId)
    if (!current || current.operationId === operationId) {
      window.sessionStorage.removeItem(existingMessageOperationKey(sessionId))
    }
  } catch {
    // The owning component still clears its in-memory reference.
  }
}

interface DesktopV3ExistingSessionFlowDeps {
  getSnapshot: typeof getDesktopV3CacheSnapshot
  requireControllerReady: typeof requireDesktopV3RealtimeControllerReady
  dispatch: typeof dispatchDesktopV3Cache
  postAppendMessage: typeof postDesktopV3AppendMessage
  hydrateSession: typeof selectAndHydrateDesktopV3Session
}

let flowDeps: DesktopV3ExistingSessionFlowDeps = {
  getSnapshot: getDesktopV3CacheSnapshot,
  requireControllerReady: requireDesktopV3RealtimeControllerReady,
  dispatch: dispatchDesktopV3Cache,
  postAppendMessage: postDesktopV3AppendMessage,
  hydrateSession: selectAndHydrateDesktopV3Session,
}

export function setDesktopV3ExistingSessionFlowDepsForTests(
  deps: Partial<DesktopV3ExistingSessionFlowDeps>,
): () => void {
  const previous = flowDeps
  flowDeps = { ...flowDeps, ...deps }
  return () => {
    flowDeps = previous
  }
}

export async function continueDesktopV3Conversation(
  operation: DesktopV3ExistingMessageOperation,
): Promise<SessionMessageMutationResponse> {
  const sessionId = operation.sessionId.trim()
  if (!sessionId) throw new Error('Existing Desktop V3 conversation requires sessionId')
  if (operation.request.role !== 'user') {
    throw new Error('Existing Desktop V3 conversation only accepts user messages')
  }
  if (!operation.request.content.trim()) {
    throw new Error('Existing Desktop V3 conversation requires prompt')
  }

  const state = flowDeps.getSnapshot()
  if (state.tombstonesBySession[sessionId]) {
    throw new Error(`Desktop V3 session ${sessionId} is deleted`)
  }

  const controller = await flowDeps.requireControllerReady()
  let endpointCursor: string | undefined
  try {
    endpointCursor = controller.currentEndpointCursor()
  } catch (error) {
    console.error('[desktop-v3] existing-session realtime cursor unavailable before append', error)
  }

  flowDeps.dispatch({
    type: 'pendingUser.upsert',
    input: {
      clientRequestId: operation.request.client_request_id,
      messageId: operation.request.message_id,
      sessionId,
      content: operation.request.content,
      metadata: operation.request.metadata,
      createdAt: operation.createdAt,
    },
  })

  const hydratePromise = flowDeps.hydrateSession(sessionId)
  hydratePromise.catch((error) => {
    console.error('[desktop-v3] existing-session hydrate failed', error)
  })

  const connectPromise = (async () => {
    try {
      await controller.connectSession({ sessionId, endpointCursor })
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error)
      console.error('[desktop-v3] existing-session realtime subscribe failed before append', error)
      flowDeps.dispatch({
        type: 'realtime.statusChanged',
        status: 'stale',
        errorCode: 'session_subscribe_failed',
        error: message,
      })
    }
  })()

  let raw: SessionMessageMutationResponse | MessageMutationConflictResponse
  try {
    raw = await flowDeps.postAppendMessage(sessionId, operation.request)
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error)
    flowDeps.dispatch(messageMutationResponseToAction(
      { ok: false, error: message },
      operation.request.client_request_id,
      operation.request.message_id,
    ))
    throw error
  }

  flowDeps.dispatch(messageMutationResponseToAction(
    raw,
    operation.request.client_request_id,
    operation.request.message_id,
  ))

  if (raw.ok === false) {
    throw new Error(raw.error || raw.error_code || 'Desktop V3 existing message failed')
  }
  if (!raw.run_intent) {
    throw new Error('Desktop V3 existing message did not return a run intent')
  }

  const status = raw.run_intent.status.trim().toLowerCase()
  if (status === 'dispatch_blocked') {
    throw new Error(raw.run_intent.blocked_reason || 'Desktop V3 existing-conversation run was dispatch-blocked')
  }
  if (status !== 'pending_executor' && status !== 'running') {
    throw new Error(`Desktop V3 existing-conversation run was not queued or running: ${status || 'missing status'}`)
  }

  void connectPromise

  return raw
}
