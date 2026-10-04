import { requestJson } from '../../../app/api'
import { createProjectConversation } from '../orchestrate/project-conversations'
import type { CreationProject, CreationWorkspace, ProjectCreationDraft, ProjectCreationState } from '../state/project-creation'

export interface ProjectCreationDeps {
  request: typeof requestJson
  conversation: typeof createProjectConversation
  saveDraft: (draft: ProjectCreationDraft) => void
  clearDraft: () => void
}

// Owns mutations and HTTP receipts; components never synthesize project context.
export class ProjectCreationRuntime {
  private state: ProjectCreationState
  private listeners = new Set<() => void>()
  private refreshing: Promise<void> | undefined
  private invalidated = false
  private mutationVersion = 0
  constructor(private deps: ProjectCreationDeps, draft?: ProjectCreationDraft, project?: CreationProject) {
    this.state = { draft, project, busy: false, error: '' }
  }
  snapshot = () => this.state
  subscribe = (listener: () => void) => { this.listeners.add(listener); return () => { this.listeners.delete(listener) } }
  private update(patch: Partial<ProjectCreationState>) {
    this.state = { ...this.state, ...patch }
    for (const listener of this.listeners) listener()
  }
  async create(draft: ProjectCreationDraft): Promise<void> {
    if (this.state.busy || this.state.project) return
    // Freeze the original payload even after a lost response or a remount.
    const original = this.state.draft ?? draft
    this.update({ busy: true, error: '', draft: original })
    try {
      this.deps.saveDraft(original)
      const { project } = await this.deps.request<{ project: CreationProject }>('/v3/projects', {
        method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(original),
      })
      if (!project?.id || !project.context_generation) throw new Error('Project creation returned no generation identity. Retry the same request.')
      this.update({ project })
      this.deps.clearDraft()
    } catch (error) { this.fail(error) }
    finally { this.update({ busy: false }) }
  }
  private fail(error: unknown) { this.update({ error: error instanceof Error ? error.message : 'Project operation failed. Please retry.' }) }
  refresh = (): Promise<void> => {
    if (this.refreshing) { this.invalidated = true; return this.refreshing }
    const id = this.state.project?.id
    if (!id) return Promise.resolve()
    const version = this.mutationVersion
    this.refreshing = this.deps.request<{ project: CreationProject }>(`/v3/projects/${encodeURIComponent(id)}`)
      .then(({ project }) => { if (project?.id !== id) throw new Error('Project response identity mismatch'); if (version === this.mutationVersion) this.update({ project, error: '' }) })
      .catch(error => { if (version === this.mutationVersion) this.fail(error) }).finally(() => {
        this.refreshing = undefined
        if (this.invalidated) { this.invalidated = false; void this.refresh() }
      })
    return this.refreshing
  }
  async retry(): Promise<void> {
    const project = this.state.project
    if (!project || this.state.busy) return
    this.mutationVersion++
    this.update({ busy: true, error: '' })
    try {
      const result = await this.deps.request<{ project: CreationProject }>(`/v3/projects/${encodeURIComponent(project.id)}/context:retry`, {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ expected_attempt: project.context_generation?.attempt ?? 0 }),
      })
      if (result.project?.id !== project.id) throw new Error('Retry returned a different project')
      this.mutationVersion++
      this.update({ project: result.project })
    } catch (error) { this.fail(error) }
    finally { this.update({ busy: false }) }
  }
  async open(): Promise<string | undefined> {
    const project = this.state.project
    if (!project || project.context_generation?.status !== 'ready' || this.state.busy) return
    this.update({ busy: true, error: '' })
    try {
      // Stable across remounts and lost responses; the backend binds it to this project.
      const sessionId = this.state.sessionId ?? await this.deps.conversation(project.id, `desktop-project-first:${project.id}`)
      this.update({ sessionId })
      return sessionId
    } catch (error) { this.fail(error); return undefined }
    finally { this.update({ busy: false }) }
  }
}

export async function registerProjectFolder(path: string, request = requestJson): Promise<CreationWorkspace> {
  try {
    const { workspace } = await request<{ workspace: { workspace_id: string; resolved_path: string; workspace_name?: string } }>('/v1/workspace/add', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ path: path.trim(), context_only: true, make_current: false }),
    })
    if (!workspace?.workspace_id || !workspace.resolved_path) throw new Error('Folder registration returned no workspace identity. Retry adding the folder.')
    return { workspace_id: workspace.workspace_id, path: workspace.resolved_path, label: workspace.workspace_name || workspace.resolved_path, role: 'auxiliary' }
  } catch (error) {
    // A prior add may have succeeded with a lost receipt, or another view added it.
    // Only a matching account catalog row can turn this failure into a selection.
    const { workspaces = [] } = await request<{ workspaces?: Array<{ id?: string; workspace_id?: string; path: string; name?: string }> }>('/v1/workspace/list?limit=200')
    const existing = workspaces.find(w => w.path === path.trim().replace(/\/$/, '') && (w.id || w.workspace_id))
    if (!existing) throw error
    return { workspace_id: (existing.id || existing.workspace_id)!, path: existing.path, label: existing.name || existing.path, role: 'auxiliary' }
  }
}

export const projectCreationDeps = { request: requestJson, conversation: createProjectConversation }
