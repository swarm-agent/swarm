import type { AgentStateRecord } from '../types/chat'

export function resolveDesktopV3StartupAgent(agentState: AgentStateRecord, requestedAgent = ''): string {
  const requested = requestedAgent.trim()
  if (requested) return requested
  const swarm = agentState.profiles.find((profile) => profile.name === 'swarm' && profile.mode === 'primary' && profile.enabled !== false)
  if (swarm) return swarm.name
  const activePrimary = agentState.activePrimary.trim()
  if (activePrimary && agentState.profiles.some((profile) => profile.name === activePrimary && profile.mode === 'primary' && profile.enabled !== false)) {
    return activePrimary
  }
  return ''
}
