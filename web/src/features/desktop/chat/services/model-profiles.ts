import type {
  ActiveModelProfileState,
  AgentModelPolicyRecord,
  ModelOptionRecord,
  ModelProfileChoice,
  ModelProfileInput,
  ModelProfileRecord,
  ModelProfileSelectionRecord,
  SessionPreferenceRecord,
} from '../types/chat'
import { defaultModelThinking, modelOptionKey, normalizeModelServiceTier } from './model-options'

export type ModelProfileDraft = {
  name: string
  modelMode: 'single' | 'split'
  single: ModelProfileSelectionRecord
  plan: ModelProfileSelectionRecord
  auto: ModelProfileSelectionRecord
}

const EMPTY_SELECTION: ModelProfileSelectionRecord = {
  provider: '', model: '', thinking: '', serviceTier: '', contextMode: '',
}

export function emptyModelProfileDraft(name = ''): ModelProfileDraft {
  return { name, modelMode: 'single', single: { ...EMPTY_SELECTION }, plan: { ...EMPTY_SELECTION }, auto: { ...EMPTY_SELECTION } }
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
    modelMode: profile.modelMode,
    single: { ...(profile.single ?? EMPTY_SELECTION) },
    plan: { ...(profile.plan ?? EMPTY_SELECTION) },
    auto: { ...(profile.auto ?? EMPTY_SELECTION) },
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
  if (draft.modelMode === 'split') {
    return { name: draft.name.trim(), modelMode: 'split', single: null, plan: normalizedSelection(draft.plan), auto: normalizedSelection(draft.auto) }
  }
  return { name: draft.name.trim(), modelMode: 'single', single: normalizedSelection(draft.single), plan: null, auto: null }
}

export function selectionForProfileMode(
  profile: Pick<ModelProfileInput, 'modelMode' | 'single' | 'plan' | 'auto'>,
  mode: 'plan' | 'auto',
): ModelProfileSelectionRecord | null {
  return profile.modelMode === 'split' ? (mode === 'plan' ? profile.plan : profile.auto) : profile.single
}

export function preferenceFromModelProfile(
  profile: Pick<ModelProfileInput, 'modelMode' | 'single' | 'plan' | 'auto'>,
  mode: 'plan' | 'auto',
  updatedAt = 0,
): SessionPreferenceRecord | null {
  const selection = selectionForProfileMode(profile, mode)
  if (!selection) return null
  return { ...selection, updatedAt }
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
    serviceTier: String(selection.serviceTier ?? selection.service_tier ?? '').trim(),
    contextMode: String(selection.contextMode ?? selection.context_mode ?? '').trim().toLowerCase(),
  }
}

export function modelProfileFromMetadata(metadata: unknown): ModelProfileInput | null {
  const profile = record(record(metadata).model_profile)
  const modelMode = String(profile.model_mode ?? '').trim()
  if (modelMode === 'single') {
    const single = selectionFromMetadata(profile.single)
    return single ? { name: String(profile.name ?? '').trim(), modelMode: 'single', single, plan: null, auto: null } : null
  }
  if (modelMode === 'split') {
    const plan = selectionFromMetadata(profile.plan)
    const auto = selectionFromMetadata(profile.auto)
    return plan && auto ? { name: String(profile.name ?? '').trim(), modelMode: 'split', single: null, plan, auto } : null
  }
  return null
}

export function preferenceFromModelProfileMetadata(
  metadata: unknown,
  mode: 'plan' | 'auto',
): SessionPreferenceRecord | null {
  const profile = modelProfileFromMetadata(metadata)
  if (!profile) return null
  const snapshot = record(record(metadata).model_profile)
  const appliedAt = typeof snapshot.applied_at === 'number' ? snapshot.applied_at : 0
  return preferenceFromModelProfile(profile, mode, appliedAt)
}

export function activeModelProfileFromMetadata(metadata: unknown): ActiveModelProfileState {
  const profile = record(record(metadata).model_profile)
  const source = String(profile.source ?? '').trim()
  const modelMode = String(profile.model_mode ?? '').trim()
  return {
    source: source === 'saved' ? 'saved' : source === 'temporary' ? 'temporary' : '',
    profileId: String(profile.saved_profile_id ?? '').trim(),
    name: String(profile.name ?? '').trim(),
    modelMode: modelMode === 'split' ? 'split' : modelMode === 'single' ? 'single' : '',
  }
}

export function activeModelProfileFromPolicy(value: unknown): ActiveModelProfileState {
  const policy = record(value) as Partial<AgentModelPolicyRecord> & Record<string, unknown>
  const source = String(policy.profileSource ?? policy.profile_source ?? '').trim()
  return {
    source: source === 'saved' ? 'saved' : source === 'temporary' ? 'temporary' : source === 'agent-default' ? 'agent-default' : '',
    profileId: String(policy.profileId ?? policy.profile_id ?? '').trim(),
    name: String(policy.profileName ?? policy.profile_name ?? '').trim(),
    modelMode: String(policy.profileMode ?? policy.profile_mode ?? '').trim() === 'split' ? 'split' : String(policy.profileMode ?? policy.profile_mode ?? '').trim() === 'single' ? 'single' : '',
  }
}
