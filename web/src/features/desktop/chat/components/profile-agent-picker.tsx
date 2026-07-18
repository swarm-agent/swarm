import { useCallback, useEffect, useLayoutEffect, useRef, useState, type MouseEvent as ReactMouseEvent } from 'react'
import { createPortal } from 'react-dom'
import { Check, ChevronDown, Pencil, Plus, Star, Trash2 } from 'lucide-react'
import type { ActiveModelProfileState, AgentProfileRecord, ModelProfileRecord, ModelProfileSelectionRecord } from '../types/chat'
import { displayAgentName } from '../services/agent-display'
import type { DesktopSessionMode } from '../../settings/swarm/types/swarm-settings'

interface ProfileAgentPickerProps {
  currentAgent: string
  selectedPrimaryAgent: string
  agents: AgentProfileRecord[]
  profiles: ModelProfileRecord[]
  activeProfile?: ActiveModelProfileState
  mode?: DesktopSessionMode
  loading?: boolean
  error?: string | null
  busy?: boolean
  disabled?: boolean
  compact?: boolean
  modelDetail?: string
  onAgentSelect?: (agent: string) => void | Promise<void>
  onProfileSelect?: (profileId: string) => void | Promise<void>
  onAddProfile: () => void
  onEditProfile: (profileId: string) => void
  onSetDefault: (profileId: string) => void | Promise<void>
  onDeleteProfile: (profileId: string) => void | Promise<void>
}

const GUTTER = 8
const MOBILE_BREAKPOINT = 640

function selectionLabel(selection: ModelProfileSelectionRecord | null): string {
  if (!selection) return 'Unavailable selection'
  const model = [selection.provider.trim(), selection.model.trim()].filter(Boolean).join('/') || 'Default model'
  return [model, selection.thinking.trim() ? `thinking ${selection.thinking.trim()}` : '', selection.serviceTier.trim()].filter(Boolean).join(' · ')
}

function profileLabels(profile: ModelProfileRecord): string[] {
  if (profile.modelMode === 'split') return [`Plan ${selectionLabel(profile.plan)}`, `Action ${selectionLabel(profile.auto)}`]
  return [selectionLabel(profile.single)]
}

export function profileTriggerDisplay(input: {
  activeProfile?: ActiveModelProfileState
  profiles: ModelProfileRecord[]
  mode: DesktopSessionMode
  modelDetail: string
}): { profileLabel: string; modelLabel: string; combinedLabel: string } {
  const { activeProfile, profiles, mode } = input
  const profileLabel = activeProfile?.source === 'temporary'
    ? 'Temporary/customized'
    : activeProfile?.source === 'saved'
      ? activeProfile.name || 'Saved profile'
      : ''
  const savedProfile = activeProfile?.source === 'saved'
    ? profiles.find((profile) => profile.profileId === activeProfile.profileId) ?? null
    : null
  const modelMode = activeProfile?.modelMode || savedProfile?.modelMode || ''
  const selection = savedProfile
    ? savedProfile.modelMode === 'split'
      ? mode === 'plan' ? savedProfile.plan : savedProfile.auto
      : savedProfile.single
    : null
  const resolvedModel = input.modelDetail.trim() || (selection ? selectionLabel(selection) : '')
  const modelLabel = resolvedModel && modelMode === 'split'
    ? `${mode === 'plan' ? 'Plan' : 'Action'} ${resolvedModel}`
    : resolvedModel || 'Agent model default'
  return {
    profileLabel,
    modelLabel,
    combinedLabel: [profileLabel, modelLabel].filter(Boolean).join(' · '),
  }
}

function agentMode(profile: AgentProfileRecord): string {
  return (profile.mode || 'primary').trim().toLowerCase()
}

