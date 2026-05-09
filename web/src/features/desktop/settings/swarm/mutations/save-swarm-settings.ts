import { patchUISettings } from '../queries/get-ui-settings'
import { normalizeSwarmName, normalizeSwarmSettings } from '../types/swarm-settings'
import type { SwarmSettings } from '../types/swarm-settings'

export async function saveSwarmSettings(input: { name: string }): Promise<SwarmSettings> {
  const response = await patchUISettings({
    swarm: {
      name: normalizeSwarmName(input.name),
    },
  })

  return normalizeSwarmSettings(response)
}
