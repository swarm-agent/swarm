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
  const modeLabel = splitModelActive ? `${normalizedMode} ` : ''
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
    disabledReason: locked && agentName
      ? `To change models for ${agentName}, update the ${modeLabel}model in Settings → Agents.`
      : '',
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
  const matchingOption = modelOptions.find((option) => option.provider === lock.provider && option.model === lock.model) ?? null
  return {
    ...current,
    provider: lock.provider,
    model: lock.model,
    thinking: lock.thinking || current.thinking || defaultThinkingForModelOption(matchingOption),
    serviceTier: lock.serviceTier,
    contextMode: matchingOption?.contextMode ?? '',
  }
}
