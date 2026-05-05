import { patchUISettings } from '../queries/get-ui-settings'
import type { UISettingsWire } from '../types/swarm-settings'
import { withDefaultWorkspaceRoute } from '../types/swarm-settings'

export async function saveDefaultWorkspaceRoute(input: { current: UISettingsWire; workspacePath: string; routeId: string }): Promise<UISettingsWire> {
  return patchUISettings(withDefaultWorkspaceRoute(input.current, input.workspacePath, input.routeId))
}
