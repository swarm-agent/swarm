import type { AgentStateRecord } from '../types/chat'

export function resolveDesktopV3StartupAgent(agentState: AgentStateRecord, requestedAgent = ''): string {
  const validPrimary = (name: string) => agentState.profiles.find((profile) => profile.name === name && profile.mode === 'primary' && profile.enabled !== false)?.name ?? ''
  const requested = requestedAgent.trim()
  if (requested) return requested
  const activePrimary = agentState.activePrimary.trim()
  if (activePrimary) {
    const validActive = validPrimary(activePrimary)
    if (validActive) return validActive
  }
  return validPrimary('swarm')
}
