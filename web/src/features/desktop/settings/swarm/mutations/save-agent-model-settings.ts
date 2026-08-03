import { requestJson } from '../../../../../app/api'
import { parseAgentModelSettings } from '../queries/get-agent-model-settings'
import type {
  AgentModelAssignment,
  AgentModelSettings,
  SwarmAgentModelSettingsPatch,
  SystemAgentModelSettingsPatch,
} from '../types/agent-model-settings'

function assignmentWire(assignment: AgentModelAssignment, label: string) {
  const provider = assignment.provider.trim().toLowerCase()
  const model = assignment.model.trim()
  const thinking = assignment.thinking.trim()
  if (!provider || !model || !thinking) throw new Error(`${label} provider, model, and thinking are required`)
  return {
    provider,
    model,
    thinking,
    service_tier: assignment.serviceTier.trim().toLowerCase(),
    context_mode: assignment.contextMode.trim().toLowerCase(),
  }
}

async function patchAgentModelSettings(body: unknown): Promise<AgentModelSettings> {
  const response = await requestJson<unknown>('/v1/agent-model-settings', {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
  return parseAgentModelSettings(response)
}

export function saveSwarmAgentModelSettings(input: SwarmAgentModelSettingsPatch): Promise<AgentModelSettings> {
  return patchAgentModelSettings({
    swarm: {
      action: assignmentWire(input.action, 'Action'),
      plan: assignmentWire(input.plan, 'Plan'),
    },
  })
}

export function saveSystemAgentModelSettings(input: SystemAgentModelSettingsPatch): Promise<AgentModelSettings> {
  return patchAgentModelSettings({
    system_agents: {
      [input.agent]: assignmentWire(input.assignment, input.agent),
    },
  })
}