export function ProfileAgentPicker({
  currentAgent,
  selectedPrimaryAgent,
  agents,
  profiles,
  activeProfile,
  mode = 'auto',
  loading = false,
  error = null,
  busy = false,
  disabled = false,
  compact = false,
  modelDetail = '',
  onAgentSelect,
  onProfileSelect,
  onAddProfile,
  onEditProfile,
  onSetDefault,
  onDeleteProfile,
}: ProfileAgentPickerProps) {
  const [open, setOpen] = useState(false)
  const [localError, setLocalError] = useState<string | null>(null)
  const [position, setPosition] = useState<{ bottom: number; left: number; width: number; maxHeight: number } | null>(null)
  const triggerRef = useRef<HTMLButtonElement | null>(null)
  const dropdownRef = useRef<HTMLDivElement | null>(null)
  const pointerRef = useRef<{ x: number; y: number } | null>(null)
  const selectedAgent = agents.find((agent) => agent.name === currentAgent) ?? agents.find((agent) => agent.name === selectedPrimaryAgent)
  const triggerDisplay = profileTriggerDisplay({ activeProfile, profiles, mode, modelDetail })

  const updatePosition = useCallback(() => {
    const trigger = triggerRef.current
    if (!trigger || typeof window === 'undefined') return setPosition(null)
    const rect = trigger.getBoundingClientRect()
    const viewportWidth = window.innerWidth
    const viewportHeight = window.visualViewport?.height ?? window.innerHeight
    const mobile = viewportWidth < MOBILE_BREAKPOINT
    const width = mobile ? Math.max(160, viewportWidth - GUTTER * 2) : Math.min(520, Math.max(360, rect.width), viewportWidth - GUTTER * 2)
    const anchorX = pointerRef.current?.x ?? rect.left + rect.width / 2
    const anchorY = pointerRef.current?.y ?? rect.top
    setPosition({
      bottom: Math.max(GUTTER, viewportHeight - anchorY + GUTTER),
      left: mobile ? GUTTER : Math.min(Math.max(GUTTER, anchorX - width / 2), viewportWidth - width - GUTTER),
      width,
      maxHeight: Math.max(180, Math.min(480, anchorY - GUTTER * 2)),
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

  const invoke = useCallback(async (action: () => void | Promise<void>, close = false) => {
    setLocalError(null)
    try {
      await action()
      if (close) setOpen(false)
    } catch (cause) {
      setLocalError(cause instanceof Error ? cause.message : String(cause))
    }
  }, [])

  const agentSections = [
    { label: 'Primary agents', agents: agents.filter((agent) => agentMode(agent) === 'primary') },
    { label: 'Subagents', agents: agents.filter((agent) => agentMode(agent) === 'subagent') },
    { label: 'Other agents', agents: agents.filter((agent) => !['primary', 'subagent'].includes(agentMode(agent))) },
  ].filter((section) => section.agents.length > 0)

  const dropdown = open && position ? createPortal(
    <div ref={dropdownRef} style={{ position: 'fixed', bottom: position.bottom, left: position.left, width: position.width, maxHeight: position.maxHeight, zIndex: 9999 }}>
      <div className="flex max-h-[inherit] min-h-0 flex-col overflow-hidden rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] shadow-xl shadow-black/40">
        <div className="shrink-0 border-b border-[var(--app-border)] px-3 py-2.5 text-[11px] font-semibold uppercase tracking-wider text-[var(--app-text-subtle)]">Profiles and agents</div>
        <div className="min-h-0 flex-1 overflow-y-auto">
          <section aria-label="Profiles" className="px-2 py-2">
            <div className="flex items-center justify-between gap-2 px-1 pb-1.5">
              <span className="text-[10px] font-semibold uppercase tracking-wider text-[var(--app-text-subtle)]">Profiles</span>
              <button type="button" onClick={() => { setOpen(false); onAddProfile() }} disabled={busy} className="inline-flex items-center gap-1 rounded-md px-2 py-1 text-[11px] font-semibold text-[var(--app-primary)] hover:bg-[var(--app-surface-hover)] disabled:opacity-50"><Plus size={12} />Add profile</button>
            </div>
            {loading ? <div className="rounded-lg border border-[var(--app-border)] px-3 py-3 text-xs text-[var(--app-text-muted)]">Loading profiles…</div> : profiles.length === 0 ? (
              <div className="rounded-lg border border-dashed border-[var(--app-border)] px-3 py-4 text-center text-xs text-[var(--app-text-muted)]">No profiles yet</div>
            ) : (
              <div className="overflow-hidden rounded-lg border border-[var(--app-border)]">
                {profiles.map((profile, index) => {
                  const selected = activeProfile?.source === 'saved' && activeProfile.profileId === profile.profileId
                  return <div key={profile.profileId} className={`group flex items-stretch ${index ? 'border-t border-[var(--app-border)]' : ''} ${selected ? 'bg-[var(--app-surface-subtle)]' : 'hover:bg-[var(--app-surface-hover)]'}`}>
                    <button type="button" disabled={busy || !onProfileSelect} onClick={() => void invoke(() => onProfileSelect?.(profile.profileId), true)} className="flex min-w-0 flex-1 items-start gap-2 px-3 py-2.5 text-left disabled:opacity-50">
                      {selected ? <Check size={14} className="mt-0.5 shrink-0 text-[var(--app-primary)]" /> : <span className="w-[14px] shrink-0" />}
                      <span className="min-w-0 flex-1">
                        <span className="flex flex-wrap items-center gap-1.5 text-sm font-medium text-[var(--app-text)]">{profile.name}{profile.isDefault ? <span className="rounded-full border border-[var(--app-primary)] px-1.5 py-0.5 text-[9px] font-semibold uppercase text-[var(--app-primary)]">Default</span> : null}</span>
                        <span className="mt-0.5 block text-[10px] leading-4 text-[var(--app-text-subtle)]">{profileLabels(profile).map((label) => <span key={label} className="block break-words">{label}</span>)}</span>
                      </span>
                    </button>
                    <div className="flex shrink-0 items-center pr-1">
                      {!profile.isDefault ? <button type="button" disabled={busy} onClick={() => void invoke(() => onSetDefault(profile.profileId))} aria-label={`Make ${profile.name} default`} title="Make default" className="rounded-md p-1.5 text-[var(--app-text-subtle)] hover:bg-[var(--app-surface)] hover:text-[var(--app-primary)] disabled:opacity-50"><Star size={13} /></button> : null}
                      <button type="button" disabled={busy} onClick={() => { setOpen(false); onEditProfile(profile.profileId) }} aria-label={`Edit ${profile.name}`} className="rounded-md p-1.5 text-[var(--app-text-subtle)] hover:bg-[var(--app-surface)] hover:text-[var(--app-text)] disabled:opacity-50"><Pencil size={13} /></button>
                      <button type="button" disabled={busy} onClick={() => { if (window.confirm(`Delete profile “${profile.name}”?`)) void invoke(() => onDeleteProfile(profile.profileId)) }} aria-label={`Delete ${profile.name}`} className="rounded-md p-1.5 text-[var(--app-text-subtle)] hover:bg-[var(--app-danger-bg)] hover:text-[var(--app-danger)] disabled:opacity-50"><Trash2 size={13} /></button>
                    </div>
                  </div>
                })}
              </div>
            )}
          </section>
          {agentSections.map((section) => <section key={section.label} aria-label={section.label} className="border-t border-[var(--app-border)] px-2 py-2">
            <div className="px-1 pb-1.5 text-[10px] font-semibold uppercase tracking-wider text-[var(--app-text-subtle)]">{section.label}</div>
            <div className="overflow-hidden rounded-lg border border-[var(--app-border)]">
              {section.agents.map((agent, index) => {
                const selected = agent.name === selectedAgent?.name && !activeProfile?.source
                return <button key={agent.name} type="button" disabled={busy || !onAgentSelect} onClick={() => void invoke(() => onAgentSelect?.(agent.name), true)} className={`flex w-full items-start gap-2 px-3 py-2.5 text-left text-sm ${index ? 'border-t border-[var(--app-border)]' : ''} ${selected ? 'bg-[var(--app-surface-subtle)] text-[var(--app-text)]' : 'text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]'} disabled:opacity-50`}>
                  {selected ? <Check size={14} className="mt-0.5 text-[var(--app-primary)]" /> : <span className="w-[14px]" />}
                  <span className="font-medium">{displayAgentName(agent.name)}</span>
                </button>
              })}
            </div>
          </section>)}
        </div>
        {error || localError ? <div role="alert" className="shrink-0 border-t border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-3 py-2 text-xs text-[var(--app-danger)]">{localError || error}</div> : null}
      </div>
    </div>, document.body) : null

  return <div className="inline-flex min-w-0 items-center">
    <button ref={triggerRef} type="button" disabled={disabled} aria-expanded={open} aria-haspopup="menu" aria-label={`Model profile: ${triggerDisplay.combinedLabel}`} title={triggerDisplay.combinedLabel} onClick={(event: ReactMouseEvent<HTMLButtonElement>) => {
      if (!open) pointerRef.current = event.detail > 0 ? { x: event.clientX, y: event.clientY } : null
      setOpen((value) => !value)
    }} className="inline-flex min-h-9 min-w-0 items-center gap-2 rounded-lg px-3 py-2 text-xs font-medium text-[var(--app-text-muted)] transition hover:text-[var(--app-text)] disabled:opacity-50">
      <span className={`min-w-0 truncate text-[11px] ${compact ? 'max-w-[240px]' : 'max-w-[420px]'}`}>
        {triggerDisplay.profileLabel ? <><span className="font-medium text-[var(--app-text-muted)]">{triggerDisplay.profileLabel}</span><span aria-hidden="true" className="text-[var(--app-text-subtle)]"> · </span></> : null}
        <span data-testid="selected-model-detail" className="text-[var(--app-text-subtle)]">{triggerDisplay.modelLabel}</span>
      </span>
      <ChevronDown size={14} className={`shrink-0 transition-transform ${open ? 'rotate-180' : ''}`} />
    </button>
    {dropdown}
  </div>
}
