import { requestJson } from '../../../../../app/api'
import { parseModelSettings } from '../queries/get-model-settings'
import type { SwarmModelSettings, SwarmModelSettingsInput } from '../types/model-settings'

export async function saveSwarmModelSettings(input: SwarmModelSettingsInput): Promise<SwarmModelSettings> {
  const actionFavoriteId = input.actionFavoriteId.trim()
  const planFavoriteId = input.planFavoriteId?.trim() ?? ''
  if (!actionFavoriteId) throw new Error('Action favorite is required')
  if (input.planEnabled && !planFavoriteId) throw new Error('Plan favorite is required when Plan is enabled')
  if (!input.planEnabled && planFavoriteId) throw new Error('Plan favorite must be omitted when Plan is disabled')

  const response = await requestJson<unknown>('/v1/swarm/model-settings', {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      action_favorite_id: actionFavoriteId,
      plan_enabled: input.planEnabled,
      ...(input.planEnabled ? { plan_favorite_id: planFavoriteId } : {}),
    }),
  })
  return parseModelSettings(response)
}
