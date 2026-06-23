import { compactSessionV3, type CompactSessionV3Result } from '../chat/queries/chat-queries'
import { dispatchDesktopV3Cache } from '../state/desktop-v3-cache-store'
import { buildDesktopV3SelectedSessionHydrateInput, postDesktopV3SyncHydrate, type DesktopV3HydrateInput } from '../state/desktop-v3-sync-api'
import type { SyncSnapshotResponse } from '../state/desktop-v3-cache-types'
import { hydrateResponseToAction } from '../state/desktop-v3-cache-wire'

export interface CompactDesktopV3SessionInput {
  sessionId: string
  note?: string | null
  agentName?: string | null
  instructions?: string | null
  clientRequestId?: string | null
}

interface DesktopV3CompactSessionFlowDeps {
  compact: typeof compactSessionV3
  hydrate: (input: DesktopV3HydrateInput) => Promise<SyncSnapshotResponse>
  dispatch: typeof dispatchDesktopV3Cache
}

let compactFlowDeps: DesktopV3CompactSessionFlowDeps = {
  compact: compactSessionV3,
  hydrate: postDesktopV3SyncHydrate,
  dispatch: dispatchDesktopV3Cache,
}

export function setDesktopV3CompactSessionFlowDepsForTests(
  deps: Partial<DesktopV3CompactSessionFlowDeps>,
): () => void {
  const previous = compactFlowDeps
  compactFlowDeps = { ...compactFlowDeps, ...deps }
  return () => {
    compactFlowDeps = previous
  }
}

export async function compactDesktopV3Session(
  input: CompactDesktopV3SessionInput,
): Promise<CompactSessionV3Result> {
  const sessionId = input.sessionId.trim()
  if (!sessionId) throw new Error('Desktop V3 compact requires session_id')

  const response = await compactFlowDeps.compact(sessionId, {
    note: input.note,
    agentName: input.agentName,
    instructions: input.instructions,
    clientRequestId: input.clientRequestId,
  })
  const status = response.status?.trim().toLowerCase() ?? ''
  const cursor = response.realtimeOutbox?.endpointCursor?.trim() ?? ''
  if (!cursor) {
    throw new Error(response.error || 'Desktop V3 compact did not return a terminal hydrate cursor')
  }

  const hydrateInput = buildDesktopV3SelectedSessionHydrateInput(sessionId)
  hydrateInput.known_sessions = {
    [sessionId]: { endpoint_cursor: cursor },
  }
  const hydrateResponse = await compactFlowDeps.hydrate(hydrateInput)
  compactFlowDeps.dispatch(hydrateResponseToAction(hydrateResponse, [sessionId]))

  if (response.ok === false || (status && status !== 'completed')) {
    throw new Error(response.error || `Desktop V3 compact ended with status ${status || 'unknown'}`)
  }
  return response
}
