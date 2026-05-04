import { patchUISettings } from '../queries/get-ui-settings'
import { normalizeDefaultNewSessionMode, normalizeSwarmName, normalizeSwarmSettings } from '../types/swarm-settings'
import type { SwarmSettings } from '../types/swarm-settings'

export async function saveSwarmSettings(input: { current?: { chat?: { default_new_session_mode?: 'auto' | 'plan' } }; name: string; defaultNewSessionMode?: 'auto' | 'plan' }): Promise<SwarmSettings> {
  const response = await patchUISettings({
    chat: {
      default_new_session_mode: normalizeDefaultNewSessionMode(input.defaultNewSessionMode ?? input.current?.chat?.default_new_session_mode),
    },
    swarm: {
      name: normalizeSwarmName(input.name),
    },
  })

  return normalizeSwarmSettings(response)
}
