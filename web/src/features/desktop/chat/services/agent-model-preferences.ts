import { normalizeSessionMode, type DesktopSessionMode } from '../../settings/swarm/types/swarm-settings'
import type { AgentProfileRecord, ModelOptionRecord, SessionPreferenceRecord } from '../types/chat'

export type AgentModelLockState = {
  profile: AgentProfileRecord | null
  locked: boolean
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
  const preference = profile?.modelMode === 'split'
    ? splitPreferenceForMode(profile, normalizedMode)
    : {
        provider: profile?.provider.trim() ?? '',
        model: profile?.model.trim() ?? '',
        thinking: profile?.thinking.trim() ?? '',
        serviceTier: '',
      }
  const locked = Boolean(preference.provider && preference.model)
  const modeLabel = profile?.modelMode === 'split' ? `${normalizedMode} ` : ''
  return {
    profile,
    locked,
    agentName,
    provider: preference.provider,
    model: preference.model,
    thinking: preference.thinking,
    serviceTier: preference.serviceTier,
    mode: normalizedMode,
    disabledReason: locked && agentName
      ? `To change models for ${agentName}, set the ${modeLabel}model to Default in Settings → Agents.`
      : '',
  }
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
    thinking: lock.thinking || current.thinking || matchingOption?.thinking || '',
    serviceTier: lock.serviceTier,
    contextMode: matchingOption?.contextMode ?? '',
  }
}
