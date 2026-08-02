import { requestJson } from '../../../../../app/api'
import type { SwarmModelSelection, SwarmModelSettings } from '../types/model-settings'

function parseSelection(value: unknown, field: 'action' | 'plan'): SwarmModelSelection {
  if (!value || typeof value !== 'object' || Array.isArray(value)) {
    throw new Error(`Swarm model settings response is missing ${field}`)
  }
  const selection = value as Record<string, unknown>
  const provider = typeof selection.provider === 'string' ? selection.provider.trim() : ''
  const model = typeof selection.model === 'string' ? selection.model.trim() : ''
  const thinking = typeof selection.thinking === 'string' ? selection.thinking.trim() : ''
  if (!provider || !model || !thinking) {
    throw new Error(`Swarm model settings response has invalid ${field} selection`)
  }
  for (const key of ['service_tier', 'context_mode'] as const) {
    if (selection[key] !== undefined && typeof selection[key] !== 'string') {
      throw new Error(`Swarm model settings response has invalid ${field}.${key}`)
    }
  }
  return {
    provider,
    model,
    thinking,
    serviceTier: String(selection.service_tier ?? '').trim(),
    contextMode: String(selection.context_mode ?? '').trim().toLowerCase(),
  }
}

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
  if (typeof settings.updated_at !== 'number' || !Number.isFinite(settings.updated_at)) {
    throw new Error('Swarm model settings response has invalid updated_at')
  }
  return {
    action: parseSelection(settings.action, 'action'),
    plan: parseSelection(settings.plan, 'plan'),
    updatedAt: settings.updated_at,
  }
}

export async function getSwarmModelSettings(signal?: AbortSignal): Promise<SwarmModelSettings> {
  return parseModelSettings(await requestJson<unknown>('/v1/swarm/model-settings', { signal }))
}

export { parseModelSettings }
