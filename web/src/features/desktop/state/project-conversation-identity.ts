import type { SessionSnapshot } from './desktop-v3-cache-types'

// Only persisted provenance can resolve an old link; names and workspace membership
// are not conversation ownership (one workspace may belong to several projects).
export function conversationProjectId(session?: Pick<SessionSnapshot, 'metadata'> | null): string {
  const metadata = session?.metadata
  return metadata?.agent_name === 'system-orchestrator' && !metadata.task_id && !metadata.parent_session_id
    && !metadata.worker_id && typeof metadata.project_id === 'string'
    && (!metadata.swarm_v3_project_id || metadata.swarm_v3_project_id === metadata.project_id) ? metadata.project_id : ''
}

export function requireProjectConversation(projectId: string, session: SessionSnapshot): void {
  if (!projectId.trim() || !session.id || conversationProjectId(session) !== projectId) throw new Error('Session does not belong to this project conversation history')
}
