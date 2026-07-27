import type { ActiveModelProfileState, AgentProfileRecord, ModelProfileRecord } from '../types/chat'

export type ModelProfilePolicyGroup = 'single' | 'split'

export function modelProfilePolicyGroup(value: string | null | undefined): ModelProfilePolicyGroup {
  return value === 'split' ? 'split' : 'single'
}

export function modelProfilePolicyGroupLabel(group: ModelProfilePolicyGroup): string {
  return group === 'split' ? 'Plan + action' : 'Single model'
}

export function modelProfilesInPolicyGroup(profiles: ModelProfileRecord[], group: ModelProfilePolicyGroup): ModelProfileRecord[] {
  return profiles.filter((profile) => profile.modelMode === group)
}

export function canSwitchModelProfilePolicyGroup(agent: AgentProfileRecord | null | undefined): boolean {
  return agent?.name === 'swarm'
}

export function initialModelProfilePolicyGroup(
  agent: AgentProfileRecord | null | undefined,
  activeProfile?: ActiveModelProfileState,
): ModelProfilePolicyGroup {
  if (canSwitchModelProfilePolicyGroup(agent) && activeProfile?.modelMode) {
    return modelProfilePolicyGroup(activeProfile.modelMode)
  }
  return modelProfilePolicyGroup(agent?.modelMode)
}
