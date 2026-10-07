import type { AgentModelAssignment, AgentModelSettings } from '../../settings/swarm/types/agent-model-settings'

export type FavoriteScope = 'chat' | 'default' | 'default-and-chat'

// An explicit selection wins over the surrounding Orchestrator context. In
// particular, a deployed Swarm chat must never change Orchestrator's Plan slot.
export function favoriteDefaultSlot(selectedAgent: string, currentAgent = ''): 'action' | 'plan' {
  const agent = (selectedAgent.trim() || currentAgent.trim()).toLowerCase()
  return ['system-orchestrator', 'swarm-orchestrator', 'orchestrator'].includes(agent) ? 'plan' : 'action'
}

export function favoriteDefaultPatch(settings: AgentModelSettings, assignment: AgentModelAssignment, selectedAgent: string, currentAgent = '') {
  const slot = favoriteDefaultSlot(selectedAgent, currentAgent)
  return { ...settings.swarm, [slot]: assignment }
}

export async function applyFavoriteScope(scope: FavoriteScope, saveDefault: () => Promise<void>, applyChat?: () => Promise<void>) {
  if (scope !== 'default' && !applyChat) throw new Error('This chat is unavailable.')
  if (scope !== 'chat') await saveDefault()
  if (scope !== 'default') {
    try {
      await applyChat!()
    } catch (cause) {
      if (scope === 'default-and-chat') {
        throw new Error(`Default saved, but this chat was not changed: ${cause instanceof Error ? cause.message : String(cause)}`)
      }
      throw cause
    }
  }
}
