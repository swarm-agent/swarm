import { patchUISettings } from '../queries/get-ui-settings'
import { type UISettingsWire } from '../types/swarm-settings'

export async function saveLocalContainerUpdateWarningDismissal(dismissed: boolean): Promise<UISettingsWire> {
  return patchUISettings({
    updates: {
      local_container_warning_dismissed: dismissed,
    },
  })
}
