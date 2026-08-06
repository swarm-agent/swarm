import type {
  ActiveModelProfileState,
  ModelOptionRecord,
  ModelProfileChoice,
  ModelProfileInput,
  ModelProfileRecord,
  ModelProfileSelectionRecord,
  SessionPreferenceRecord,
} from '../types/chat'
import { defaultModelThinking, modelOptionKey, normalizeModelServiceTier } from './model-options'

export type ModelProfileDraft = ModelProfileInput

const EMPTY_SELECTION: ModelProfileSelectionRecord = {
  provider: '', model: '', thinking: '', serviceTier: '', contextMode: '',
}

export function emptyModelProfileDraft(name = ''): ModelProfileDraft {
  return { name, ...EMPTY_SELECTION }
}

export function selectionFromModelOption(
  option: ModelOptionRecord | null | undefined,
  input: Partial<ModelProfileSelectionRecord> = {},
): ModelProfileSelectionRecord {
  const provider = input.provider?.trim() || option?.provider.trim() || ''
  return {
    provider,
    model: input.model?.trim() || option?.model.trim() || '',
    thinking: input.thinking?.trim() || defaultModelThinking(option),
    serviceTier: normalizeModelServiceTier(provider, input.serviceTier ?? option?.defaultServiceTier ?? ''),
    contextMode: input.contextMode?.trim().toLowerCase() || option?.contextMode.trim().toLowerCase() || '',
  }
}

export function modelOptionForSelection(
  selection: Pick<ModelProfileSelectionRecord, 'provider' | 'model' | 'contextMode'>,
  options: ModelOptionRecord[],
): ModelOptionRecord | null {
  const key = modelOptionKey(selection.provider, selection.model, selection.contextMode)
  return options.find((option) => option.key === key) ?? null
}

export function modelProfileDraftFromRecord(profile: ModelProfileRecord): ModelProfileDraft {
  return {
    name: profile.name,
    provider: profile.provider,
    model: profile.model,
    thinking: profile.thinking,
    serviceTier: profile.serviceTier,
    contextMode: profile.contextMode,
  }
}

function normalizedSelection(selection: ModelProfileSelectionRecord): ModelProfileSelectionRecord {
  return {
    provider: selection.provider.trim(),
    model: selection.model.trim(),
    thinking: selection.thinking.trim(),
    serviceTier: normalizeModelServiceTier(selection.provider, selection.serviceTier),
    contextMode: selection.contextMode.trim().toLowerCase(),
  }
}

export function modelProfileInputFromDraft(draft: ModelProfileDraft): ModelProfileInput {
  return { name: draft.name.trim(), ...normalizedSelection(draft) }
}

export function preferenceFromModelProfile(
  profile: ModelProfileInput,
  _mode?: unknown,
  updatedAt = 0,
): SessionPreferenceRecord {
  return { ...normalizedSelection(profile), updatedAt }
}

export function modelProfileChoiceForSaved(profileId: string, isAccountDefault = false): ModelProfileChoice {
  return isAccountDefault ? { kind: 'account-default' } : { kind: 'saved', profileId: profileId.trim() }
}

export function modelProfileChoiceForTemporary(draft: ModelProfileDraft): ModelProfileChoice {
  return { kind: 'temporary', profile: modelProfileInputFromDraft(draft) }
}

function record(value: unknown): Record<string, unknown> {
  return value && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : {}
}

function selectionFromMetadata(value: unknown): ModelProfileSelectionRecord | null {
  const selection = record(value)
  const provider = String(selection.provider ?? '').trim()
  const model = String(selection.model ?? '').trim()
  if (!provider || !model) return null
  return {
    provider,
    model,
    thinking: String(selection.thinking ?? '').trim(),
    serviceTier: String(selection.service_tier ?? '').trim(),
    contextMode: String(selection.context_mode ?? '').trim().toLowerCase(),
  }
}

/** Reads the immutable Action/optional Plan session snapshot, not the favorite CRUD wire. */
export function modelProfileFromMetadata(metadata: unknown, mode: 'plan' | 'auto' = 'auto'): ModelProfileInput | null {
  const snapshot = record(record(metadata).model_profile)
  const selected = mode === 'plan' ? selectionFromMetadata(snapshot.plan) : selectionFromMetadata(snapshot.action)
  if (!selected) return null
  const name = String(mode === 'plan' ? snapshot.plan_favorite_name : snapshot.action_favorite_name).trim()
  return { name, ...selected }
}

export function preferenceFromModelProfileMetadata(
  metadata: unknown,
  mode: 'plan' | 'auto',
): SessionPreferenceRecord | null {
  const profile = modelProfileFromMetadata(metadata, mode)
  if (!profile) return null
  const snapshot = record(record(metadata).model_profile)
  const appliedAt = typeof snapshot.applied_at === 'number' ? snapshot.applied_at : 0
  return preferenceFromModelProfile(profile, mode, appliedAt)
}

export function activeModelProfileFromMetadata(metadata: unknown): ActiveModelProfileState {
  const profile = record(record(metadata).model_profile)
  const source = String(profile.source ?? '').trim()
  const profileId = String(profile.action_favorite_id ?? profile.plan_favorite_id ?? '').trim()
  const name = String(profile.action_favorite_name ?? profile.plan_favorite_name ?? '').trim()
  return {
    source: source === 'saved' ? 'saved' : source === 'temporary' ? 'temporary' : '',
    profileId,
    name,
  }
}

export function activeModelProfileFromPolicy(value: unknown): ActiveModelProfileState {
  const policy = record(value)
  const source = String(policy.profileSource ?? policy.profile_source ?? '').trim()
  return {
    source: source === 'saved' ? 'saved' : source === 'temporary' ? 'temporary' : source === 'agent-default' ? 'agent-default' : '',
    profileId: String(policy.profileId ?? policy.profile_id ?? '').trim(),
    name: String(policy.profileName ?? policy.profile_name ?? '').trim(),
  }
}
