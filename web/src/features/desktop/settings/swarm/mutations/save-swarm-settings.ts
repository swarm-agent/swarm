import { requestJson } from '../../../../../app/api'
import { normalizeDefaultNewSessionMode, normalizeSwarmName, normalizeSwarmSettings } from '../types/swarm-settings'
import type { SwarmSettings, UISettingsWire } from '../types/swarm-settings'

export async function saveSwarmSettings(input: { current: UISettingsWire; name: string; defaultNewSessionMode?: 'auto' | 'plan' }): Promise<SwarmSettings> {
  const payload: UISettingsWire = {
    ...input.current,
    chat: {
      ...(input.current.chat ?? {}),
      default_new_session_mode: normalizeDefaultNewSessionMode(input.defaultNewSessionMode ?? input.current.chat?.default_new_session_mode),
    },
    swarm: {
      ...(input.current.swarm ?? {}),
      name: normalizeSwarmName(input.name),
    },
  }

  const response = await requestJson<UISettingsWire>('/v1/ui/settings', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify(payload),
  })

  return normalizeSwarmSettings(response)
}
