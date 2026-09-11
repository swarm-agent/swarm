import { requestJson } from '../../../app/api'
import type { SessionSnapshot } from './desktop-v3-cache-types'

import { AUTOMATION_MANAGEMENT_PURPOSE, isAutomationManagementSession } from './desktop-automation-purpose'
export { isAutomationManagementSession } from './desktop-automation-purpose'
export interface AutomationConversationPage {
  sessions_by_id: Record<string, SessionSnapshot>
  session_order: string[]
  pagination: { has_more?: boolean; next_before_updated_at?: number; next_before_session_id?: string }
}
export function loadAutomationConversations(workspaceId: string, workspacePath: string, before?: AutomationConversationPage['pagination'], signal?: AbortSignal): Promise<AutomationConversationPage> {
  return requestJson('/v3/sessions:discover', {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, signal,
    body: JSON.stringify({ automation_management_workspace_id: workspaceId, workspace: { workspace_path: workspacePath }, recent: { limit: 20, before_updated_at: before?.next_before_updated_at, before_session_id: before?.next_before_session_id } }),
  })
}
// Explicit user gesture only. Retain clientRequestId on retry after uncertain failure.
export async function createAutomationConversation(workspacePath: string, clientRequestId: string): Promise<SessionSnapshot> {
  const result = await requestJson<{ session: SessionSnapshot }>('/v3/sessions', {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ client_request_id: clientRequestId, purpose: AUTOMATION_MANAGEMENT_PURPOSE, workspace_path: workspacePath, agent_name: 'swarm', title: 'Automation planning', mode: 'plan', worktree_mode: 'on', worktree_branch_name: `automation-management-${clientRequestId.slice(0, 8)}` }),
  })
  if (!result.session?.id || !isAutomationManagementSession(result.session)) throw new Error('Automation conversation identity was not returned.')
  return result.session
}
