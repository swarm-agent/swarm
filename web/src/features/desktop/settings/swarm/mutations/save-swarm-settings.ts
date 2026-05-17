import { requestJson } from '../../../../../app/api'
import { patchUISettings } from '../queries/get-ui-settings'
import { normalizeSwarmName, normalizeSwarmSettings } from '../types/swarm-settings'
import type { SwarmSettings } from '../types/swarm-settings'
import type { SwarmTargetsResponse } from '../../../swarm/api/swarm-targets'

export async function saveSwarmSettings(input: { name: string }): Promise<SwarmSettings> {
  const response = await patchUISettings({
    swarm: {
      name: normalizeSwarmName(input.name),
    },
  })
  const settings = normalizeSwarmSettings(response)

  const targets = await requestJson<SwarmTargetsResponse>('/v1/swarm/targets', { cache: 'no-store' })
  const currentTargetName = targets.targets.find((target) => target.current)?.name?.trim() ?? ''
  if (!currentTargetName) {
    return settings
  }

  return {
    ...settings,
    name: currentTargetName,
    raw: {
      ...settings.raw,
      swarm: {
        ...(settings.raw.swarm ?? {}),
        name: currentTargetName,
      },
    },
  }
}
