import { buildDesktopV3ChildCardHydrateInput, postDesktopV3SyncHydrate } from '../state/desktop-v3-sync-api'
import { projectConversationBatches } from '../orchestrate/project-entry-policy'
import type { SyncSnapshotResponse } from '../state/desktop-v3-cache-types'

// Sidebar rows need metadata/attention counts, never composer media capability.
export async function hydrateProjectConversationRows(ids: string[], signal: AbortSignal, publish: (response: SyncSnapshotResponse, ids: string[]) => void, read = postDesktopV3SyncHydrate) {
  const batches = projectConversationBatches(ids)
  let next = 0
  const failures: unknown[] = []
  await Promise.all(Array.from({ length: Math.min(3, batches.length) }, async () => {
    while (!signal.aborted && next < batches.length) {
      const batch = batches[next++]
      const input = buildDesktopV3ChildCardHydrateInput(batch, { permissionSummary: true })
      input.resources.session_view = false
      try {
        const response = await read(input, signal)
        if (!signal.aborted) publish(response, batch)
      } catch (error) { failures.push(error) }
    }
  }))
  if (failures.length) throw failures[0]
}
