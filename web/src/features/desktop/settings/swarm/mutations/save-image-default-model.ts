import { patchUISettings } from '../queries/get-ui-settings'
import { type UISettingsWire, withImageDefaultModel } from '../types/swarm-settings'

export async function saveImageDefaultModel(input: { current: UISettingsWire; defaultModel: string }): Promise<UISettingsWire> {
  return patchUISettings({ tools: withImageDefaultModel(input.current, input.defaultModel).tools })
}
