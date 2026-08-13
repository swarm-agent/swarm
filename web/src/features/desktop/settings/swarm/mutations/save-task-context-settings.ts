import { patchUISettings } from '../queries/get-ui-settings'
import { withTaskContextSettings } from '../types/swarm-settings'
import type { TaskContextSettings, UISettingsWire } from '../types/swarm-settings'

export async function saveTaskContextSettings(
  current: UISettingsWire,
  settings: TaskContextSettings,
): Promise<UISettingsWire> {
  const next = withTaskContextSettings(current, settings)
  return patchUISettings({
    chat: {
      task_context_max_compactions: next.chat?.task_context_max_compactions,
    },
  })
}
