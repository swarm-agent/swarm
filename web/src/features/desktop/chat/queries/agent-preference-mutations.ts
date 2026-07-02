import { requestJson } from '../../../../app/api'
import type { AgentProfileRecord } from '../types/chat'
import type { AgentModelControlProfilePatch } from '../components/agent-model-control'

function runtimeModeForAgent(profile: AgentProfileRecord): 'plan_auto' | 'read' | 'readwrite' {
  const raw = (profile.exitPlanModeEnabled ? 'plan_auto' : profile.runtimeMode || profile.executionSetting || '').trim()
  return raw === 'plan_auto' || raw === 'read' || raw === 'readwrite' ? raw : 'readwrite'
}

function agentPayload(profile: AgentProfileRecord, patch: Partial<AgentProfileRecord>): Record<string, unknown> {
  const next = { ...profile, ...patch }
  const modelMode = next.modelMode === 'split' ? 'split' : 'single'
  const runtimeMode = runtimeModeForAgent(next)
  return {
    mode: next.mode || 'primary',
    description: next.description || '',
    provider: modelMode === 'split' ? '' : next.provider,
    model: modelMode === 'split' ? '' : next.model,
    thinking: modelMode === 'split' ? '' : next.thinking,
    model_mode: modelMode,
    plan_provider: next.planProvider,
    plan_model: next.planModel,
    plan_thinking: next.planThinking,
    plan_service_tier: next.planServiceTier,
    auto_provider: next.autoProvider,
    auto_model: next.autoModel,
    auto_thinking: next.autoThinking,
    auto_service_tier: next.autoServiceTier,
    prompt: next.prompt,
    runtime_mode: runtimeMode,
    execution_setting: runtimeMode === 'plan_auto' ? '' : runtimeMode,
    exit_plan_mode_enabled: runtimeMode === 'plan_auto',
    tool_contract: next.toolContract
      ? {
        preset: next.toolContract.preset || undefined,
        inherit_policy: next.toolContract.inheritPolicy,
        tools: next.toolContract.tools,
      }
      : undefined,
    enabled: next.enabled,
  }
}

export async function updateAgentProfile(profile: AgentProfileRecord, patch: AgentModelControlProfilePatch): Promise<void> {
  await requestJson(
    `/v2/agents/${encodeURIComponent(profile.name.trim())}`,
    {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(agentPayload(profile, patch)),
    },
  )
}

export async function switchAgentToSingleModel(profile: AgentProfileRecord): Promise<void> {
  const provider = profile.provider || profile.autoProvider || profile.planProvider
  const model = profile.model || profile.autoModel || profile.planModel
  const thinking = profile.thinking || profile.autoThinking || profile.planThinking || 'off'
  await updateAgentProfile(profile, {
    modelMode: 'single',
    provider,
    model,
    thinking,
  })
}

export async function switchAgentToDefaultModel(profile: AgentProfileRecord): Promise<void> {
  await updateAgentProfile(profile, {
    modelMode: 'single',
    provider: '',
    model: '',
    thinking: '',
  })
}
