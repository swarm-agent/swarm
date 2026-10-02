import { designRefKey, type DesignCandidate, type DesignRef, type DesignRevision, type ProjectDesign } from '../session-v3/design-api'
import type { MediaLibraryItem } from '../tools/media-library/types'

export const designNodeId = (session: string, ref: DesignRef) => JSON.stringify(['design', session, designRefKey(ref)])
export const designRequestId = (row: ProjectDesign) => JSON.stringify(['design-request', row.request.parent_session_id, row.request.id])
export function readyDesignRevision(candidate: DesignCandidate): DesignRevision | undefined {
  const attempt = candidate.attempts?.slice().reverse().find(attempt => attempt.state === 'succeeded' && attempt.result)
  if (!attempt?.result) return
  return { ref: attempt.result, kind: candidate.spec.kind, base: candidate.spec.base, plan_source: candidate.spec.plan_source, attempt }
}
export function designMediaItem(row: ProjectDesign, candidate: number, revision: DesignRevision): MediaLibraryItem {
  const session = row.request.parent_session_id
  return {
    source: 'independent-design', design: { projectId: row.project_id, requestId: row.request.id, candidate, revision },
    id: designNodeId(session, revision.ref), title: `${row.title} · Candidate ${candidate + 1} · Revision ${revision.ref.revision}`,
    filename: `design-r${revision.ref.revision}.${revision.kind === 'plan' ? 'txt' : 'html'}`,
    kind: 'animation', mediaType: revision.kind === 'plan' ? 'text/plain' : 'text/html', directUrl: '',
    createdAt: 0, formattedDate: '', formattedTime: '', dayKey: 'design', dayLabel: 'Designs',
    sessionId: session, sessionTitle: row.title, workspacePath: '', workspaceName: '',
    iterationGroupId: JSON.stringify(['design', session, revision.ref.artifact_id]), iterationGroupTitle: row.title,
    variantIndex: candidate + 1, totalVariants: row.request.candidates.length,
    parentId: revision.base ? designNodeId(session, revision.base) : undefined,
  }
}
export function designReadyItems(rows: readonly ProjectDesign[]): MediaLibraryItem[] {
  const items = new Map<string, MediaLibraryItem>()
  for (const row of rows) row.request.candidates.forEach((candidate, index) => {
    if (candidate.archived) return
    const revision = readyDesignRevision(candidate)
    if (revision) { const item = designMediaItem(row, index, revision); items.set(item.id, item) }
  })
  return [...items.values()]
}
export function designStatus(state: string) {
  return state === 'succeeded' ? 'ready' : state === 'pending' || state === 'accepted' ? 'queued' : state.split('_').join(' ')
}
