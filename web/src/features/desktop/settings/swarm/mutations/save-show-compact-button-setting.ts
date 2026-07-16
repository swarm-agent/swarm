import { patchUISettings } from '../queries/get-ui-settings'
import { type UISettingsWire } from '../types/swarm-settings'

export async function saveShowCompactButtonSetting(enabled: boolean): Promise<UISettingsWire> {
  return patchUISettings({
    chat: {
      show_compact_button: enabled,
    },
  })
}
