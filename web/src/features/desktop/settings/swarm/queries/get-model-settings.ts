import { requestJson } from '../../../../../app/api'
import type { SwarmModelSettings } from '../types/model-settings'

function parseModelSettings(response: unknown): SwarmModelSettings {
  if (!response || typeof response !== 'object' || Array.isArray(response)) {
    throw new Error('Swarm model settings response is malformed')
  }
  const body = response as Record<string, unknown>
  const raw = body.model_settings
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) {
    throw new Error('Swarm model settings response is missing model_settings')
  }
  const settings = raw as Record<string, unknown>
  if (typeof settings.action_favorite_id !== 'string' || !settings.action_favorite_id.trim()) {
    throw new Error('Swarm model settings response is missing action_favorite_id')
  }
  if (typeof settings.plan_enabled !== 'boolean') {
    throw new Error('Swarm model settings response has invalid plan_enabled')
  }
  if (typeof settings.updated_at !== 'number' || !Number.isFinite(settings.updated_at)) {
    throw new Error('Swarm model settings response has invalid updated_at')
  }
  const planFavoriteId = settings.plan_favorite_id === undefined
    ? ''
    : typeof settings.plan_favorite_id === 'string'
      ? settings.plan_favorite_id.trim()
      : (() => { throw new Error('Swarm model settings response has invalid plan_favorite_id') })()
  if (settings.plan_enabled && !planFavoriteId) {
    throw new Error('Swarm model settings response requires plan_favorite_id when Plan is enabled')
  }
  if (!settings.plan_enabled && planFavoriteId) {
    throw new Error('Swarm model settings response must omit plan_favorite_id when Plan is disabled')
  }
  return {
    actionFavoriteId: settings.action_favorite_id.trim(),
    planEnabled: settings.plan_enabled,
    ...(planFavoriteId ? { planFavoriteId } : {}),
    updatedAt: settings.updated_at,
  }
}

export async function getSwarmModelSettings(signal?: AbortSignal): Promise<SwarmModelSettings> {
  return parseModelSettings(await requestJson<unknown>('/v1/swarm/model-settings', { signal }))
}

export { parseModelSettings }
