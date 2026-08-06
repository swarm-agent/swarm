import { patchUISettings } from '../queries/get-ui-settings'
import { type UISettingsWire } from '../types/swarm-settings'

export async function saveShowTipsSetting(enabled: boolean): Promise<UISettingsWire> {
  return patchUISettings({
    chat: {
      show_tips: enabled,
    },
  })
}
