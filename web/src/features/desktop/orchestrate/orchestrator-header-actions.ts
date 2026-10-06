import { useMemo } from 'react'
import {
  preferenceFromModelProfile,
  preferenceFromModelProfileMetadata,
} from '../chat/services/model-profiles'
import { modelOptionKey } from '../chat/services/model-options'
import type { ModelOptionRecord, ModelProfileRecord, SessionPreferenceRecord } from '../chat/types/chat'
import {
  sessionV3ModelProfileSettingsMutationResponse,
  updateSessionV3ModelProfile,
  type SessionV3ModelProfileMutationResponseWire,
} from '../session-v3/api'
import { dispatchDesktopV3Cache, useDesktopV3CacheSelector } from '../state/desktop-v3-cache-store'
import type { DesktopV3CacheAction } from '../state/desktop-v3-cache-types'
import type { DesktopSessionMode } from '../settings/swarm/types/swarm-settings'
import { projectConversationLink } from './project-conversations'
import { swarmPageLink } from './swarm-navigation'

export interface ApplySessionModelFavoriteInput {
  sessionId: string
  profile: ModelProfileRecord
  mode: DesktopSessionMode | string
  now?: number
  dispatch?: (action: DesktopV3CacheAction) => void
}

export interface ApplySessionModelFavoriteResult {
  nextPreference: SessionPreferenceRecord
  response: SessionV3ModelProfileMutationResponseWire
}

/**
 * Applies a model favorite to a session, updating backend state and hydrating
 * the canonical Desktop V3 cache store with the resulting mutation.
 */
export async function applySessionModelFavorite({
  sessionId,
  profile,
  mode,
  now = Date.now(),
  dispatch = dispatchDesktopV3Cache,
}: ApplySessionModelFavoriteInput): Promise<ApplySessionModelFavoriteResult | null> {
  const normalizedSessionId = sessionId.trim()
  if (!normalizedSessionId) return null
  const nextPreference = preferenceFromModelProfile(profile, mode, now)
  if (!nextPreference) {
    throw new Error('Model favorite does not resolve for the current chat mode')
  }
  const response = await updateSessionV3ModelProfile(normalizedSessionId, {
    kind: 'temporary',
    profile: {
      name: profile.name,
      provider: profile.provider,
      model: profile.model,
      thinking: profile.thinking,
      serviceTier: profile.serviceTier,
      contextMode: profile.contextMode,
    },
  })
  dispatch({
    type: 'mutation.sessionSettingsResult',
    raw: sessionV3ModelProfileSettingsMutationResponse(response, normalizedSessionId),
  })
  return { nextPreference, response }
}

export interface ResolveCanonicalHeaderModelLabelOptions {
  metadata?: unknown
  mode?: DesktopSessionMode | string
  cachedPreference?: unknown
  modelOptions?: ModelOptionRecord[]
}

/**
 * Resolves the canonical model label for the header presentation directly
 * from session metadata and cached preferences.
 */
export function resolveCanonicalHeaderModelLabel({
  metadata,
  mode = 'auto',
  cachedPreference,
  modelOptions = [],
}: ResolveCanonicalHeaderModelLabelOptions): string {
  const sessionProfilePreference = preferenceFromModelProfileMetadata(
    metadata,
    mode === 'plan' ? 'plan' : 'auto',
  )
  const preference = sessionProfilePreference ?? cachedPreference
  if (!preference || typeof preference !== 'object' || Array.isArray(preference)) return ''
  const selection = 'preference' in preference ? preference.preference : preference
  if (!selection || typeof selection !== 'object' || Array.isArray(selection)) return ''
  const provider = 'provider' in selection && typeof selection.provider === 'string' ? selection.provider.trim() : ''
  const model = 'model' in selection && typeof selection.model === 'string' ? selection.model.trim() : ''
  if (!provider || !model) return ''
  const contextMode = 'contextMode' in selection ? selection.contextMode
    : 'context_mode' in selection ? selection.context_mode : ''
  const key = modelOptionKey(provider, model, typeof contextMode === 'string' ? contextMode : '')
  const option = modelOptions.find((opt) => opt.key === key)
  return option?.label || model
}

/**
 * Hook to reactively observe the canonical model label for a session from the Desktop V3 cache.
 */
export function useCanonicalSessionModelLabel(
  sessionId?: string,
  mode: DesktopSessionMode | string = 'auto',
  modelOptions: ModelOptionRecord[] = [],
): string {
  const metadata = useDesktopV3CacheSelector((state) => {
    if (!sessionId) return null
    const record = state.sessionsById[sessionId]
    return record?.kind === 'full' ? record.session?.metadata : null
  })
  const cachedPreference = useDesktopV3CacheSelector((state) => {
    if (!sessionId) return undefined
    return state.preferencesBySession[sessionId]
  })
  return useMemo(() => {
    return resolveCanonicalHeaderModelLabel({
      metadata,
      mode,
      cachedPreference,
      modelOptions,
    })
  }, [metadata, mode, cachedPreference, modelOptions])
}

export interface ProjectAgentsNavigationOptions {
  projectId?: string
  projectSegment?: string
  sessionId?: string
  primarySessionId?: string
  workspaceSlug?: string
}

export function projectAgentsLink(options: ProjectAgentsNavigationOptions) {
  const targetProject = options.projectSegment || options.projectId
  const targetSession = options.sessionId || options.primarySessionId
  if (targetProject) {
    return {
      ...projectConversationLink(targetProject, targetSession),
      search: { section: 'agents' as const },
    }
  }
  return {
    ...swarmPageLink(options.workspaceSlug, 'agents'),
    search: { section: 'agents' as const },
  }
}

export function navigateToProjectAgents(
  navigate: (target: any) => void | Promise<unknown>,
  options: ProjectAgentsNavigationOptions,
): void {
  void navigate(projectAgentsLink(options))
}
