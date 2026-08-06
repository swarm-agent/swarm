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
  disabledReason: string
}

export function findAgentProfile(agents: AgentProfileRecord[], agentName: string): AgentProfileRecord | null {
  const normalizedAgentName = agentName.trim()
  if (!normalizedAgentName) return null
  return agents.find((agent) => agent.name === normalizedAgentName)
    ?? agents.find((agent) => agent.name.trim().toLowerCase() === normalizedAgentName.toLowerCase())
    ?? null
}

function record(value: unknown): Record<string, unknown> {
  return value && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : {}
}

export function resolveDesktopV3SessionAgentModelLock(metadata: unknown): AgentModelLockState | null {
  const profile = record(record(metadata).agent_profile)
  const agentName = String(profile.name ?? '').trim()
  if (!agentName || agentName.toLowerCase() === 'swarm') return null
  const provider = String(profile.provider ?? '').trim()
  const model = String(profile.model ?? '').trim()
  if (!provider || !model) return null
  const thinking = String(profile.thinking ?? '').trim()
  const serviceTier = String(profile.service_tier ?? '').trim()
  return {
    profile: null,
    locked: true,
    customized: true,
    agentName,
    provider,
    model,
    thinking,
    serviceTier,
    disabledReason: '',
  }
}

export function resolveDesktopV3AgentModelLock(
  agents: AgentProfileRecord[],
  selectedAgentName: string,
): AgentModelLockState {
  const profile = findAgentProfile(agents, selectedAgentName)
  const agentName = profile?.name.trim() || selectedAgentName.trim()
  if (agentName.toLowerCase() === 'swarm') {
    return { profile, locked: false, customized: false, agentName, provider: '', model: '', thinking: '', serviceTier: '', disabledReason: '' }
  }
  const preference = {
    provider: profile?.provider.trim() ?? '',
    model: profile?.model.trim() ?? '',
    thinking: profile?.thinking.trim() ?? '',
    serviceTier: '',
  }
  const locked = Boolean(preference.provider && preference.model)
  return {
    profile,
    locked,
    customized: Boolean(profile && (profile.provider.trim() || profile.model.trim())),
    agentName,
    provider: preference.provider,
    model: preference.model,
    thinking: preference.thinking,
    serviceTier: preference.serviceTier,
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
