import type { QueryClient } from '@tanstack/react-query'
import { requestJson } from '../../../../app/api'
import { modelProfilesQueryKey } from '../../../queries/query-options'
import type {
  ModelProfileInput,
  ModelProfileRecord,
  ModelProfileState,
} from '../types/chat'

function requireRecord(value: unknown, message: string): Record<string, unknown> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error(message)
  return value as Record<string, unknown>
}

function requireString(value: unknown, field: string, allowEmpty = false): string {
  if (typeof value !== 'string') throw new Error(`Model profile response has invalid ${field}`)
  const normalized = value.trim()
  if (!allowEmpty && !normalized) throw new Error(`Model profile response is missing ${field}`)
  return normalized
}

function requireNumber(value: unknown, field: string): number {
  if (typeof value !== 'number' || !Number.isFinite(value)) throw new Error(`Model profile response has invalid ${field}`)
  return value
}

function profileFromWire(value: unknown): ModelProfileRecord {
  const profile = requireRecord(value, 'Model profile response is malformed')
  return {
    profileId: requireString(profile.profile_id, 'profile_id'),
    name: requireString(profile.name, 'name'),
    provider: requireString(profile.provider, 'provider'),
    model: requireString(profile.model, 'model'),
    thinking: requireString(profile.thinking, 'thinking'),
    serviceTier: requireString(profile.service_tier, 'service_tier', true),
    contextMode: requireString(profile.context_mode, 'context_mode', true).toLowerCase(),
    createdAt: requireNumber(profile.created_at, 'created_at'),
    updatedAt: requireNumber(profile.updated_at, 'updated_at'),
    sortOrder: requireNumber(profile.sort_order, 'sort_order'),
    isDefault: typeof profile.is_default === 'boolean'
      ? profile.is_default
      : (() => { throw new Error('Model profile response has invalid is_default') })(),
  }
}

function inputToWire(input: ModelProfileInput) {
  return {
    name: input.name.trim(),
    provider: input.provider.trim(),
    model: input.model.trim(),
    thinking: input.thinking.trim(),
    service_tier: input.serviceTier.trim(),
    context_mode: input.contextMode.trim().toLowerCase(),
  }
}

function responseProfile(response: unknown): ModelProfileRecord {
  const body = requireRecord(response, 'Model profile response is malformed')
  return profileFromWire(body.model_profile)
}

function responseProfiles(response: unknown): ModelProfileRecord[] {
  const body = requireRecord(response, 'Model profiles response is malformed')
  if (!Array.isArray(body.model_profiles)) throw new Error('Model profiles response is missing model_profiles')
  return body.model_profiles.map(profileFromWire)
}

export async function fetchModelProfiles(signal?: AbortSignal): Promise<ModelProfileState> {
  const response = await requestJson<unknown>('/v1/model-profiles', { signal })
  const body = requireRecord(response, 'Model profiles response is malformed')
  const profiles = responseProfiles(body)
  const defaultProfileId = requireString(body.default_profile_id, 'default_profile_id', true)
  if (defaultProfileId && !profiles.some((profile) => profile.profileId === defaultProfileId)) {
    throw new Error('Model profiles response default_profile_id does not identify a returned profile')
  }
  return {
    profiles: profiles.map((profile) => ({ ...profile, isDefault: profile.profileId === defaultProfileId })),
    defaultProfileId,
  }
}

export async function reorderModelProfiles(profileIds: string[]): Promise<ModelProfileRecord[]> {
  const response = await requestJson<unknown>('/v1/model-profiles', {
    method: 'PATCH', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ profile_ids: profileIds }),
  })
  return responseProfiles(response)
}

export async function createModelProfile(input: ModelProfileInput): Promise<ModelProfileRecord> {
  const response = await requestJson<unknown>('/v1/model-profiles', {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(inputToWire(input)),
  })
  return responseProfile(response)
}

export async function updateModelProfile(profileId: string, input: ModelProfileInput): Promise<ModelProfileRecord> {
  const normalized = profileId.trim()
  if (!normalized) throw new Error('Model profile update requires profile_id')
  const response = await requestJson<unknown>(`/v1/model-profiles/${encodeURIComponent(normalized)}`, {
    method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(inputToWire(input)),
  })
  return responseProfile(response)
}

export async function deleteModelProfile(profileId: string): Promise<void> {
  const normalized = profileId.trim()
  if (!normalized) throw new Error('Model profile delete requires profile_id')
  await requestJson(`/v1/model-profiles/${encodeURIComponent(normalized)}`, { method: 'DELETE' })
}

export async function setDefaultModelProfile(profileId: string): Promise<ModelProfileRecord> {
  const normalized = profileId.trim()
  if (!normalized) throw new Error('Default model profile requires profile_id')
  const response = await requestJson<unknown>('/v1/model-profiles/default', {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ profile_id: normalized }),
  })
  return responseProfile(response)
}

export async function invalidateModelProfiles(queryClient: QueryClient): Promise<void> {
  await queryClient.invalidateQueries({ queryKey: modelProfilesQueryKey(), refetchType: 'active' })
}
