import type { ModelProfileSelectionRecord } from '../../../chat/types/chat'

export type AgentModelAssignment = ModelProfileSelectionRecord

export type SystemAgentModelName = 'compact' | 'finder' | 'coder' | 'designer' | 'router'

export interface SwarmAgentModelAssignments {
  action: AgentModelAssignment
  plan: AgentModelAssignment
}

export type SystemAgentModelAssignments = Record<SystemAgentModelName, AgentModelAssignment>

export interface AgentModelRole {
  id: string
  label: string
  group: 'swarm' | 'system_agents'
  slot: 'action' | 'plan' | SystemAgentModelName
}

export interface AgentModelSettings {
  roles?: AgentModelRole[]
  swarm: SwarmAgentModelAssignments
  systemAgents: SystemAgentModelAssignments
  updatedAt: number
}

export interface SwarmAgentModelSettingsPatch {
  action: AgentModelAssignment
  plan: AgentModelAssignment
}

export interface SystemAgentModelSettingsPatch {
  agent: SystemAgentModelName
  assignment: AgentModelAssignment
}
