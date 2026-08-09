import { patchUISettings } from '../queries/get-ui-settings'
import { withPlanContextGuardSettings } from '../types/swarm-settings'
import type { PlanContextGuardSettings, UISettingsWire } from '../types/swarm-settings'

export async function savePlanContextGuardSettings(
  current: UISettingsWire,
  settings: PlanContextGuardSettings,
): Promise<UISettingsWire> {
  const next = withPlanContextGuardSettings(current, settings)
  return patchUISettings({
    chat: {
      plan_context_guard_enabled: next.chat?.plan_context_guard_enabled,
      plan_context_guard_used_percent: next.chat?.plan_context_guard_used_percent,
      plan_context_guard_max_compactions: next.chat?.plan_context_guard_max_compactions,
    },
  })
}
