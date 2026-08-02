import { requestJson } from '../../../../../app/api'
import { parseModelSettings } from '../queries/get-model-settings'
import type { SwarmModelSelection, SwarmModelSettings, SwarmModelSettingsInput } from '../types/model-settings'

function selectionWire(selection: SwarmModelSelection, label: 'Action' | 'Plan') {
  const provider = selection.provider.trim()
  const model = selection.model.trim()
  const thinking = selection.thinking.trim()
  if (!provider || !model || !thinking) throw new Error(`${label} provider, model, and thinking are required`)
  return {
    provider,
    model,
    thinking,
    service_tier: selection.serviceTier.trim(),
    context_mode: selection.contextMode.trim().toLowerCase(),
  }
}

export async function saveSwarmModelSettings(input: SwarmModelSettingsInput): Promise<SwarmModelSettings> {
  const response = await requestJson<unknown>('/v1/swarm/model-settings', {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      action: selectionWire(input.action, 'Action'),
      plan: selectionWire(input.plan, 'Plan'),
    }),
  })
  return parseModelSettings(response)
}
