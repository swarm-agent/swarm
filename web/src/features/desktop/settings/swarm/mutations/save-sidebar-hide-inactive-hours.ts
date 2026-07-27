import { patchUISettings } from '../queries/get-ui-settings'
import { type UISettingsWire, withSidebarHideInactiveHours } from '../types/swarm-settings'

export async function saveSidebarHideInactiveHours(input: { current: UISettingsWire; hours: number | null }): Promise<UISettingsWire> {
  return patchUISettings(withSidebarHideInactiveHours(input.current, input.hours))
}
