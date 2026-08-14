import { patchUISettings } from '../queries/get-ui-settings'
import { type UISettingsWire, withMediaTranscriptionModel } from '../types/swarm-settings'

export async function saveMediaTranscriptionModel(input: { current: UISettingsWire; transcriptionModel: string }): Promise<UISettingsWire> {
  return patchUISettings({ media: withMediaTranscriptionModel(input.current, input.transcriptionModel).media })
}
