import { patchUISettings } from '../queries/get-ui-settings'
import {
  type UICompactAgentSettingsWire,
  type UISettingsWire,
  withCoderAgentSettings,
  withCompactAgentSettings,
  withDesignerAgentSettings,
  withExplorerAgentSettings,
} from '../types/swarm-settings'

export async function saveSystemAgentSettings(input: {
  current: UISettingsWire
  agent: 'compact' | 'explorer' | 'coder' | 'designer'
  settings: UICompactAgentSettingsWire
}): Promise<UISettingsWire> {
  const next = input.agent === 'compact'
    ? withCompactAgentSettings(input.current, input.settings)
    : input.agent === 'explorer'
      ? withExplorerAgentSettings(input.current, input.settings)
      : input.agent === 'coder'
        ? withCoderAgentSettings(input.current, input.settings)
        : withDesignerAgentSettings(input.current, input.settings)
  return patchUISettings(next)
}

export async function saveSystemUtilitySettings(input: {
  current: UISettingsWire
  settings: UICompactAgentSettingsWire
}): Promise<UISettingsWire> {
  const withCompact = withCompactAgentSettings(input.current, input.settings)
  const withExplorer = withExplorerAgentSettings(withCompact, input.settings)
  return patchUISettings(withDesignerAgentSettings(withExplorer, input.settings))
}
