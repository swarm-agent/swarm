import { subscribeDesktopSessionReset } from '../../../app/api'
import { archiveQueue } from './archive-queue'
import { fetchDesignCatalog, fetchDesignHistory, fetchProjectDesigns, fetchDesignEdits, setDesignArchived, type DesignRef } from '../session-v3/design-api'
import { DesktopDesignState, designEventSession } from '../state/desktop-design-state'
import type { RealtimeMessage } from '../state/desktop-v3-cache-types'

let authEpoch = 0
let archiveController = new AbortController()
let state: DesktopDesignState | undefined
function getDesignState(): DesktopDesignState {
  // Production chunks can be cyclic. Do not construct imported classes (or read
  // the auth listener registry) until the module graph has finished evaluating.
  if (!state) {
    const created = new DesktopDesignState({ catalog: fetchDesignCatalog, history: fetchDesignHistory, project: fetchProjectDesigns, edits: fetchDesignEdits })
    subscribeDesktopSessionReset(() => { authEpoch++; archiveController.abort(); archiveController = new AbortController(); created.reset() })
    state = created
  }
  return state
}

// Stable facade and resource identities, including across authentication resets.
// An invalidation before any view exists has nothing to refresh or discard.
export const desktopDesigns = {
  moreEdits: (session: string) => getDesignState().moreEdits(session),
  editRequests: (session: string) => getDesignState().editRequests(session),
  archive: async (session: string, ref: DesignRef, version: number, archived: boolean, key: string, isCurrent: () => boolean = () => true) => {
    const current = getDesignState()
    const epoch = authEpoch
    const signal = archiveController.signal
    const receipt = await archiveQueue.run(JSON.stringify([epoch, 'design', session, ref.artifact_id]), () => {
      if (signal.aborted) throw new Error('Account changed')
      if (!isCurrent()) throw new Error('Project changed')
      return setDesignArchived(session, ref, version, archived, key, signal)
    })
    if (epoch !== authEpoch) throw new Error('Account changed')
    current.acceptArchive(session, receipt)
  },
  project: (project: string, view: 'active' | 'archived' = 'active') => getDesignState().project(project, view),
  moreProject: (project: string, view: 'active' | 'archived' = 'active') => getDesignState().moreProject(project, view),
  invalidateProject: (project?: string) => state?.invalidateProject(project),
  catalog: (session: string) => getDesignState().catalog(session),
  history: (session: string, artifact: string) => getDesignState().history(session, artifact),
  moreRequests: (session: string) => getDesignState().moreRequests(session),
  moreHistory: (session: string, artifact: string) => getDesignState().moreHistory(session, artifact),
  reset: () => state?.reset(),
  invalidate: (session?: string) => state?.invalidate(session),
}
export function acceptDesktopDesignEvent(frame: RealtimeMessage) {
  if (frame.kind === 'project.updated' && frame.project_id) desktopDesigns.invalidateProject(frame.project_id)
  if (frame.kind === 'event' && frame.event && typeof frame.event === 'object') {
    const event = frame.event as { event_type?: string; payload?: { project_id?: string }; project_id?: string }
    if (event.event_type === 'project.updated') {
      const project = event.project_id || event.payload?.project_id
      if (project) desktopDesigns.invalidateProject(project)
    }
  }
  const session = designEventSession(frame)
  if (session) desktopDesigns.invalidate(session)
}
