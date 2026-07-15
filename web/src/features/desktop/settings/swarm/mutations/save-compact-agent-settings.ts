import { patchUISettings } from '../queries/get-ui-settings'
import { type UICompactAgentSettingsWire, type UISettingsWire, withCompactAgentSettings } from '../types/swarm-settings'

export async function saveCompactAgentSettings(input: { current: UISettingsWire; compact: UICompactAgentSettingsWire }): Promise<UISettingsWire> {
  return patchUISettings(withCompactAgentSettings(input.current, input.compact))
}
