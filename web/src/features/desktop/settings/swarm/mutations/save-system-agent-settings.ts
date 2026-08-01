import { patchUISettings } from '../queries/get-ui-settings'
import {
  type UICompactAgentSettingsWire,
  type UISettingsWire,
  withCoderAgentSettings,
  withCompactAgentSettings,
  withDesignerAgentSettings,
  withFinderAgentSettings,
  withRouterAgentSettings,
} from '../types/swarm-settings'

export async function saveSystemAgentSettings(input: {
  current: UISettingsWire
  agent: 'compact' | 'finder' | 'coder' | 'designer' | 'router'
  settings: UICompactAgentSettingsWire
}): Promise<UISettingsWire> {
  const next = input.agent === 'compact'
    ? withCompactAgentSettings(input.current, input.settings)
    : input.agent === 'finder'
      ? withFinderAgentSettings(input.current, input.settings)
      : input.agent === 'coder'
        ? withCoderAgentSettings(input.current, input.settings)
        : input.agent === 'designer'
          ? withDesignerAgentSettings(input.current, input.settings)
          : withRouterAgentSettings(input.current, input.settings)
  return patchUISettings(next)
}

export async function saveSystemUtilitySettings(input: {
  current: UISettingsWire
  settings: UICompactAgentSettingsWire
}): Promise<UISettingsWire> {
  const withCompact = withCompactAgentSettings(input.current, input.settings)
  const withFinder = withFinderAgentSettings(withCompact, input.settings)
  return patchUISettings(withDesignerAgentSettings(withFinder, input.settings))
}
