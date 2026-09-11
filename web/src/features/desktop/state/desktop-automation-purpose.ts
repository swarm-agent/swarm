import type { SessionSnapshot } from './desktop-v3-cache-types'

export const AUTOMATION_MANAGEMENT_PURPOSE = 'automation_management'
export function isAutomationManagementSession(session: SessionSnapshot | undefined, workspaceId?: string): boolean {
  const metadata = session?.metadata
  return metadata?.swarm_v3_session_purpose === AUTOMATION_MANAGEMENT_PURPOSE
    && typeof metadata.swarm_v3_purpose_workspace_id === 'string'
    && metadata.swarm_v3_purpose_workspace_id.length > 0
    && (!workspaceId || metadata.swarm_v3_purpose_workspace_id === workspaceId)
}
