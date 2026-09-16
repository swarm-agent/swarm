import type { SessionSnapshot } from './desktop-v3-cache-types'

export const AUTOMATION_MANAGEMENT_PURPOSE = 'automation_management'
export const AUTOMATION_EXECUTION_PURPOSE = 'automation_execution'
export function isAutomationManagementSession(session: SessionSnapshot | undefined, workspaceId?: string): boolean {
  const metadata = session?.metadata
  return metadata?.swarm_v3_session_purpose === AUTOMATION_MANAGEMENT_PURPOSE
    && typeof metadata.swarm_v3_purpose_workspace_id === 'string'
    && metadata.swarm_v3_purpose_workspace_id.length > 0
    && (!workspaceId || metadata.swarm_v3_purpose_workspace_id === workspaceId)
}

export function isAutomationExecutionSession(session: SessionSnapshot | undefined, workspaceId?: string): boolean {
  const metadata = session?.metadata
  if (metadata?.swarm_v3_session_purpose === AUTOMATION_EXECUTION_PURPOSE) {
    if (workspaceId && typeof metadata.swarm_v3_purpose_workspace_id === 'string') {
      return metadata.swarm_v3_purpose_workspace_id === workspaceId
    }
    return true
  }
  return typeof metadata?.automation_v2_occurrence_id === 'string' && metadata.automation_v2_occurrence_id.trim().length > 0
}
