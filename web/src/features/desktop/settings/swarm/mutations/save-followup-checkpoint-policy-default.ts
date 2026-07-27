import { patchUISettings } from '../queries/get-ui-settings'
import { withFollowupCheckpointPolicyDefault } from '../types/swarm-settings'
import type { FollowupCheckpointPolicyDefault, UISettingsWire } from '../types/swarm-settings'

export async function saveFollowupCheckpointPolicyDefault(input: { current: UISettingsWire; policy: FollowupCheckpointPolicyDefault }): Promise<UISettingsWire> {
  return patchUISettings(withFollowupCheckpointPolicyDefault(input.current, input.policy))
}
