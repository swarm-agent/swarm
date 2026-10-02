import { requestJson } from '../../../app/api'
import { dispatchDesktopV3Cache } from '../state/desktop-v3-cache-store'
import { sessionCreateResponseToAction } from '../state/desktop-v3-cache-wire'
import type { SessionCreateMutationResponse, SessionSnapshot } from '../state/desktop-v3-cache-types'

// Only persisted provenance can resolve an old link; names and workspace membership
// are not conversation ownership (one workspace may belong to several projects).
export function conversationProjectId(session?: Pick<SessionSnapshot, 'metadata'> | null): string {
  const metadata = session?.metadata
  return metadata?.agent_name === 'system-orchestrator' && !metadata.task_id && !metadata.parent_session_id
    && typeof metadata.project_id === 'string'
    && (!metadata.swarm_v3_project_id || metadata.swarm_v3_project_id === metadata.project_id) ? metadata.project_id : ''
}

export function requireProjectConversation(projectId: string, session: SessionSnapshot): void {
  if (!session.id || conversationProjectId(session) !== projectId) throw new Error('Session does not belong to this project conversation history')
}

export function projectConversationLink(projectId: string, sessionId?: string) {
  return sessionId
    ? { to: '/projects/$projectId/sessions/$sessionId' as const, params: { projectId, sessionId } }
    : { to: '/projects/$projectId' as const, params: { projectId } }
}

export async function createProjectConversation(projectId: string, clientRequestId: string): Promise<string> {
  const response = await requestJson<SessionCreateMutationResponse>(`/v3/projects/${encodeURIComponent(projectId)}/sessions`, {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ client_request_id: clientRequestId }),
  })
  requireProjectConversation(projectId, response.session)
  if (response.session_id !== response.session.id) throw new Error('Session identity mismatch')
  dispatchDesktopV3Cache(sessionCreateResponseToAction(response, `project:${projectId}`))
  return response.session_id
}
