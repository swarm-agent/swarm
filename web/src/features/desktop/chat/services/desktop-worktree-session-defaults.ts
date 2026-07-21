import type { DesktopSessionMode } from '../../settings/swarm/types/swarm-settings'
import type {
  AgentStateRecord,
  ModelOptionRecord,
  ModelProfileChoice,
  ModelProfileState,
  ResolvedSessionPreference,
  SessionPreferenceRecord,
} from '../types/chat'
import { preferenceFromAgentModelLock, resolveDesktopV3AgentModelLock } from './agent-model-preferences'
import { resolveDesktopV3StartupAgent } from './desktop-startup-agent'
import { preferenceFromModelProfile } from './model-profiles'

export interface DesktopWorktreeSessionDefaults {
  agentName: string
  mode: DesktopSessionMode
  preference: SessionPreferenceRecord
  modelProfileChoice?: ModelProfileChoice
}

function requireResolvedPreference(preference: SessionPreferenceRecord | null | undefined, authority: string): SessionPreferenceRecord {
  if (!preference?.provider.trim() || !preference.model.trim() || !preference.thinking.trim()) {
    throw new Error(`${authority} did not resolve a provider, model, and thinking level.`)
  }
  return preference
}

function preferenceFromDraft(draft: ResolvedSessionPreference | undefined): SessionPreferenceRecord {
  return requireResolvedPreference(draft?.preference, 'Desktop draft model settings')
}

export function resolveDesktopWorktreeSessionDefaults(input: {
  agentState: AgentStateRecord
  modelProfiles: ModelProfileState
  modelOptions: ModelOptionRecord[]
  draftPreference?: ResolvedSessionPreference
  explicitMode?: DesktopSessionMode
  globalDefaultMode: DesktopSessionMode
}): DesktopWorktreeSessionDefaults {
  const agentName = resolveDesktopV3StartupAgent(input.agentState)
  if (!agentName) {
    throw new Error('Desktop agent settings did not resolve an enabled primary agent.')
  }

  const agentProfile = input.agentState.profiles.find((profile) => profile.name === agentName) ?? null
  if (!agentProfile) {
    throw new Error(`Desktop agent settings did not return the resolved primary agent ${JSON.stringify(agentName)}.`)
  }
  const mode = input.explicitMode ?? agentProfile.defaultSessionMode ?? input.globalDefaultMode
  const agentModelLock = resolveDesktopV3AgentModelLock(input.agentState.profiles, agentName, mode)

  if (agentName.toLowerCase() !== 'swarm' && agentModelLock.locked) {
    return {
      agentName,
      mode,
      preference: requireResolvedPreference(
        preferenceFromAgentModelLock(agentModelLock, {
          provider: '', model: '', thinking: '', serviceTier: '', contextMode: '', updatedAt: 0,
        }, input.modelOptions),
        `Agent ${JSON.stringify(agentName)} model settings`,
      ),
      modelProfileChoice: { kind: 'agent-default' },
    }
  }

  if (agentName.toLowerCase() === 'swarm' && input.modelProfiles.defaultProfileId) {
    const accountDefault = input.modelProfiles.profiles.find(
      (profile) => profile.profileId === input.modelProfiles.defaultProfileId,
    )
    if (!accountDefault) {
      throw new Error('Desktop model-profile settings did not return the configured account-default profile.')
    }
    return {
      agentName,
      mode,
      preference: requireResolvedPreference(
        preferenceFromModelProfile(accountDefault, mode, accountDefault.updatedAt),
        'The account-default model profile',
      ),
      modelProfileChoice: { kind: 'account-default' },
    }
  }

  return {
    agentName,
    mode,
    preference: preferenceFromDraft(input.draftPreference),
  }
}
