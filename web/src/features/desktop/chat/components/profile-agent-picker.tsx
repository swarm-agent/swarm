import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type MouseEvent as ReactMouseEvent } from 'react'
import { createPortal } from 'react-dom'
import { Check, ChevronDown, LoaderCircle, Plus } from 'lucide-react'
import type { ActiveModelProfileState, ModelOptionRecord, ModelProfileInput, ModelProfileRecord, ModelProfileSelectionRecord } from '../types/chat'
import { defaultModelThinking, modelServiceTierOptions, modelThinkingOptions } from '../services/model-options'

interface ProfileAgentPickerProps {
  profiles: ModelProfileRecord[]
  modelOptions?: ModelOptionRecord[]
  activeProfile?: ActiveModelProfileState
  loading?: boolean
  error?: string | null
  busy?: boolean
  disabled?: boolean
  compact?: boolean
  modelDetail?: string
  onProfileSelect?: (profileId: string) => void | Promise<void>
  onFavoriteCreate?: (input: ModelProfileInput) => string | Promise<string>
}

const GUTTER = 8
const MOBILE_BREAKPOINT = 640

type FavoriteDraft = Pick<ModelProfileInput, 'provider' | 'model' | 'thinking' | 'serviceTier' | 'contextMode'>

function selectionLabel(selection: ModelProfileSelectionRecord): string {
  const model = [selection.provider.trim(), selection.model.trim()].filter(Boolean).join('/') || 'Unavailable model'
  return [model, selection.thinking.trim() ? `thinking ${selection.thinking.trim()}` : '', selection.serviceTier.trim()].filter(Boolean).join(' · ')
}

function draftFromProfile(profile: ModelProfileRecord | null): FavoriteDraft {
  return {
    provider: profile?.provider ?? '',
    model: profile?.model ?? '',
    thinking: profile?.thinking ?? '',
    serviceTier: profile?.serviceTier ?? '',
    contextMode: profile?.contextMode ?? '',
  }
}

function draftSignature(draft: FavoriteDraft): string {
  return JSON.stringify(draft)
}

export function profileTriggerDisplay(input: {
  activeProfile?: ActiveModelProfileState
  profiles: ModelProfileRecord[]
  modelDetail: string
}): { profileLabel: string; modelLabel: string; combinedLabel: string } {
  const { activeProfile, profiles } = input
  const savedProfile = activeProfile?.source === 'saved'
    ? profiles.find((profile) => profile.profileId === activeProfile.profileId) ?? null
    : null
  const profileLabel = activeProfile?.source === 'temporary'
    ? activeProfile.name || 'Temporary favorite'
    : savedProfile?.name || activeProfile?.name || (activeProfile?.source === 'agent-default' ? 'Action favorite' : 'Resolved favorite')
  const modelLabel = input.modelDetail.trim() || (savedProfile ? selectionLabel(savedProfile) : 'Resolved model unavailable')
  return { profileLabel, modelLabel, combinedLabel: `${profileLabel} · ${modelLabel}` }
}

