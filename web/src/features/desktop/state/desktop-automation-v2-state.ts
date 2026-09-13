import type { AutomationV2Read, AutomationV2Response } from './desktop-automation-v2-api'
import type { DesktopV3CacheState } from './desktop-v3-cache-types'
export interface AutomationV2Page { input: AutomationV2Read; data?: AutomationV2Response; requestId?: string; generation: number; loading: boolean; stale: boolean; error?: string }
export type AutomationV2Pages = Record<string, AutomationV2Page>
export type AutomationV2CacheAction =
  | { type: 'automationV2.begin'; key: string; input: AutomationV2Read; requestId: string }
  | { type: 'automationV2.finish'; key: string; requestId: string; generation: number; data?: AutomationV2Response; error?: string }
  | { type: 'automationV2.invalidate'; workspaceId?: string; sessionId?: string }
  | { type: 'automationV2.evict'; key: string }
export const automationV2PageKey = (input: AutomationV2Read) => JSON.stringify(Object.entries(input).filter(([, v]) => v !== undefined).sort(([a], [b]) => a.localeCompare(b)))
export function reduceAutomationV2Pages(pages: AutomationV2Pages, action: AutomationV2CacheAction): AutomationV2Pages {
  if (action.type === 'automationV2.invalidate') return Object.fromEntries(Object.entries(pages).map(([key, page]) => [key, (!action.workspaceId || page.input.workspace_id === action.workspaceId) && (!action.sessionId || !page.input.session_id || page.input.session_id === action.sessionId) ? { ...page, generation: page.generation + 1, stale: true } : page]))
  if (action.type === 'automationV2.evict') { const next = { ...pages }; delete next[action.key]; return next }
  const old = pages[action.key]
  if (action.type === 'automationV2.begin') return { ...pages, [action.key]: { ...old, input: action.input, requestId: action.requestId, generation: old?.generation ?? 0, loading: true, stale: old?.stale ?? true, error: undefined } }
  if (!old || old.requestId !== action.requestId) return pages
  if (old.generation !== action.generation) return { ...pages, [action.key]: { ...old, loading: false, requestId: undefined } }
  return { ...pages, [action.key]: { ...old, data: action.data ?? old.data, loading: false, requestId: undefined, stale: !!action.error, error: action.error } }
}
// Pending identity is a permission resource, never an invented automation record.
export function selectAutomationV2Identity(state: DesktopV3CacheState, sessionId: string): 'pending' | 'accepted' | undefined {
  const record = state.sessionsById[sessionId]
  if (record?.kind === 'full' && record.session.automation_v2) return 'accepted'
  const permissions = state.permissionsBySession[sessionId]
  if (permissions?.some(p => p.status === 'pending' && p.requirement === 'automation_v2_acceptance')) return 'pending'
  for (const page of Object.values(state.automationV2Pages)) if (page.data?.records?.some(r => r.session_id === sessionId) || page.data?.record?.session_id === sessionId) return 'accepted'
  return undefined
}
