import { requestJson } from '../../../../../app/api'
import { getUISettings } from '../queries/get-ui-settings'
import { type UISettingsWire, withLocalContainerUpdateWarningDismissed } from '../types/swarm-settings'

export async function saveLocalContainerUpdateWarningDismissal(dismissed: boolean): Promise<UISettingsWire> {
  const current = await getUISettings()
  const payload = withLocalContainerUpdateWarningDismissed(current, dismissed)

  return requestJson<UISettingsWire>('/v1/ui/settings', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify(payload),
  })
}
