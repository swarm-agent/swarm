import { requestJson } from '../../../../../app/api'
import { getUISettings } from '../queries/get-ui-settings'
import { type UISettingsWire } from '../types/swarm-settings'

export async function saveGlobalThemeSettings(themeId: string): Promise<UISettingsWire> {
  const current = await getUISettings()
  const normalizedThemeId = themeId.trim().toLowerCase() || 'crimson'
  const payload: UISettingsWire = {
    ...current,
    theme: {
      ...(current.theme ?? {}),
      active_id: normalizedThemeId,
      custom_themes: current.theme?.custom_themes,
    },
  }

  const response = await requestJson<UISettingsWire>('/v1/ui/settings', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify(payload),
  })

  return response
}
