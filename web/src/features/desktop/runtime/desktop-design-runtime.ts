import { subscribeDesktopSessionReset } from '../../../app/api'
import { fetchDesignCatalog, fetchDesignHistory, fetchProjectDesigns, fetchDesignEdits } from '../session-v3/design-api'
import { DesktopDesignState, designEventSession } from '../state/desktop-design-state'
import type { RealtimeMessage } from '../state/desktop-v3-cache-types'

let state: DesktopDesignState | undefined
function getDesignState(): DesktopDesignState {
  // Production chunks can be cyclic. Do not construct imported classes (or read
  // the auth listener registry) until the module graph has finished evaluating.
  if (!state) {
    const created = new DesktopDesignState({ catalog: fetchDesignCatalog, history: fetchDesignHistory, project: fetchProjectDesigns, edits: fetchDesignEdits })
    subscribeDesktopSessionReset(() => created.reset())
    state = created
  }
  return state
}

// Stable facade and resource identities, including across authentication resets.
// An invalidation before any view exists has nothing to refresh or discard.
export const desktopDesigns = {
  moreEdits: (session: string) => getDesignState().moreEdits(session),
  editRequests: (session: string) => getDesignState().editRequests(session),
  project: (project: string) => getDesignState().project(project),
  moreProject: (project: string) => getDesignState().moreProject(project),
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
