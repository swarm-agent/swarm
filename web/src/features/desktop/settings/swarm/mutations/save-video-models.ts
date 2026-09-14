import { patchUISettings } from '../queries/get-ui-settings'
import { type UISettingsWire, withVideoDefaultModel, withVideoIterationModel } from '../types/swarm-settings'

export async function saveVideoDefaultModel(input: { current: UISettingsWire; defaultModel: string }): Promise<UISettingsWire> {
  return patchUISettings({ tools: withVideoDefaultModel(input.current, input.defaultModel).tools })
}

export async function saveVideoIterationModel(input: { current: UISettingsWire; iterationModel: string }): Promise<UISettingsWire> {
  return patchUISettings({ tools: withVideoIterationModel(input.current, input.iterationModel).tools })
}
