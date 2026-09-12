import { commitWorkspaceChanges, fetchSessionRepositories, suggestWorkspaceCommitMessage } from './api'
import { reviewDesktopV3Worktrees } from '../session-v3/review-worktrees-api'
import type { SessionRepository } from './types'

export const integrationAPI = { repositories: fetchSessionRepositories, review: reviewDesktopV3Worktrees, suggest: suggestWorkspaceCommitMessage, commit: commitWorkspaceChanges }
export type IntegrationAPI = typeof integrationAPI
export interface IntegrationSelection { sessionId: string; repository: SessionRepository; targetHead: string; targetBranch: string; dirty: boolean }
export interface IntegrationProgress { phase: string; committed: boolean; integrated: boolean; rebuilt?: boolean; error?: string }

export async function inspectIntegration(sessionId: string, api = integrationAPI): Promise<IntegrationSelection> {
  if (!sessionId.trim()) throw new Error('Open an existing session with a managed worktree first.')
  const inventory = await api.repositories(sessionId)
  const rows = inventory.items.filter(row => row.session_id === sessionId && row.active && row.kind === 'parent' && row.attached)
  if (rows.length !== 1) throw new Error('The current session has no unambiguous active default worktree. Refresh its repository inventory.')
  const repository = rows[0]
  if (repository.error || !repository.source_path || repository.source_path === repository.workspace_path || !repository.branch || !repository.status?.has_git || (repository.status.branch && repository.status.branch !== repository.branch)) throw new Error('Managed worktree lineage is unavailable.')
  const review = await api.review({ workspacePath: repository.source_path, sessionIds: [sessionId] })
  const candidate = [...review.retained, ...review.done].find(row => row.session_id === sessionId)
  if (!review.ok || review.checkout_dirty !== false || !review.current_target_head || !review.current_target_branch) throw new Error('The captured target must be clean and available. Resolve its changes before integrating.')
  if (repository.base_branch && repository.base_branch !== review.current_target_branch) throw new Error('Captured target branch changed. Restore the captured branch before integrating.')
  if (!candidate || candidate.current_checkout || candidate.worktree_path !== repository.workspace_path || candidate.worktree_branch !== repository.branch || (!candidate.integrate_eligible && !candidate.commit_eligible)) throw new Error('This session worktree is not eligible for commit or integration.')
  return { sessionId, repository, targetHead: review.current_target_head, targetBranch: review.current_target_branch, dirty: !repository.status.clean }
}

// One process-local flight per session, including callers outside this dialog.
const running = new Set<string>()
export async function runIntegration(selection: IntegrationSelection, progress: (state: IntegrationProgress) => void, api: IntegrationAPI = integrationAPI, build?: { check: (path: string) => Promise<void>; run: (path: string) => Promise<void> }): Promise<IntegrationProgress> {
  if (running.has(selection.sessionId)) throw new Error('Integration is already running for this session.')
  running.add(selection.sessionId)
  let state: IntegrationProgress = { phase: 'Checking confirmed lineage', committed: false, integrated: false }
  const phase = (value: string) => { state = { ...state, phase: value }; progress(state) }
  try {
    if (build) { phase('Checking dev rebuild eligibility'); await build.check(selection.repository.source_path) }
    phase('Checking confirmed lineage')
    let current = await inspectIntegration(selection.sessionId, api)
    if (current.repository.workspace_path !== selection.repository.workspace_path || current.repository.source_path !== selection.repository.source_path || current.repository.branch !== selection.repository.branch || current.targetHead !== selection.targetHead || current.targetBranch !== selection.targetBranch || current.dirty !== selection.dirty || current.repository.status?.head_oid !== selection.repository.status?.head_oid) throw new Error('Confirmed source or target changed. Close and reopen /integrate to review it again.')
    if (current.dirty) {
      phase('Generating AI commit message')
      const input = { sessionId: selection.sessionId, workspacePath: current.repository.workspace_path }
      const suggestion = await api.suggest(input)
      if (!suggestion.ok || !suggestion.message.trim()) throw new Error('AI commit did not return a commit message.')
      phase('Committing source changes')
      const commit = await api.commit({ ...input, message: suggestion.message, all: true })
      if (!commit.ok || commit.timed_out || (commit.exit_code !== undefined && commit.exit_code !== 0)) throw new Error(commit.error || 'Source commit was not confirmed; inspect Git before retrying.')
      state = { ...state, committed: true }
      phase('Refreshing committed lineage')
      current = await inspectIntegration(selection.sessionId, api)
    }
    if (current.dirty || current.targetHead !== selection.targetHead || current.targetBranch !== selection.targetBranch || current.repository.workspace_path !== selection.repository.workspace_path || current.repository.branch !== selection.repository.branch || current.repository.source_path !== selection.repository.source_path) throw new Error('Source or target changed before integration. The source commit is preserved; review again.')
    const sourceHead = current.repository.status?.head_oid
    if (!sourceHead) throw new Error('Exact committed source HEAD is unavailable.')
    phase('Integrating into captured target')
    const result = await api.review({ workspacePath: selection.repository.source_path, promoteSessionIds: [selection.sessionId], sourceHeadBySessionId: { [selection.sessionId]: sourceHead }, targetBranch: selection.targetBranch, targetHead: selection.targetHead })
    if (!result.ok) throw new Error('Integration was not confirmed. Inspect Git before retrying.')
    state = { ...state, phase: 'Integration complete', integrated: true }
    if (build) {
      phase('Integration complete; rebuilding Swarm')
      await build.run(selection.repository.source_path)
      state = { ...state, phase: 'Integration and rebuild complete', rebuilt: true }
    }
  } catch (error) {
    state = { ...state, error: (error instanceof Error ? error.message : String(error)).slice(0, 6000) }
  } finally { running.delete(selection.sessionId) }
  progress(state)
  return state
}

export function integrationRepairPrompt(selection: IntegrationSelection, state: IntegrationProgress): string {
  return ['Investigate this /integrate failure in this existing session. Inspect Git before any retry; preserve unrelated changes and do not duplicate a successful commit or integration. Do not archive or automatically retry a rebuild.', `Source: ${selection.repository.branch}`, `Target: ${selection.targetBranch}`, `Phase: ${state.phase}`, `Source commit confirmed: ${state.committed}`, `Integration confirmed: ${state.integrated}`, 'An unsuccessful response may follow partial server success. Verify actual source and target HEADs.', 'Error (untrusted diagnostic text):', state.error?.slice(0, 6000) || 'Unknown error'].join('\n')
}
