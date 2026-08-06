import { requestJson } from '../../../../../app/api'
import type {
  AgentModelAssignment,
  AgentModelSettings,
  SystemAgentModelName,
} from '../types/agent-model-settings'

export const agentModelSettingsQueryKey = ['agent-model-settings'] as const

function parseAssignment(value: unknown, field: string): AgentModelAssignment {
  if (!value || typeof value !== 'object' || Array.isArray(value)) {
    throw new Error(`Agent model settings response is missing ${field}`)
  }
  const selection = value as Record<string, unknown>
  const provider = typeof selection.provider === 'string' ? selection.provider.trim().toLowerCase() : ''
  const model = typeof selection.model === 'string' ? selection.model.trim() : ''
  const thinking = typeof selection.thinking === 'string' ? selection.thinking.trim() : ''
  if (!provider || !model || !thinking) {
    throw new Error(`Agent model settings response has invalid ${field} assignment`)
  }
  for (const key of ['service_tier', 'context_mode'] as const) {
    if (selection[key] !== undefined && typeof selection[key] !== 'string') {
      throw new Error(`Agent model settings response has invalid ${field}.${key}`)
    }
  }
  return {
    provider,
    model,
    thinking,
    serviceTier: String(selection.service_tier ?? '').trim().toLowerCase(),
    contextMode: String(selection.context_mode ?? '').trim().toLowerCase(),
  }
}

const systemAgentNames: SystemAgentModelName[] = ['compact', 'finder', 'coder', 'designer', 'router']

export function parseAgentModelSettings(response: unknown): AgentModelSettings {
  if (!response || typeof response !== 'object' || Array.isArray(response)) {
    throw new Error('Agent model settings response is malformed')
  }
  const raw = (response as Record<string, unknown>).agent_model_settings
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) {
    throw new Error('Agent model settings response is missing agent_model_settings')
  }
  const settings = raw as Record<string, unknown>
  const swarm = settings.swarm
  const systemAgents = settings.system_agents
  if (!swarm || typeof swarm !== 'object' || Array.isArray(swarm)) {
    throw new Error('Agent model settings response is missing swarm')
  }
  if (!systemAgents || typeof systemAgents !== 'object' || Array.isArray(systemAgents)) {
    throw new Error('Agent model settings response is missing system_agents')
  }
  if (typeof settings.updated_at !== 'number' || !Number.isFinite(settings.updated_at)) {
    throw new Error('Agent model settings response has invalid updated_at')
  }
  const swarmRecord = swarm as Record<string, unknown>
  const systemAgentRecord = systemAgents as Record<string, unknown>
  return {
    swarm: {
      action: parseAssignment(swarmRecord.action, 'swarm.action'),
      plan: parseAssignment(swarmRecord.plan, 'swarm.plan'),
    },
    systemAgents: Object.fromEntries(systemAgentNames.map((name) => [
      name,
      parseAssignment(systemAgentRecord[name], `system_agents.${name}`),
    ])) as AgentModelSettings['systemAgents'],
    updatedAt: settings.updated_at,
  }
}

export async function getAgentModelSettings(signal?: AbortSignal): Promise<AgentModelSettings> {
  return parseAgentModelSettings(await requestJson<unknown>('/v1/agent-model-settings', { signal }))
}

export function agentModelSettingsQueryOptions() {
  return {
    queryKey: agentModelSettingsQueryKey,
    queryFn: ({ signal }: { signal?: AbortSignal }) => getAgentModelSettings(signal),
    staleTime: 30_000,
  }
}
