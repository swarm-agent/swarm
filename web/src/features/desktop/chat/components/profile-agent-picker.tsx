import { useCallback, useEffect, useLayoutEffect, useRef, useState, type MouseEvent as ReactMouseEvent } from 'react'
import { createPortal } from 'react-dom'
import { Check, ChevronDown, LoaderCircle } from 'lucide-react'
import type { ActiveModelProfileState, ModelProfileRecord, ModelProfileSelectionRecord } from '../types/chat'

interface ProfileAgentPickerProps {
  profiles: ModelProfileRecord[]
  activeProfile?: ActiveModelProfileState
  loading?: boolean
  error?: string | null
  busy?: boolean
  disabled?: boolean
  compact?: boolean
  modelDetail?: string
  onProfileSelect?: (profileId: string) => void | Promise<void>
}

const GUTTER = 8
const MOBILE_BREAKPOINT = 640

function selectionLabel(selection: ModelProfileSelectionRecord): string {
  const model = [selection.provider.trim(), selection.model.trim()].filter(Boolean).join('/') || 'Unavailable model'
  return [
    model,
    selection.thinking.trim() ? `thinking ${selection.thinking.trim()}` : '',
    selection.serviceTier.trim(),
  ].filter(Boolean).join(' · ')
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
    : savedProfile?.name || activeProfile?.name || (activeProfile?.source === 'agent-default' ? 'Agent default' : 'Resolved favorite')
  const modelLabel = input.modelDetail.trim() || (savedProfile ? selectionLabel(savedProfile) : 'Resolved model unavailable')
  return {
    profileLabel,
    modelLabel,
    combinedLabel: `${profileLabel} · ${modelLabel}`,
  }
}

