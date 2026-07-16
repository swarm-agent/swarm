import { patchUISettings } from '../queries/get-ui-settings'
import {
  type UICompactAgentSettingsWire,
  type UISettingsWire,
  withCompactAgentSettings,
  withExplorerAgentSettings,
} from '../types/swarm-settings'

export async function saveSystemAgentSettings(input: {
  current: UISettingsWire
  agent: 'compact' | 'explorer'
  settings: UICompactAgentSettingsWire
}): Promise<UISettingsWire> {
  const next = input.agent === 'compact'
    ? withCompactAgentSettings(input.current, input.settings)
    : withExplorerAgentSettings(input.current, input.settings)
  return patchUISettings(next)
}

export async function saveSystemUtilitySettings(input: {
  current: UISettingsWire
  settings: UICompactAgentSettingsWire
}): Promise<UISettingsWire> {
  return patchUISettings(withExplorerAgentSettings(withCompactAgentSettings(input.current, input.settings), input.settings))
}
