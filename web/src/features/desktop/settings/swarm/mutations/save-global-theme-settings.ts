import { getUISettings, patchUISettings } from '../queries/get-ui-settings'
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

  return patchUISettings({
    theme: payload.theme,
  })
}
