import { requestJson } from '../../../app/api'
import { dispatchDesktopV3Cache } from '../state/desktop-v3-cache-store'
import { sessionCreateResponseToAction } from '../state/desktop-v3-cache-wire'
import type { SessionCreateMutationResponse } from '../state/desktop-v3-cache-types'
import { requireProjectConversation } from '../state/project-conversation-identity'
export { conversationProjectId, requireProjectConversation } from '../state/project-conversation-identity'

export function projectConversationLink(projectId: string, sessionId?: string) {
  return sessionId
    ? { to: '/projects/$projectId/sessions/$sessionId' as const, params: { projectId, sessionId } }
    : { to: '/projects/$projectId' as const, params: { projectId } }
}

// Project ownership belongs to the canonical session. Message metadata carries
// display/context hints only; project_id is reserved by the V3 write boundary.
export function projectConversationMessageMetadata(): Record<string, unknown> {
  return { orchestrate_view: true }
}

export async function createProjectConversation(projectId: string, clientRequestId: string): Promise<string> {
  const response = await requestJson<SessionCreateMutationResponse>(`/v3/projects/${encodeURIComponent(projectId)}/sessions`, {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ client_request_id: clientRequestId }),
  })
  if (response.ok !== true) throw new Error('Project conversation creation was not confirmed')
  requireProjectConversation(projectId, response.session)
  if (response.session_id !== response.session.id) throw new Error('Session identity mismatch')
  dispatchDesktopV3Cache(sessionCreateResponseToAction(response, `project:${projectId}`))
  return response.session_id
}
