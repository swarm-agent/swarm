import type { QueryClient } from '@tanstack/react-query'
import { requestJson } from '../../../../app/api'
import { agentSettingsStateQueryOptions, agentStateQueryOptions, draftModelQueryKey, draftModelQueryOptions } from '../../../queries/query-options'
import type { AgentProfileRecord, AgentStateRecord } from '../types/chat'
import type { AgentModelControlProfilePatch } from '../components/agent-model-control'

function runtimeModeForAgent(profile: AgentProfileRecord): 'plan_auto' | 'read' | 'readwrite' {
  const raw = (profile.exitPlanModeEnabled ? 'plan_auto' : profile.runtimeMode || profile.executionSetting || '').trim()
  return raw === 'plan_auto' || raw === 'read' || raw === 'readwrite' ? raw : 'readwrite'
}

function agentIsPlanCapable(profile: AgentProfileRecord): boolean {
  if (profile.exitPlanModeEnabled || profile.runtimeMode === 'plan_auto') return true
  if (profile.runtimeMode === 'read' || profile.runtimeMode === 'readwrite' || profile.executionSetting === 'read' || profile.executionSetting === 'readwrite') return false
  const tools = profile.toolContract?.tools ?? {}
  return Boolean(tools.plan_manage?.enabled || tools.exit_plan_mode?.enabled)
}

function agentPayload(profile: AgentProfileRecord, patch: Partial<AgentProfileRecord>): Record<string, unknown> {
  const next = { ...profile, ...patch }
  const runtimeMode = runtimeModeForAgent(next)
  const modelMode = next.modelMode === 'split' && agentIsPlanCapable(next) ? 'split' : 'single'
  return {
    mode: next.mode || 'primary',
    description: next.description || '',
    provider: modelMode === 'split' ? '' : next.provider,
    model: modelMode === 'split' ? '' : next.model,
    thinking: modelMode === 'split' ? '' : next.thinking,
    model_mode: modelMode,
    plan_provider: modelMode === 'split' ? next.planProvider : '',
    plan_model: modelMode === 'split' ? next.planModel : '',
    plan_thinking: modelMode === 'split' ? next.planThinking : '',
    plan_service_tier: modelMode === 'split' ? next.planServiceTier : '',
    auto_provider: modelMode === 'split' ? next.autoProvider : '',
    auto_model: modelMode === 'split' ? next.autoModel : '',
    auto_thinking: modelMode === 'split' ? next.autoThinking : '',
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

export async function refreshAgentModelMutationCaches(queryClient: QueryClient): Promise<AgentStateRecord> {
  const [draftModelResult, agentStateResult, agentSettingsStateResult] = await Promise.all([
    queryClient.fetchQuery({
      ...draftModelQueryOptions(),
      staleTime: 0,
    }),
    queryClient.fetchQuery({
      ...agentStateQueryOptions(),
      staleTime: 0,
    }),
    queryClient.fetchQuery({
      ...agentSettingsStateQueryOptions(),
      staleTime: 0,
    }),
  ])
  queryClient.setQueryData(draftModelQueryKey(), draftModelResult)
  queryClient.setQueryData(agentStateQueryOptions().queryKey, agentStateResult)
  queryClient.setQueryData(agentSettingsStateQueryOptions().queryKey, agentSettingsStateResult)
  await Promise.all([
    queryClient.invalidateQueries({ queryKey: draftModelQueryKey(), refetchType: 'inactive' }),
    queryClient.invalidateQueries({ queryKey: agentStateQueryOptions().queryKey, refetchType: 'inactive' }),
    queryClient.invalidateQueries({ queryKey: agentSettingsStateQueryOptions().queryKey, refetchType: 'inactive' }),
  ])
  return agentSettingsStateResult
}

export async function updateAgentProfile(profile: AgentProfileRecord, patch: AgentModelControlProfilePatch): Promise<void> {
  const normalizedMode = patch.modelMode ?? profile.modelMode
  const nextPatch: AgentModelControlProfilePatch = normalizedMode === 'split'
    ? {
      ...patch,
      provider: '',
      model: '',
      thinking: '',
    }
    : {
      ...patch,
      planProvider: '',
      planModel: '',
      planThinking: '',
      planServiceTier: '',
    }
  await requestJson(
    `/v2/agents/${encodeURIComponent(profile.name.trim())}`,
    {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(agentPayload(profile, nextPatch)),
    },
  )
}
