import type { QueryClient } from '@tanstack/react-query'
import { requestJson } from '../../../../app/api'
import { modelProfilesQueryKey } from '../../../queries/query-options'
import type {
  ModelProfileInput,
  ModelProfileRecord,
  ModelProfileSelectionRecord,
  ModelProfileState,
} from '../types/chat'

type SelectionWire = {
  provider?: unknown
  model?: unknown
  thinking?: unknown
  service_tier?: unknown
  context_mode?: unknown
}

type ProfileWire = {
  profile_id?: unknown
  name?: unknown
  model_mode?: unknown
  single?: SelectionWire | null
  plan?: SelectionWire | null
  auto?: SelectionWire | null
  created_at?: unknown
  updated_at?: unknown
  is_default?: unknown
}

function selectionFromWire(value: SelectionWire | null | undefined): ModelProfileSelectionRecord | null {
  if (!value) return null
  return {
    provider: String(value.provider ?? '').trim(),
    model: String(value.model ?? '').trim(),
    thinking: String(value.thinking ?? '').trim(),
    serviceTier: String(value.service_tier ?? '').trim(),
    contextMode: String(value.context_mode ?? '').trim(),
  }
}

function profileFromWire(value: ProfileWire): ModelProfileRecord {
  const modelMode = String(value.model_mode ?? '').trim()
  if (modelMode !== 'single' && modelMode !== 'split') throw new Error('Model profile response has an invalid model_mode')
  const profileId = String(value.profile_id ?? '').trim()
  if (!profileId) throw new Error('Model profile response is missing profile_id')
  return {
    profileId,
    name: String(value.name ?? '').trim(),
    modelMode,
    single: selectionFromWire(value.single),
    plan: selectionFromWire(value.plan),
    auto: selectionFromWire(value.auto),
    createdAt: typeof value.created_at === 'number' ? value.created_at : 0,
    updatedAt: typeof value.updated_at === 'number' ? value.updated_at : 0,
    isDefault: value.is_default === true,
  }
}

function selectionToWire(value: ModelProfileSelectionRecord | null) {
  return value ? {
    provider: value.provider.trim(),
    model: value.model.trim(),
    thinking: value.thinking.trim(),
    service_tier: value.serviceTier.trim(),
    context_mode: value.contextMode.trim(),
  } : undefined
}

function inputToWire(input: ModelProfileInput) {
  return {
    name: input.name.trim(),
    model_mode: input.modelMode,
    single: selectionToWire(input.single),
    plan: selectionToWire(input.plan),
    auto: selectionToWire(input.auto),
  }
}

export async function fetchModelProfiles(signal?: AbortSignal): Promise<ModelProfileState> {
  const response = await requestJson<{ model_profiles?: ProfileWire[]; default_profile_id?: unknown }>('/v1/model-profiles', { signal })
  const profiles = Array.isArray(response.model_profiles) ? response.model_profiles.map(profileFromWire) : []
  const defaultProfileId = String(response.default_profile_id ?? '').trim()
  return {
    profiles: profiles.map((profile) => ({ ...profile, isDefault: profile.profileId === defaultProfileId || profile.isDefault })),
    defaultProfileId,
  }
}

export async function createModelProfile(input: ModelProfileInput): Promise<ModelProfileRecord> {
  const response = await requestJson<{ model_profile: ProfileWire }>('/v1/model-profiles', {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(inputToWire(input)),
  })
  return profileFromWire(response.model_profile)
}

export async function updateModelProfile(profileId: string, input: ModelProfileInput): Promise<ModelProfileRecord> {
  const normalized = profileId.trim()
  if (!normalized) throw new Error('Model profile update requires profile_id')
  const response = await requestJson<{ model_profile: ProfileWire }>(`/v1/model-profiles/${encodeURIComponent(normalized)}`, {
    method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(inputToWire(input)),
  })
  return profileFromWire(response.model_profile)
}

export async function deleteModelProfile(profileId: string): Promise<void> {
  const normalized = profileId.trim()
  if (!normalized) throw new Error('Model profile delete requires profile_id')
  await requestJson(`/v1/model-profiles/${encodeURIComponent(normalized)}`, { method: 'DELETE' })
}

export async function setDefaultModelProfile(profileId: string): Promise<ModelProfileRecord> {
  const normalized = profileId.trim()
  if (!normalized) throw new Error('Default model profile requires profile_id')
  const response = await requestJson<{ model_profile: ProfileWire }>('/v1/model-profiles/default', {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ profile_id: normalized }),
  })
  return profileFromWire(response.model_profile)
}

export async function invalidateModelProfiles(queryClient: QueryClient): Promise<void> {
  await queryClient.invalidateQueries({ queryKey: modelProfilesQueryKey(), refetchType: 'active' })
}
