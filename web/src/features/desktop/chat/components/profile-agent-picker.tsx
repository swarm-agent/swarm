import { useCallback, useEffect, useLayoutEffect, useRef, useState, type MouseEvent as ReactMouseEvent, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { ChevronDown, Plus, Settings2, Star, Trash2 } from 'lucide-react'
import type { ActiveModelProfileState, AgentProfileRecord, ModelProfileRecord, ModelProfileSelectionRecord } from '../types/chat'
import { displayAgentName } from '../services/agent-display'
import { canSwitchModelProfilePolicyGroup, initialModelProfilePolicyGroup, modelProfilePolicyGroupLabel, modelProfilesInPolicyGroup, type ModelProfilePolicyGroup } from '../services/model-profile-groups'
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
  renderTrigger?: (input: { open: boolean; openPicker: () => void; disabled: boolean; profileLabel: string; modelLabel: string; combinedLabel: string }) => ReactNode
  onAgentSelect?: (agent: string) => void | Promise<void>
  onProfileSelect?: (profileId: string) => void | Promise<void>
  onAddProfile: () => void
  onOpenAgentSetup?: (agent?: string) => void
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

const SWARM_AGENT_NAME = 'swarm'

export function profilePickerAgentSections(agents: AgentProfileRecord[], hasProfiles: boolean): Array<{ label: string; agents: AgentProfileRecord[] }> {
  const sections = [
    { label: 'Agents', agents: agents.filter((agent) => agentMode(agent) === 'primary' && agent.name !== SWARM_AGENT_NAME) },
    { label: 'Subagents', agents: agents.filter((agent) => agentMode(agent) === 'subagent') },
    { label: 'Default agent', agents: hasProfiles ? [] : agents.filter((agent) => agent.name === SWARM_AGENT_NAME) },
    { label: 'Other agents', agents: agents.filter((agent) => !['primary', 'subagent'].includes(agentMode(agent)) && agent.name !== SWARM_AGENT_NAME) },
  ]
  return sections.filter((section) => section.agents.length > 0)
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
  renderTrigger,
  onAgentSelect,
  onProfileSelect,
  onAddProfile,
  onOpenAgentSetup,
  onSetDefault,
  onDeleteProfile,
}: ProfileAgentPickerProps) {
  const [open, setOpen] = useState(false)
  const [localError, setLocalError] = useState<string | null>(null)
  const [profileGroup, setProfileGroup] = useState<ModelProfilePolicyGroup>('single')
  const [position, setPosition] = useState<{ bottom: number; left: number; width: number; maxHeight: number } | null>(null)
  const triggerRef = useRef<HTMLButtonElement | null>(null)
  const dropdownRef = useRef<HTMLDivElement | null>(null)
  const pointerRef = useRef<{ x: number; y: number } | null>(null)
  const selectedAgent = agents.find((agent) => agent.name === currentAgent) ?? agents.find((agent) => agent.name === selectedPrimaryAgent)
  const profileGroupSwitchable = canSwitchModelProfilePolicyGroup(selectedAgent)
  const visibleProfiles = modelProfilesInPolicyGroup(profiles, profileGroup)
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
    setProfileGroup(initialModelProfilePolicyGroup(selectedAgent, activeProfile))
  }, [activeProfile, open, selectedAgent])

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

  const openPicker = useCallback(() => {
    pointerRef.current = null
    setOpen((value) => !value)
  }, [])

  const agentSections = profilePickerAgentSections(agents, profiles.length > 0)

  const dropdown = open && position ? createPortal(
    <div ref={dropdownRef} style={{ position: 'fixed', bottom: position.bottom, left: position.left, width: position.width, maxHeight: position.maxHeight, zIndex: 9999 }}>
      <div className="flex max-h-[inherit] min-h-0 flex-col overflow-hidden rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] shadow-xl shadow-black/40">
        <div className="flex shrink-0 items-center justify-between gap-2 border-b border-[var(--app-border)] px-3 py-2.5">
          <span className="text-[11px] font-semibold uppercase tracking-wider text-[var(--app-text-subtle)]">Profiles</span>
          {onOpenAgentSetup ? <button type="button" onClick={() => { setOpen(false); onOpenAgentSetup(selectedAgent?.name) }} className="inline-flex items-center gap-1 rounded-md px-2 py-1 text-[11px] font-semibold text-[var(--app-primary)] hover:bg-[var(--app-surface-hover)]"><Settings2 size={12} />Agent setup</button> : null}
        </div>
        <div className="min-h-0 flex-1 overflow-y-auto">
          <section aria-label="Profiles" className="px-2 py-2">
            <div className="flex items-center justify-between gap-2 px-1 pb-1.5">
              <span className="text-[10px] font-semibold uppercase tracking-wider text-[var(--app-text-subtle)]">{modelProfilePolicyGroupLabel(profileGroup)} profiles</span>
              <button type="button" onClick={() => { setOpen(false); onAddProfile() }} disabled={busy} className="inline-flex items-center gap-1 rounded-md px-2 py-1 text-[11px] font-semibold text-[var(--app-primary)] hover:bg-[var(--app-surface-hover)] disabled:opacity-50"><Plus size={12} />Add profile</button>
            </div>
            {profileGroupSwitchable ? <PolicyGroupSwitch value={profileGroup} onChange={setProfileGroup} /> : null}
            {loading ? <div className="rounded-lg border border-[var(--app-border)] px-3 py-3 text-xs text-[var(--app-text-muted)]">Loading profiles…</div> : visibleProfiles.length === 0 ? (
              <div className="rounded-lg border border-dashed border-[var(--app-border)] px-3 py-4 text-center text-xs text-[var(--app-text-muted)]">No {modelProfilePolicyGroupLabel(profileGroup).toLowerCase()} profiles yet</div>
            ) : (
              <div className="grid gap-2">
                {visibleProfiles.map((profile) => {
                  const selected = activeProfile?.source === 'saved' && activeProfile.profileId === profile.profileId
                  return <div key={profile.profileId} className={`group flex min-w-0 items-center rounded-lg border ${selected ? 'border-[var(--app-primary)] bg-[var(--app-surface-subtle)] shadow-sm' : 'border-[var(--app-border)] bg-[var(--app-surface)] hover:border-[var(--app-border-strong)] hover:bg-[var(--app-surface-hover)]'}`}>
                    <button type="button" disabled={busy || !onProfileSelect} onClick={() => void invoke(() => onProfileSelect?.(profile.profileId), true)} aria-label={`Apply ${profile.name}`} aria-pressed={selected} className="flex min-w-0 flex-1 items-center gap-2 px-3 py-2.5 text-left disabled:opacity-50">
                      <span className="min-w-0 flex-1">
                        <span className="flex min-w-0 items-center gap-1 text-sm font-semibold leading-5 text-[var(--app-text)]"><span className="truncate">{profile.name}</span></span>
                        <span className="mt-1 grid gap-0.5 text-xs leading-4 text-[var(--app-text-subtle)]">
                          {profileLabels(profile).map((label) => <span key={label} className="block truncate">{label}</span>)}
                        </span>
                      </span>
                    </button>
                    <div className="ml-1 flex shrink-0 items-center gap-0.5 pr-1">
                      <button type="button" disabled={busy || profile.isDefault} onClick={() => void invoke(() => onSetDefault(profile.profileId))} aria-label={profile.isDefault ? `${profile.name} is the account default` : `Make ${profile.name} the account default`} aria-pressed={profile.isDefault} title={profile.isDefault ? 'Account default' : 'Make account default'} className={`rounded-md p-1.5 disabled:cursor-default ${profile.isDefault ? 'text-[var(--app-primary)]' : 'text-[var(--app-text-subtle)] hover:bg-[var(--app-surface)] hover:text-[var(--app-primary)] disabled:opacity-50'}`}><Star size={14} fill={profile.isDefault ? 'currentColor' : 'none'} /></button>
                      <button type="button" disabled={busy} onClick={() => { if (window.confirm(`Delete profile “${profile.name}”?`)) void invoke(() => onDeleteProfile(profile.profileId)) }} aria-label={`Delete ${profile.name}`} title="Delete profile" className="rounded-md p-1.5 text-[var(--app-text-subtle)] hover:bg-[var(--app-danger-bg)] hover:text-[var(--app-danger)] disabled:opacity-50"><Trash2 size={14} /></button>
                    </div>
                  </div>
                })}
              </div>
            )}
          </section>
          {agentSections.map((section) => <section key={section.label} aria-label={section.label} className="border-t border-[var(--app-border)] px-2 py-2">
            <div className="px-1 pb-1.5 text-[10px] font-semibold uppercase tracking-wider text-[var(--app-text-subtle)]">{section.label}</div>
            <div className="grid gap-1.5">
              {section.agents.map((agent) => {
                const selected = agent.name === selectedAgent?.name && !activeProfile?.source
                return <button key={agent.name} type="button" disabled={busy || !onAgentSelect} onClick={() => void invoke(() => onAgentSelect?.(agent.name), true)} className={`flex min-w-0 items-center gap-2 rounded-lg border px-2.5 py-2 text-left text-xs ${selected ? 'border-[var(--app-primary)] bg-[var(--app-surface-subtle)] text-[var(--app-text)]' : 'border-[var(--app-border)] text-[var(--app-text-muted)] hover:border-[var(--app-border-strong)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]'} disabled:opacity-50`}>
                  <span className="min-w-0 truncate font-semibold">{displayAgentName(agent.name)}</span>
                </button>
              })}
            </div>
          </section>)}
        </div>
        {error || localError ? <div role="alert" className="shrink-0 border-t border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-3 py-2 text-xs text-[var(--app-danger)]">{localError || error}</div> : null}
      </div>
    </div>, document.body) : null

  return <div className="inline-flex min-w-0 items-center">
    <div ref={renderTrigger ? (node) => { triggerRef.current = node?.querySelector('button[aria-haspopup="menu"]') ?? node?.querySelector('button') ?? null } : undefined} className="contents">
      {renderTrigger ? renderTrigger({ ...triggerDisplay, open, openPicker, disabled }) : (
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
      )}
    </div>
    {dropdown}
  </div>
}

function PolicyGroupSwitch({ value, onChange }: { value: ModelProfilePolicyGroup; onChange: (value: ModelProfilePolicyGroup) => void }) {
  return <div role="group" aria-label="Profile policy type" className="mb-2 grid grid-cols-2 gap-1 rounded-lg border border-[var(--app-border)] p-1">
    {(['split', 'single'] as const).map((group) => <button key={group} type="button" aria-pressed={value === group} onClick={() => onChange(group)} className={`rounded-md px-2 py-1.5 text-[11px] font-semibold transition ${value === group ? 'bg-[var(--app-surface-subtle)] text-[var(--app-primary)]' : 'text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]'}`}>{modelProfilePolicyGroupLabel(group)}</button>)}
  </div>
}
