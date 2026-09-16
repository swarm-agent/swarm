import { apiFetch } from '../../../../app/api'
import { mapWorkspaceRepositoryState, WorkspaceRepositoryPrerequisiteError, type WorkspaceRepositoryState, type WorkspaceRepositoryStateWire } from './workspace-repository'

export interface RepositoryReview {
 repository: WorkspaceRepositoryState
 digest: string
 files: { path: string; size: number; selectable: boolean }[]
 warning: string
}
export interface BaselineRequest {
 path: string; expected_resolved_path: string; review_digest: string
 selected_paths: string[]; confirm_baseline: boolean; confirm_omissions: boolean
}
async function responseBody(response: Response) {
 const payload = await response.json()
 if (!response.ok || payload.ok !== true) {
  if (payload.repository) throw new WorkspaceRepositoryPrerequisiteError(mapWorkspaceRepositoryState(payload.repository), payload.error)
  throw new Error(payload.error || 'Daemon did not acknowledge repository operation')
 }
 return payload
}
export async function reviewRepository(path: string): Promise<RepositoryReview> {
 const payload = await responseBody(await apiFetch(`/v1/workspace/repository/review?path=${encodeURIComponent(path)}`))
 const review = payload.review
 if (!review?.digest || !review.repository?.path || !Array.isArray(review.files)) throw new Error('Daemon did not return a complete content review')
 return { ...review, repository: mapWorkspaceRepositoryState(review.repository as WorkspaceRepositoryStateWire) }
}
export async function prepareBaseline(request: BaselineRequest): Promise<WorkspaceRepositoryState> {
 const payload = await responseBody(await apiFetch('/v1/workspace/repository/baseline', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(request) }))
 const state = mapWorkspaceRepositoryState(payload.repository || {})
 if (state.state !== 'ready' || !state.headCommit) throw new Error('Daemon did not acknowledge a committed baseline')
 return state
}

export async function inspectRepository(path: string): Promise<WorkspaceRepositoryState> {
 const payload = await responseBody(await apiFetch(`/v1/workspace/repository?path=${encodeURIComponent(path)}`))
 if (!payload.repository?.path || !payload.repository?.state) throw new Error('Daemon did not acknowledge repository inspection')
 return mapWorkspaceRepositoryState(payload.repository)
}
