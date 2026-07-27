import { patchUISettings } from '../queries/get-ui-settings'
import type { DesktopSessionMode, UISettingsWire } from '../types/swarm-settings'
import { withDefaultNewSessionMode } from '../types/swarm-settings'

export async function saveDefaultNewSessionMode(input: { current: UISettingsWire; mode: DesktopSessionMode }): Promise<UISettingsWire> {
  return patchUISettings(withDefaultNewSessionMode(input.current, input.mode))
}
