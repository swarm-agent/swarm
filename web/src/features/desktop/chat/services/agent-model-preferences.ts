import { normalizeSessionMode, type DesktopSessionMode } from '../../settings/swarm/types/swarm-settings'
import type { AgentProfileRecord, ModelOptionRecord, SessionPreferenceRecord } from '../types/chat'
import { defaultModelThinking } from './model-options'

export type AgentModelLockState = {
  profile: AgentProfileRecord | null
  locked: boolean
  customized: boolean
  agentName: string
  provider: string
  model: string
  thinking: string
  serviceTier: string
  mode: DesktopSessionMode
  disabledReason: string
}

export function findAgentProfile(agents: AgentProfileRecord[], agentName: string): AgentProfileRecord | null {
  const normalizedAgentName = agentName.trim()
  if (!normalizedAgentName) return null
  return agents.find((agent) => agent.name === normalizedAgentName)
    ?? agents.find((agent) => agent.name.trim().toLowerCase() === normalizedAgentName.toLowerCase())
    ?? null
}

function agentIsPlanCapable(profile: AgentProfileRecord | null): boolean {
  if (!profile) return false
  if (profile.exitPlanModeEnabled || profile.runtimeMode === 'plan_auto') return true
  if (profile.runtimeMode === 'read' || profile.runtimeMode === 'readwrite' || profile.executionSetting === 'read' || profile.executionSetting === 'readwrite') return false
  const tools = profile.toolContract?.tools ?? {}
  return Boolean(tools.plan_manage?.enabled || tools.exit_plan_mode?.enabled)
}

function splitPreferenceForMode(profile: AgentProfileRecord, mode: DesktopSessionMode) {
  if (mode === 'plan') {
    return {
      provider: profile.planProvider.trim(),
      model: profile.planModel.trim(),
      thinking: profile.planThinking.trim(),
      serviceTier: profile.planServiceTier.trim(),
    }
  }
  return {
    provider: profile.autoProvider.trim(),
    model: profile.autoModel.trim(),
    thinking: profile.autoThinking.trim(),
    serviceTier: profile.autoServiceTier.trim(),
  }
}

function record(value: unknown): Record<string, unknown> {
  return value && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : {}
}

function storedAgentSupportsPlan(profile: Record<string, unknown>): boolean {
  const runtimeMode = String(profile.runtime_mode ?? '').trim().toLowerCase().replace(/-/g, '_')
  if (runtimeMode === 'plan_auto' || profile.exit_plan_mode_enabled === true) return true
  if (runtimeMode === 'read' || runtimeMode === 'readwrite' || runtimeMode === 'read_write') return false
  const tools = record(record(profile.tool_contract).tools)
  return record(tools.plan_manage).enabled === true || record(tools.exit_plan_mode).enabled === true
}

export function resolveDesktopV3SessionAgentModelLock(
  metadata: unknown,
  mode: DesktopSessionMode = 'auto',
): AgentModelLockState | null {
  const profile = record(record(metadata).agent_profile)
  const agentName = String(profile.name ?? '').trim()
  if (!agentName) return null
  const normalizedMode = normalizeSessionMode(mode)
  const splitModelActive = String(profile.model_mode ?? '').trim().toLowerCase() === 'split'
    && storedAgentSupportsPlan(profile)
  const provider = String(splitModelActive
    ? normalizedMode === 'plan' ? profile.plan_provider : profile.auto_provider
    : profile.provider ?? '').trim()
  const model = String(splitModelActive
    ? normalizedMode === 'plan' ? profile.plan_model : profile.auto_model
    : profile.model ?? '').trim()
  if (!provider || !model) return null
  const thinking = String(splitModelActive
    ? normalizedMode === 'plan' ? profile.plan_thinking : profile.auto_thinking
    : profile.thinking ?? '').trim()
  const serviceTier = String(splitModelActive
    ? normalizedMode === 'plan' ? profile.plan_service_tier : profile.auto_service_tier
    : profile.auto_service_tier ?? '').trim()
  return {
    profile: null,
    locked: true,
    customized: true,
    agentName,
    provider,
    model,
    thinking,
    serviceTier,
    mode: normalizedMode,
    disabledReason: '',
  }
}

export function resolveDesktopV3AgentModelLock(
  agents: AgentProfileRecord[],
  selectedAgentName: string,
  mode: DesktopSessionMode = 'auto',
): AgentModelLockState {
  const normalizedMode = normalizeSessionMode(mode)
  const profile = findAgentProfile(agents, selectedAgentName)
  const agentName = profile?.name.trim() || selectedAgentName.trim()
  const splitModelActive = profile?.modelMode === 'split' && agentIsPlanCapable(profile)
  const preference = splitModelActive
    ? splitPreferenceForMode(profile, normalizedMode)
    : {
        provider: profile?.provider.trim() ?? '',
        model: profile?.model.trim() ?? '',
        thinking: profile?.thinking.trim() ?? '',
        serviceTier: profile?.autoServiceTier.trim() ?? '',
      }
  const locked = Boolean(preference.provider && preference.model)
  return {
    profile,
    locked,
    customized: Boolean(profile && (splitModelActive || profile.provider.trim() || profile.model.trim())),
    agentName,
    provider: preference.provider,
    model: preference.model,
    thinking: preference.thinking,
    serviceTier: preference.serviceTier,
    mode: normalizedMode,
    disabledReason: '',
  }
}

function defaultThinkingForModelOption(option: ModelOptionRecord | null): string {
  return defaultModelThinking(option)
}

export function preferenceFromAgentModelLock(
  lock: AgentModelLockState,
  current: SessionPreferenceRecord,
  modelOptions: ModelOptionRecord[],
): SessionPreferenceRecord {
  if (!lock.locked) return current
  const matchingOptions = modelOptions.filter((option) => option.provider === lock.provider && option.model === lock.model)
  const matchingOption = matchingOptions.find((option) => option.contextMode === current.contextMode) ?? matchingOptions.find((option) => option.contextMode === '') ?? matchingOptions[0] ?? null
  return {
    ...current,
    provider: lock.provider,
    model: lock.model,
    thinking: lock.thinking || current.thinking || defaultThinkingForModelOption(matchingOption),
    serviceTier: lock.serviceTier,
    contextMode: matchingOption?.contextMode ?? '',
  }
}
