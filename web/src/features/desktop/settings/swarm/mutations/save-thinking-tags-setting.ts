import { requestJson } from '../../../../../app/api'
import { getUISettings } from '../queries/get-ui-settings'
import { type UISettingsWire, withThinkingTagsEnabled } from '../types/swarm-settings'

export async function saveThinkingTagsSetting(enabled: boolean): Promise<UISettingsWire> {
  const current = await getUISettings()
  const payload = withThinkingTagsEnabled(current, enabled)

  return requestJson<UISettingsWire>('/v1/ui/settings', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify(payload),
  })
}
