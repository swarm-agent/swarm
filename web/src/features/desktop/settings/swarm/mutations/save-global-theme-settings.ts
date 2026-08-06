import { getUISettings, patchUISettings } from '../queries/get-ui-settings'
import type { UISettingsWire } from '../types/swarm-settings'

export async function saveGlobalThemeSettings(themeId: string): Promise<UISettingsWire> {
  const current = await getUISettings()
  const fallbackThemeId = current.theme?.default_theme_id?.trim().toLowerCase() || ''
  const normalizedThemeId = themeId.trim().toLowerCase() || fallbackThemeId
  if (!normalizedThemeId) {
    throw new Error('Canonical default theme is unavailable')
  }
  return patchUISettings({
    theme: {
      active_id: normalizedThemeId,
      custom_themes: current.theme?.custom_themes,
    },
  })
}
