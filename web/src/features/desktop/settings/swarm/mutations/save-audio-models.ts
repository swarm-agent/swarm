import { patchUISettings } from '../queries/get-ui-settings'
import { type UISettingsWire, withAudioDefaultModel } from '../types/swarm-settings'

export async function saveAudioDefaultModel(input: { current: UISettingsWire; defaultModel: string }): Promise<UISettingsWire> {
  return patchUISettings({ tools: withAudioDefaultModel(input.current, input.defaultModel).tools })
}
