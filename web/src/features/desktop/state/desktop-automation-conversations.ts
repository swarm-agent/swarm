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
export async function createAutomationConversation(
  workspacePath: string,
  clientRequestId: string,
  workspaceId?: string,
  title?: string,
  workspaceBindingId?: string,
): Promise<SessionSnapshot> {
  const result = await requestJson<{ session: SessionSnapshot }>('/v3/sessions', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      client_request_id: clientRequestId,
      purpose: AUTOMATION_MANAGEMENT_PURPOSE,
      workspace_path: workspacePath,
      ...(workspaceBindingId ? { workspace_binding_id: workspaceBindingId } : {}),
      ...(workspaceId ? { workspace_id: workspaceId } : {}),
      agent_name: 'swarm',
      ...(title ? { title } : {}),
      mode: 'plan',
    }),
  })
  if (!result.session?.id || !isAutomationManagementSession(result.session, workspaceId)) {
    throw new Error('Automation conversation identity was not returned.')
  }
  return result.session
}
