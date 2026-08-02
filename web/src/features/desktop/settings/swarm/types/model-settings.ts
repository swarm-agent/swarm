import type { ModelProfileSelectionRecord } from '../../../chat/types/chat'

export type SwarmModelSelection = ModelProfileSelectionRecord

export interface SwarmModelSettings {
  action: SwarmModelSelection
  plan: SwarmModelSelection
  updatedAt: number
}

export interface SwarmModelSettingsInput {
  action: SwarmModelSelection
  plan: SwarmModelSelection
}
