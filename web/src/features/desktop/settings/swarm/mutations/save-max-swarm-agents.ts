import { patchUISettings } from '../queries/get-ui-settings'
import { normalizeMaxSwarmAgents } from '../types/swarm-settings'
import type { UISettingsWire } from '../types/swarm-settings'

export async function saveMaxSwarmAgents(maxAgents: number): Promise<UISettingsWire> {
  return patchUISettings({
    chat: {
      max_swarm_agents: normalizeMaxSwarmAgents(maxAgents),
    },
  })
}