export function ProfileAgentPicker({
  profiles,
  modelOptions = [],
  activeProfile,
  loading = false,
  error = null,
  busy = false,
  disabled = false,
  compact = false,
  modelDetail = '',
  onProfileSelect,
  onFavoriteCreate,
}: ProfileAgentPickerProps) {
  const [open, setOpen] = useState(false)
  const [localError, setLocalError] = useState<string | null>(null)
  const [stagedProfileId, setStagedProfileId] = useState('')
  const [draft, setDraft] = useState<FavoriteDraft>(() => draftFromProfile(null))
  const [favoriteName, setFavoriteName] = useState('')
  const [addingFavorite, setAddingFavorite] = useState(false)
  const [position, setPosition] = useState<{ bottom: number; left: number; width: number; maxHeight: number } | null>(null)
  const triggerRef = useRef<HTMLButtonElement | null>(null)
  const dropdownRef = useRef<HTMLDivElement | null>(null)
  const pointerRef = useRef<{ x: number; y: number } | null>(null)
  const triggerDisplay = profileTriggerDisplay({ activeProfile, profiles, modelDetail })
  const stagedProfile = profiles.find((profile) => profile.profileId === stagedProfileId) ?? null
  const draftChanged = stagedProfile ? draftSignature(draft) !== draftSignature(draftFromProfile(stagedProfile)) : Boolean(draft.provider || draft.model)
  const providers = useMemo(() => Array.from(new Set(modelOptions.map((option) => option.provider.trim()).filter(Boolean))).sort(), [modelOptions])
  const modelChoices = useMemo(() => modelOptions.filter((option) => option.provider === draft.provider), [draft.provider, modelOptions])
  const selectedOption = modelChoices.find((option) => option.model === draft.model && option.contextMode === draft.contextMode)
    ?? modelChoices.find((option) => option.model === draft.model)
    ?? null
  const thinkingOptions = modelThinkingOptions(selectedOption)
  const serviceTiers = selectedOption ? modelServiceTierOptions(selectedOption.provider, selectedOption.model, selectedOption) : []
  const contextModes = Array.from(new Set([...(selectedOption?.contextModes ?? []).map((mode) => mode.mode), selectedOption?.contextMode ?? '', draft.contextMode].map((value) => value.trim()).filter(Boolean)))

  const stageProfile = useCallback((profile: ModelProfileRecord) => {
    setStagedProfileId(profile.profileId)
    setDraft(draftFromProfile(profile))
    setAddingFavorite(false)
    setFavoriteName('')
    setLocalError(null)
  }, [])

  const resetDraft = useCallback(() => {
    const activeId = activeProfile?.source === 'saved' ? activeProfile.profileId : ''
    const active = profiles.find((profile) => profile.profileId === activeId) ?? profiles[0] ?? null
    setStagedProfileId(active?.profileId ?? '')
    setDraft(draftFromProfile(active))
    setAddingFavorite(false)
    setFavoriteName('')
    setLocalError(null)
  }, [activeProfile, profiles])

  const updatePosition = useCallback(() => {
    const trigger = triggerRef.current
    if (!trigger || typeof window === 'undefined') return setPosition(null)
    const rect = trigger.getBoundingClientRect()
    const viewportWidth = window.innerWidth
    const viewportHeight = window.visualViewport?.height ?? window.innerHeight
    const mobile = viewportWidth < MOBILE_BREAKPOINT
    const width = mobile ? Math.max(160, viewportWidth - GUTTER * 2) : Math.min(620, Math.max(420, rect.width), viewportWidth - GUTTER * 2)
    const anchorX = pointerRef.current?.x ?? rect.left + rect.width / 2
    const anchorY = pointerRef.current?.y ?? rect.top
    setPosition({
      bottom: Math.max(GUTTER, viewportHeight - anchorY + GUTTER),
      left: mobile ? GUTTER : Math.min(Math.max(GUTTER, anchorX - width / 2), viewportWidth - width - GUTTER),
      width,
      maxHeight: Math.max(240, Math.min(620, anchorY - GUTTER * 2)),
    })
  }, [])

  useLayoutEffect(() => {
    if (open) updatePosition()
    else setPosition(null)
  }, [open, updatePosition])

  useEffect(() => {
    if (!open) return
    resetDraft()
  }, [open, resetDraft])

  useEffect(() => {
    if (!open) return
    const outside = (event: PointerEvent) => {
      const target = event.target as Node | null
      if (!target || triggerRef.current?.contains(target) || dropdownRef.current?.contains(target)) return
      setOpen(false)
    }
    const escape = (event: KeyboardEvent) => { if (event.key === 'Escape') setOpen(false) }
    window.addEventListener('resize', updatePosition)
    window.addEventListener('scroll', updatePosition, true)
    document.addEventListener('pointerdown', outside)
    document.addEventListener('keydown', escape)
    return () => {
      window.removeEventListener('resize', updatePosition)
      window.removeEventListener('scroll', updatePosition, true)
      document.removeEventListener('pointerdown', outside)
      document.removeEventListener('keydown', escape)
    }
  }, [open, updatePosition])

  const saveStagedProfile = useCallback(async () => {
    if (!onProfileSelect || !stagedProfileId || draftChanged) return
    setLocalError(null)
    try {
      await onProfileSelect(stagedProfileId)
      setOpen(false)
    } catch (cause) {
      setLocalError(cause instanceof Error ? cause.message : String(cause))
    }
  }, [draftChanged, onProfileSelect, stagedProfileId])

  const createAndAssignFavorite = useCallback(async () => {
    if (!onFavoriteCreate || !onProfileSelect || !favoriteName.trim() || !draft.provider.trim() || !draft.model.trim()) return
    setLocalError(null)
    try {
      const profileId = await onFavoriteCreate({ name: favoriteName.trim(), ...draft })
      await onProfileSelect(profileId)
      setOpen(false)
    } catch (cause) {
      setLocalError(cause instanceof Error ? cause.message : String(cause))
    }
  }, [draft, favoriteName, onFavoriteCreate, onProfileSelect])

  const updateProvider = (provider: string) => {
    const option = modelOptions.find((candidate) => candidate.provider === provider) ?? null
    setDraft({
      provider,
      model: option?.model ?? '',
      thinking: defaultModelThinking(option),
      serviceTier: option?.defaultServiceTier ?? '',
      contextMode: option?.contextMode ?? '',
    })
  }

  const updateModel = (key: string) => {
    const option = modelChoices.find((candidate) => `${candidate.model}::${candidate.contextMode}` === key) ?? null
    setDraft((current) => ({
      ...current,
      model: option?.model ?? '',
      thinking: defaultModelThinking(option),
      serviceTier: option?.defaultServiceTier ?? '',
      contextMode: option?.contextMode ?? '',
    }))
  }

  const dropdown = open && position ? createPortal(
    <div ref={dropdownRef} style={{ position: 'fixed', bottom: position.bottom, left: position.left, width: position.width, maxHeight: position.maxHeight, zIndex: 9999 }}>
      <div className="flex max-h-[inherit] min-h-0 flex-col overflow-hidden rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] shadow-xl shadow-black/40" aria-busy={busy || loading}>
        <div className="shrink-0 border-b border-[var(--app-border)] px-4 py-3">
          <div className="text-sm font-semibold text-[var(--app-text)]">Profiles</div>
          <div className="mt-1 text-xs text-[var(--app-text-muted)]">Choose an Action favorite or change its model and add a new favorite.</div>
        </div>
        <div className="min-h-0 flex-1 overflow-y-auto p-3">
          {loading ? (
            <div className="flex items-center gap-2 rounded-lg border border-[var(--app-border)] px-3 py-3 text-xs text-[var(--app-text-muted)]" role="status"><LoaderCircle size={14} className="animate-spin" />Loading favorites…</div>
          ) : (
            <>
              <div className="grid gap-1.5" role="radiogroup" aria-label="Action favorites">
                {profiles.map((profile) => {
                  const selected = stagedProfileId === profile.profileId
                  return <button key={profile.profileId} type="button" role="radio" aria-checked={selected} disabled={busy} onClick={() => stageProfile(profile)} className={`flex min-w-0 items-center gap-2 rounded-lg border px-3 py-2 text-left disabled:opacity-50 ${selected ? 'border-[var(--app-primary)] bg-[var(--app-surface-subtle)]' : 'border-[var(--app-border)] hover:bg-[var(--app-surface-hover)]'}`}>
                    <span className="min-w-0 flex-1"><span className="block truncate text-sm font-semibold text-[var(--app-text)]">{profile.name}</span><span className="block truncate text-xs text-[var(--app-text-subtle)]">{selectionLabel(profile)}</span></span>
                    {selected ? <Check size={15} className="shrink-0 text-[var(--app-primary)]" /> : null}
                  </button>
                })}
                {profiles.length === 0 ? <div className="rounded-lg border border-dashed border-[var(--app-border)] px-3 py-3 text-center text-xs text-[var(--app-text-muted)]">No favorites yet. Choose a model below to add one.</div> : null}
              </div>

              <div className="mt-3 grid gap-3 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-3 sm:grid-cols-2">
                <label className="grid gap-1 text-xs font-medium text-[var(--app-text-muted)]">Provider<select value={draft.provider} disabled={busy} onChange={(event) => updateProvider(event.target.value)} className="rounded-lg border border-[var(--app-border)] bg-[var(--app-bg)] px-2.5 py-2 text-sm text-[var(--app-text)]"><option value="">Choose provider</option>{providers.map((provider) => <option key={provider} value={provider}>{provider}</option>)}</select></label>
                <label className="grid gap-1 text-xs font-medium text-[var(--app-text-muted)]">Model<select value={selectedOption ? `${selectedOption.model}::${selectedOption.contextMode}` : ''} disabled={busy || !draft.provider} onChange={(event) => updateModel(event.target.value)} className="rounded-lg border border-[var(--app-border)] bg-[var(--app-bg)] px-2.5 py-2 text-sm text-[var(--app-text)]"><option value="">Choose model</option>{modelChoices.map((option) => <option key={`${option.model}::${option.contextMode}`} value={`${option.model}::${option.contextMode}`}>{option.label || option.model}</option>)}</select></label>
                <label className="grid gap-1 text-xs font-medium text-[var(--app-text-muted)]">Thinking<select value={draft.thinking} disabled={busy || !selectedOption} onChange={(event) => setDraft((current) => ({ ...current, thinking: event.target.value }))} className="rounded-lg border border-[var(--app-border)] bg-[var(--app-bg)] px-2.5 py-2 text-sm text-[var(--app-text)]">{thinkingOptions.map((thinking) => <option key={thinking} value={thinking}>{thinking}</option>)}</select></label>
                <label className="grid gap-1 text-xs font-medium text-[var(--app-text-muted)]">Service tier<select value={draft.serviceTier} disabled={busy || !selectedOption} onChange={(event) => setDraft((current) => ({ ...current, serviceTier: event.target.value }))} className="rounded-lg border border-[var(--app-border)] bg-[var(--app-bg)] px-2.5 py-2 text-sm text-[var(--app-text)]"><option value="">Provider default</option>{serviceTiers.filter((tier) => tier.value).map((tier) => <option key={tier.value} value={tier.value}>{tier.label}</option>)}</select></label>
                {contextModes.length > 0 ? <label className="grid gap-1 text-xs font-medium text-[var(--app-text-muted)] sm:col-span-2">Context mode<select value={draft.contextMode} disabled={busy} onChange={(event) => setDraft((current) => ({ ...current, contextMode: event.target.value }))} className="rounded-lg border border-[var(--app-border)] bg-[var(--app-bg)] px-2.5 py-2 text-sm text-[var(--app-text)]"><option value="">Provider default</option>{contextModes.map((mode) => <option key={mode} value={mode}>{mode}</option>)}</select></label> : null}
              </div>

              {addingFavorite ? <div className="mt-3 flex gap-2"><input autoFocus value={favoriteName} disabled={busy} onChange={(event) => setFavoriteName(event.target.value)} placeholder="Favorite name" aria-label="New favorite name" className="min-w-0 flex-1 rounded-lg border border-[var(--app-border)] bg-[var(--app-bg)] px-3 py-2 text-sm text-[var(--app-text)]" /><button type="button" disabled={busy || !favoriteName.trim() || !draft.provider || !draft.model} onClick={() => { void createAndAssignFavorite() }} className="rounded-lg bg-[var(--app-primary)] px-3 py-2 text-xs font-semibold text-[var(--app-primary-text)] disabled:opacity-50">Add & use</button></div> : null}
            </>
          )}
        </div>
        {error || localError ? <div role="alert" className="shrink-0 border-t border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-3 py-2 text-xs text-[var(--app-danger)]">{localError || error}</div> : null}
        <div className="flex shrink-0 items-center justify-between gap-2 border-t border-[var(--app-border)] px-3 py-2.5">
          <button type="button" disabled={busy || loading || !onFavoriteCreate} onClick={() => setAddingFavorite((value) => !value)} className="inline-flex items-center gap-1.5 rounded-lg px-2.5 py-2 text-xs font-semibold text-[var(--app-text)] hover:bg-[var(--app-surface-hover)] disabled:opacity-50"><Plus size={14} />Add favorite</button>
          <div className="flex gap-2"><button type="button" onClick={() => setOpen(false)} className="rounded-lg px-3 py-2 text-xs font-semibold text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)]">Cancel</button><button type="button" disabled={busy || loading || !stagedProfileId || draftChanged || !onProfileSelect} onClick={() => { void saveStagedProfile() }} className="rounded-lg bg-[var(--app-primary)] px-3 py-2 text-xs font-semibold text-[var(--app-primary-text)] disabled:opacity-50">{busy ? 'Saving…' : 'Save'}</button></div>
        </div>
      </div>
    </div>, document.body) : null

  return <div className="inline-flex min-w-0 items-center">
    <button ref={triggerRef} type="button" disabled={disabled} aria-expanded={open} aria-haspopup="dialog" aria-label={`Profiles: ${triggerDisplay.combinedLabel}`} title={triggerDisplay.combinedLabel} onClick={(event: ReactMouseEvent<HTMLButtonElement>) => {
      if (!open) pointerRef.current = event.detail > 0 ? { x: event.clientX, y: event.clientY } : null
      setOpen((value) => !value)
    }} className="inline-flex min-h-9 min-w-0 items-center gap-2 rounded-lg px-3 py-2 text-xs font-medium text-[var(--app-text-muted)] transition hover:text-[var(--app-text)] disabled:opacity-50">
      {busy ? <LoaderCircle size={14} className="shrink-0 animate-spin" /> : null}
      <span className={`min-w-0 truncate text-[11px] ${compact ? 'max-w-[240px]' : 'max-w-[420px]'}`}><span className="font-semibold text-[var(--app-text)]">Profiles</span><span aria-hidden="true" className="text-[var(--app-text-subtle)]"> · </span><span>{triggerDisplay.profileLabel}</span><span aria-hidden="true" className="text-[var(--app-text-subtle)]"> · </span><span data-testid="selected-model-detail" className="text-[var(--app-text-subtle)]">{triggerDisplay.modelLabel}</span></span>
      <ChevronDown size={14} className={`shrink-0 transition-transform ${open ? 'rotate-180' : ''}`} />
    </button>
    {dropdown}
  </div>
}
