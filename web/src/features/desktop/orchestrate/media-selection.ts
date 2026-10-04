import type { RunningTask } from './orchestrate-types'
import type { DesignRef, ProjectDesign } from '../session-v3/design-api'
import { designThreads, readyDesignRevision } from './design-media-task'
import { mediaTaskThreads } from './media-task-card'

export type MediaArchiveRecord = { key: string; title: string } & (
  { kind: 'task'; task: RunningTask } |
  { kind: 'design'; session: string; reference: DesignRef; version: number }
)
export interface MediaSelectionCard { id: string; title: string; records: MediaArchiveRecord[]; unavailable: number }
export const mediaTaskKey = (id: string) => JSON.stringify(['task', id])

// Counts are loaded, visible cards, not outputs. Selection captures exact record
// identities at click time: later turns are never implicitly included. Designs
// archive whole artifacts (all revisions), matching DesignArchiveButton semantics.
export function mediaSelectionCards(tasks: readonly RunningTask[], designs: readonly ProjectDesign[]): MediaSelectionCard[] {
  return [
    ...designThreads(designs.filter(row => row.request.candidates.some(candidate => !candidate.archived))).map(thread => {
      const records = new Map<string, MediaArchiveRecord>(); let unavailable = 0
      for (const row of thread.turns) for (const candidate of row.request.candidates) {
        if (candidate.archived) continue
        const revision = readyDesignRevision(candidate)
        if (!revision) { unavailable++; continue }
        const session = row.request.parent_session_id
        const key = JSON.stringify(['design', session, revision.ref.artifact_id])
        const previous = records.get(key)
        if (previous?.kind === 'design' && previous.reference.revision > revision.ref.revision) continue
        records.set(key, { key, title: row.title, kind: 'design', session, reference: revision.ref, version: candidate.archive_version ?? 0 })
      }
      return { id: thread.id, title: thread.turns[0].title, records: [...records.values()], unavailable }
    }),
    ...mediaTaskThreads(tasks).map(thread => ({ id: thread.id, title: thread.turns[0].title, unavailable: 0,
      records: thread.turns.map(task => ({ key: mediaTaskKey(task.id), title: task.title, kind: 'task' as const, task })),
    })),
  ]
}
