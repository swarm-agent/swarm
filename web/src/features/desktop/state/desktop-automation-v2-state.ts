import { automationV2PermissionProposal, type AutomationV2Proposal, type AutomationV2Read, type AutomationV2Response } from './desktop-automation-v2-api'
import type { DesktopV3CacheState } from './desktop-v3-cache-types'
import type { DesktopPermissionRecord } from '../types/realtime'

/** A worker awaiting user review is a permission on an ordinary authoring chat. */
export function selectPendingWorkerSidebarReviews(state: DesktopV3CacheState): Array<{ permission: DesktopPermissionRecord; proposal: AutomationV2Proposal }> {
  const reviews: Array<{ permission: DesktopPermissionRecord; proposal: AutomationV2Proposal }> = []
  for (const [sessionId, permissions] of Object.entries(state.permissionsBySession)) {
    if (state.tombstonesBySession[sessionId]) continue
    for (const permission of permissions) {
      if (permission.status !== 'pending' || permission.requirement !== 'automation_v2_acceptance') continue
      const proposal = automationV2PermissionProposal(permission)
      if (!proposal || !proposal.session_id || proposal.session_id !== sessionId || !proposal.workspace_id) continue
      reviews.push({ permission, proposal })
    }
  }
  return reviews
}
export interface AutomationV2Page { input: AutomationV2Read; data?: AutomationV2Response; requestId?: string; generation: number; loading: boolean; stale: boolean; error?: string }
export type AutomationV2Pages = Record<string, AutomationV2Page>
export type AutomationV2CacheAction =
  | { type: 'automationV2.begin'; key: string; input: AutomationV2Read; requestId: string }
  | { type: 'automationV2.finish'; key: string; requestId: string; generation: number; data?: AutomationV2Response; error?: string }
  | { type: 'automationV2.invalidate'; workspaceId?: string; sessionId?: string }
  | { type: 'automationV2.evict'; key: string }
export const automationV2PageKey = (input: AutomationV2Read) => JSON.stringify(Object.entries(input).filter(([, v]) => v !== undefined).sort(([a], [b]) => a.localeCompare(b)))
export function reduceAutomationV2Pages(pages: AutomationV2Pages = {}, action: AutomationV2CacheAction): AutomationV2Pages {
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
  if (permissions?.some(p => String(p.status).toLowerCase() === 'pending' && String(p.requirement).toLowerCase() === 'automation_v2_acceptance')) return 'pending'
  for (const page of Object.values(state.automationV2Pages)) if (page.data?.records?.some(r => r.session_id === sessionId) || page.data?.record?.session_id === sessionId) return 'accepted'
  return undefined
}

export function selectPendingAutomationV2Proposals(state: DesktopV3CacheState, workspaceId?: string): AutomationV2Proposal[] {
  const proposals: AutomationV2Proposal[] = []
  const seenProposalIds = new Set<string>()

  for (const permissions of Object.values(state.permissionsBySession ?? {})) {
    if (!permissions) continue
    for (const permission of permissions) {
      const status = String(permission.status || '').toLowerCase()
      const req = String(permission.requirement || '').toLowerCase()
      if (status !== 'pending' || req !== 'automation_v2_acceptance') continue
      const proposal = automationV2PermissionProposal(permission)
      if (!proposal || !proposal.session_id || state.tombstonesBySession[proposal.session_id]) continue
      const sessionRec = state.sessionsById[proposal.session_id]
      const sessionWorkspaceId = sessionRec?.kind === 'full'
        ? (sessionRec.session.automation_v2?.workspace_id ||
           sessionRec.session.automation?.workspace_id ||
           sessionRec.session.workspace_grants?.find((g) => g.workspace_id)?.workspace_id ||
           sessionRec.session.metadata?.swarm_v3_purpose_workspace_id)
        : undefined
      const effectiveWorkspaceId = proposal.workspace_id || sessionWorkspaceId
      if (workspaceId && effectiveWorkspaceId !== workspaceId) continue
      if (seenProposalIds.has(proposal.proposal_id)) continue
      seenProposalIds.add(proposal.proposal_id)
      proposals.push(proposal)
    }
  }

  for (const page of Object.values(state.automationV2Pages ?? {})) {
    if (page.input.action === 'review' && page.data?.proposal) {
      const proposal = page.data.proposal
      if (!proposal.session_id || state.tombstonesBySession[proposal.session_id]) continue
      const sessionRec = state.sessionsById[proposal.session_id]
      const sessionWorkspaceId = sessionRec?.kind === 'full'
        ? (sessionRec.session.automation_v2?.workspace_id ||
           sessionRec.session.automation?.workspace_id ||
           sessionRec.session.workspace_grants?.find((g) => g.workspace_id)?.workspace_id ||
           sessionRec.session.metadata?.swarm_v3_purpose_workspace_id)
        : undefined
      const effectiveWorkspaceId = proposal.workspace_id || sessionWorkspaceId
      if (workspaceId && effectiveWorkspaceId !== workspaceId) continue
      if (seenProposalIds.has(proposal.proposal_id)) continue
      const permRecord = (state.permissionsBySession?.[proposal.session_id] ?? []).find(
        p => p.id === 'permission_' + proposal.proposal_id ||
             (String(p.requirement || '').toLowerCase() === 'automation_v2_acceptance' &&
              (p.toolArguments?.includes(proposal.proposal_id) || (p as any).tool_arguments?.includes(proposal.proposal_id)))
      )
      // A hydrated empty permission list is authoritative: an old review page
      // cannot resurrect a worker card after acceptance or decline.
      if (state.permissionsBySession?.[proposal.session_id] !== undefined &&
          (!permRecord || String(permRecord.status || '').toLowerCase() !== 'pending')) {
        continue
      }
      const isAccepted = Object.values(state.automationV2Pages ?? {}).some(
        p => p.data?.records?.some(r => r.session_id === proposal.session_id) || p.data?.record?.session_id === proposal.session_id
      ) || (sessionRec?.kind === 'full' && Boolean(sessionRec.session.automation_v2))
      if (!isAccepted) {
        seenProposalIds.add(proposal.proposal_id)
        proposals.push(proposal)
      }
    }
  }

  return proposals
}
