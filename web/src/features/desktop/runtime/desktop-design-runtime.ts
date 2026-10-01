import { fetchDesignCatalog, fetchDesignHistory } from '../session-v3/design-api'
import { DesktopDesignState, designEventSession } from '../state/desktop-design-state'
import type { RealtimeMessage } from '../state/desktop-v3-cache-types'

export const desktopDesigns = new DesktopDesignState({ catalog: fetchDesignCatalog, history: fetchDesignHistory })
export function acceptDesktopDesignEvent(frame: RealtimeMessage) {
  const session = designEventSession(frame)
  if (session) desktopDesigns.invalidate(session)
}