export function ProfileAgentPicker({
  profiles,
  activeProfile,
  loading = false,
  error = null,
  busy = false,
  disabled = false,
  compact = false,
  modelDetail = '',
  onProfileSelect,
}: ProfileAgentPickerProps) {
  const [open, setOpen] = useState(false)
  const [localError, setLocalError] = useState<string | null>(null)
  const [position, setPosition] = useState<{ bottom: number; left: number; width: number; maxHeight: number } | null>(null)
  const triggerRef = useRef<HTMLButtonElement | null>(null)
  const dropdownRef = useRef<HTMLDivElement | null>(null)
  const pointerRef = useRef<{ x: number; y: number } | null>(null)
  const triggerDisplay = profileTriggerDisplay({ activeProfile, profiles, modelDetail })

  const updatePosition = useCallback(() => {
    const trigger = triggerRef.current
    if (!trigger || typeof window === 'undefined') return setPosition(null)
    const rect = trigger.getBoundingClientRect()
    const viewportWidth = window.innerWidth
    const viewportHeight = window.visualViewport?.height ?? window.innerHeight
    const mobile = viewportWidth < MOBILE_BREAKPOINT
    const width = mobile ? Math.max(160, viewportWidth - GUTTER * 2) : Math.min(460, Math.max(340, rect.width), viewportWidth - GUTTER * 2)
    const anchorX = pointerRef.current?.x ?? rect.left + rect.width / 2
    const anchorY = pointerRef.current?.y ?? rect.top
    setPosition({
      bottom: Math.max(GUTTER, viewportHeight - anchorY + GUTTER),
      left: mobile ? GUTTER : Math.min(Math.max(GUTTER, anchorX - width / 2), viewportWidth - width - GUTTER),
      width,
      maxHeight: Math.max(180, Math.min(420, anchorY - GUTTER * 2)),
    })
  }, [])

  useLayoutEffect(() => {
    if (open) updatePosition()
    else setPosition(null)
  }, [open, updatePosition])

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

  const selectProfile = useCallback(async (profileId: string) => {
    if (!onProfileSelect) return
    if (activeProfile?.source === 'saved' && activeProfile.profileId === profileId) {
      setOpen(false)
      return
    }
    setLocalError(null)
    try {
      await onProfileSelect(profileId)
      setOpen(false)
    } catch (cause) {
      setLocalError(cause instanceof Error ? cause.message : String(cause))
    }
  }, [activeProfile, onProfileSelect])

  const dropdown = open && position ? createPortal(
    <div ref={dropdownRef} style={{ position: 'fixed', bottom: position.bottom, left: position.left, width: position.width, maxHeight: position.maxHeight, zIndex: 9999 }}>
      <div className="flex max-h-[inherit] min-h-0 flex-col overflow-hidden rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] shadow-xl shadow-black/40" aria-busy={busy || loading}>
        <div className="shrink-0 border-b border-[var(--app-border)] px-3 py-2.5">
          <div className="text-[11px] font-semibold uppercase tracking-wider text-[var(--app-text-subtle)]">Session favorite</div>
          <div className="mt-1 truncate text-xs text-[var(--app-text-muted)]" data-testid="resolved-session-model-status">{triggerDisplay.modelLabel}</div>
        </div>
        <div className="min-h-0 flex-1 overflow-y-auto p-2">
          {loading ? (
            <div className="flex items-center gap-2 rounded-lg border border-[var(--app-border)] px-3 py-3 text-xs text-[var(--app-text-muted)]" role="status"><LoaderCircle size={14} className="animate-spin" />Loading favorites…</div>
          ) : profiles.length === 0 ? (
            <div className="rounded-lg border border-dashed border-[var(--app-border)] px-3 py-4 text-center text-xs text-[var(--app-text-muted)]">No permitted saved favorites</div>
          ) : (
            <div className="grid gap-1.5" role="menu" aria-label="Saved favorites">
              {profiles.map((profile) => {
                const selected = activeProfile?.source === 'saved' && activeProfile.profileId === profile.profileId
                return <button key={profile.profileId} type="button" role="menuitemradio" aria-checked={selected} disabled={busy || !onProfileSelect} onClick={() => { void selectProfile(profile.profileId) }} className={`flex min-w-0 items-center gap-2 rounded-lg border px-3 py-2.5 text-left disabled:opacity-50 ${selected ? 'border-[var(--app-primary)] bg-[var(--app-surface-subtle)]' : 'border-[var(--app-border)] hover:border-[var(--app-border-strong)] hover:bg-[var(--app-surface-hover)]'}`}>
                  <span className="min-w-0 flex-1">
                    <span className="block truncate text-sm font-semibold text-[var(--app-text)]">{profile.name}</span>
                    <span className="mt-0.5 block truncate text-xs text-[var(--app-text-subtle)]">{selectionLabel(profile)}</span>
                  </span>
                  {selected ? <Check size={15} className="shrink-0 text-[var(--app-primary)]" aria-hidden="true" /> : null}
                </button>
              })}
            </div>
          )}
        </div>
        {busy && !loading ? <div className="flex shrink-0 items-center gap-2 border-t border-[var(--app-border)] px-3 py-2 text-xs text-[var(--app-text-muted)]" role="status"><LoaderCircle size={13} className="animate-spin" />Applying favorite…</div> : null}
        {error || localError ? <div role="alert" className="shrink-0 border-t border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-3 py-2 text-xs text-[var(--app-danger)]">{localError || error}</div> : null}
      </div>
    </div>, document.body) : null

  return <div className="inline-flex min-w-0 items-center">
    <button ref={triggerRef} type="button" disabled={disabled} aria-expanded={open} aria-haspopup="menu" aria-label={`Session favorite: ${triggerDisplay.combinedLabel}`} title={triggerDisplay.combinedLabel} onClick={(event: ReactMouseEvent<HTMLButtonElement>) => {
      if (!open) pointerRef.current = event.detail > 0 ? { x: event.clientX, y: event.clientY } : null
      setOpen((value) => !value)
    }} className="inline-flex min-h-9 min-w-0 items-center gap-2 rounded-lg px-3 py-2 text-xs font-medium text-[var(--app-text-muted)] transition hover:text-[var(--app-text)] disabled:opacity-50">
      {busy ? <LoaderCircle size={14} className="shrink-0 animate-spin" aria-hidden="true" /> : null}
      <span className={`min-w-0 truncate text-[11px] ${compact ? 'max-w-[240px]' : 'max-w-[420px]'}`}>
        <span className="font-medium text-[var(--app-text-muted)]">{triggerDisplay.profileLabel}</span>
        <span aria-hidden="true" className="text-[var(--app-text-subtle)]"> · </span>
        <span data-testid="selected-model-detail" className="text-[var(--app-text-subtle)]">{triggerDisplay.modelLabel}</span>
      </span>
      <ChevronDown size={14} className={`shrink-0 transition-transform ${open ? 'rotate-180' : ''}`} />
    </button>
    {dropdown}
  </div>
}
