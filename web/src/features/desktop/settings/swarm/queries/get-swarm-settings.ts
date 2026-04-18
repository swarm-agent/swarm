import { requestJson } from '../../../../../app/api'
import { normalizeSwarmSettings } from '../types/swarm-settings'
import type { SwarmSettings, UISettingsWire } from '../types/swarm-settings'

export async function getSwarmSettings(): Promise<SwarmSettings> {
  const response = await requestJson<UISettingsWire>('/v1/ui/settings')
  return normalizeSwarmSettings(response)
}
