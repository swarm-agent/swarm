import type { WorkerRecord } from '../state/desktop-workers-api'

/** Catalog entries are supplied only by the authenticated workspace overview. */
export interface ProposalWorkspaceCatalog {
  accountScopeId: string
  workspaces: ReadonlyArray<{ workspaceId?: string; path: string; workspaceName: string }>
}

export function proposalGoal(worker: WorkerRecord): string {
  const text = (worker.description?.trim() || worker.instructions.trim()).replace(/\s+/g, ' ')
  if (!text) return 'Goal not specified'
  return text.length > 200 ? `${text.slice(0, 197)}…` : text
}

export function proposalWorkspaces(worker: WorkerRecord, accountScopeId: string, catalog?: ProposalWorkspaceCatalog) {
  const authorized = worker.account_scope_id === accountScopeId
  const entries = authorized ? [...(catalog?.accountScopeId === accountScopeId ? catalog.workspaces : []), ...Object.values(worker.authorized_workspaces || {}).filter(view => view.available && view.path && view.name).map(view => ({ workspaceId: view.workspace_id, path: view.path!, workspaceName: view.name! }))] : []
  const proposed = worker.proposed_bindings || {}
  const approved = worker.local_bindings || {}
  const roles = [...new Set([...Object.keys(proposed), ...Object.keys(approved), ...(worker.workspace_requirements || []).filter(req => req.required).map(req => req.role)])]
  return roles.map(role => {
    const target = proposed[role] || approved[role] || ''
    const workspace = target ? entries.find(entry => entry.workspaceId === target || entry.path === target) : undefined
    const approvedWorkspace = approved[role] ? entries.find(entry => entry.workspaceId === approved[role] || entry.path === approved[role]) : undefined
    const name = workspace?.workspaceName.trim() || workspace?.path
    return {
      role, target, path: workspace?.path,
      approvedTarget: approved[role], approvedPath: approvedWorkspace?.path, approvedName: approvedWorkspace?.workspaceName || approvedWorkspace?.path,
      label: workspace ? `${name} — ${workspace.path}` : target ? `Unresolved workspace (${role})` : `Workspace required (${role}) — not assigned`,
      unresolved: !workspace,
      status: proposed[role] ? (approved[role] === proposed[role] ? 'Previously approved; proposal pending' : 'Proposed; not approved') : approved[role] ? 'Previously approved' : 'Unresolved requirement',
    }
  })
}
