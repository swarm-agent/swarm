import { patchUISettings } from '../queries/get-ui-settings'
import { type UISettingsWire } from '../types/swarm-settings'

export async function saveThinkingTagsSetting(enabled: boolean): Promise<UISettingsWire> {
  return patchUISettings({
    chat: {
      thinking_tags: enabled,
    },
  })
}
