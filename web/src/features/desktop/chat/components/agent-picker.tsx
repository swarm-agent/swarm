import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { ChevronDown, Check, Settings2 } from 'lucide-react'
import type { DesktopSessionMode } from '../../settings/swarm/types/swarm-settings'
import type { AgentProfileRecord } from '../types/chat'

interface AgentPickerProps {
  currentAgent: string
  selectedPrimaryAgent: string
  agents: AgentProfileRecord[]
  mode?: DesktopSessionMode
  onSelect: (agent: string) => void | Promise<void>
  onOpenSettings?: (agent: string) => void
  disabled?: boolean
  dropdownAlign?: 'left' | 'right'
  triggerClassName?: string
}

const DROPDOWN_VIEWPORT_GUTTER = 8
const MOBILE_DROPDOWN_BREAKPOINT = 640

export function AgentPicker({ currentAgent, selectedPrimaryAgent, agents, mode = 'auto', onSelect, onOpenSettings, disabled = false, dropdownAlign = 'right', triggerClassName = '' }: AgentPickerProps) {
  const [open, setOpen] = useState(false)
  const triggerRef = useRef<HTMLButtonElement | null>(null)
  const dropdownRef = useRef<HTMLDivElement | null>(null)
  const [position, setPosition] = useState<{ top?: number; bottom?: number; left?: number; right?: number; minWidth: number; width?: number; maxWidth: number; maxHeight: number } | null>(null)

  const profileLabel = (profile: AgentProfileRecord) => profile.name === 'swarm' ? 'Swarm' : profile.name
  const profileModeLabel = (profile: AgentProfileRecord) => {
    switch (profile.mode) {
      case 'primary':
        return 'Primary'
      case 'subagent':
        return 'Subagent'
      case 'background':
        return 'Background'
      default:
        return profile.mode || 'Agent'
    }
  }
  const agentMode = (profile: AgentProfileRecord) => (profile.mode || 'primary').trim().toLowerCase()
  const primaryAgents = agents.filter((profile) => agentMode(profile) === 'primary')
  const subagentAgents = agents.filter((profile) => agentMode(profile) === 'subagent')
  const otherAgents = agents.filter((profile) => {
    const mode = agentMode(profile)
    return mode !== 'primary' && mode !== 'subagent'
  })
  const selectedProfile = agents.find((agent) => agent.name === selectedPrimaryAgent)
  const currentProfile = agents.find((agent) => agent.name === currentAgent) ?? selectedProfile
  const selectedAgentName = currentProfile?.name || currentAgent || selectedPrimaryAgent
  const displayLabel = currentProfile ? profileLabel(currentProfile) : (currentAgent === 'swarm' ? 'Swarm' : currentAgent || selectedPrimaryAgent || 'Agent')
  const settingLabel = (provider: string, model: string, thinking: string, serviceTier: string, decorate = true) => {
    const normalizedServiceTier = serviceTier.trim().toLowerCase()
    const priorityActive = Boolean(normalizedServiceTier && !['standard', 'default', 'off', 'none'].includes(normalizedServiceTier))
    return [
      [provider.trim(), model.trim()].filter(Boolean).join('/') || 'Default model',
      `${decorate ? '💡 ' : ''}${thinking.trim() || 'off'}`,
      priorityActive ? `${decorate ? '⚡ ' : ''}${serviceTier.trim()}` : '',
    ].filter(Boolean).join(' · ')
  }
  const activeProfileModelLabel = (profile: AgentProfileRecord, decorate = true) => {
    if (profile.modelMode !== 'split') {
      return settingLabel(profile.provider, profile.model, profile.thinking, profile.autoServiceTier, decorate)
    }
    return mode === 'plan'
      ? settingLabel(profile.planProvider, profile.planModel, profile.planThinking, profile.planServiceTier, decorate)
      : settingLabel(profile.autoProvider, profile.autoModel, profile.autoThinking, profile.autoServiceTier, decorate)
  }
  const profileModelLabel = (profile: AgentProfileRecord) => profile.modelMode === 'split'
    ? [
        `Plan ${settingLabel(profile.planProvider, profile.planModel, profile.planThinking, profile.planServiceTier)}`,
        `Auto ${settingLabel(profile.autoProvider, profile.autoModel, profile.autoThinking, profile.autoServiceTier)}`,
      ].join(' · ')
    : activeProfileModelLabel(profile)
  const selectedAgentDetail = currentProfile ? activeProfileModelLabel(currentProfile, false) : ''

  const updatePosition = useCallback(() => {
    if (!triggerRef.current || typeof window === 'undefined') {
      setPosition(null)
      return
    }

    const rect = triggerRef.current.getBoundingClientRect()
    const viewportWidth = window.innerWidth
    const viewportHeight = window.visualViewport?.height ?? window.innerHeight
    const mobile = viewportWidth < MOBILE_DROPDOWN_BREAKPOINT
    const maxWidth = Math.max(160, viewportWidth - DROPDOWN_VIEWPORT_GUTTER * 2)

    if (mobile) {
      const top = Math.min(rect.bottom + DROPDOWN_VIEWPORT_GUTTER, viewportHeight - 140)
      setPosition({
        top,
        left: DROPDOWN_VIEWPORT_GUTTER,
        minWidth: Math.min(rect.width, maxWidth),
        width: maxWidth,
        maxWidth,
        maxHeight: Math.max(120, viewportHeight - top - DROPDOWN_VIEWPORT_GUTTER),
      })
      return
    }

    setPosition({
      bottom: Math.max(DROPDOWN_VIEWPORT_GUTTER, viewportHeight - rect.top + DROPDOWN_VIEWPORT_GUTTER),
      left: dropdownAlign === 'left'
        ? Math.min(Math.max(DROPDOWN_VIEWPORT_GUTTER, rect.left), Math.max(DROPDOWN_VIEWPORT_GUTTER, viewportWidth - maxWidth - DROPDOWN_VIEWPORT_GUTTER))
        : undefined,
      right: dropdownAlign === 'right' ? Math.max(DROPDOWN_VIEWPORT_GUTTER, viewportWidth - rect.right) : undefined,
      minWidth: Math.min(rect.width, maxWidth),
      maxWidth,
      maxHeight: Math.max(180, Math.min(320, rect.top - DROPDOWN_VIEWPORT_GUTTER * 2)),
    })
  }, [dropdownAlign])

  useLayoutEffect(() => {
    if (!open || !triggerRef.current) {
      setPosition(null)
      return
    }
    updatePosition()
  }, [open, updatePosition])

  useEffect(() => {
    if (!open) return
    window.addEventListener('scroll', updatePosition, true)
    window.addEventListener('resize', updatePosition)
    return () => {
      window.removeEventListener('scroll', updatePosition, true)
      window.removeEventListener('resize', updatePosition)
    }
  }, [open, updatePosition])

  useEffect(() => {
    if (!open) return
    function handlePointerDownOutside(event: PointerEvent) {
      const target = event.target as Node | null
      if (!target || !target.isConnected || !document.body.contains(target)) return
      if (
        triggerRef.current?.contains(target) ||
        dropdownRef.current?.contains(target)
      ) {
        return
      }
      setOpen(false)
    }
    function handleEscape(event: KeyboardEvent) {
      if (event.key === 'Escape') {
        setOpen(false)
      }
    }
    document.addEventListener('pointerdown', handlePointerDownOutside)
    document.addEventListener('keydown', handleEscape)
    return () => {
      document.removeEventListener('pointerdown', handlePointerDownOutside)
      document.removeEventListener('keydown', handleEscape)
    }
  }, [open])

  const handleSelect = useCallback(async (value: string) => {
    await onSelect(value)
    setOpen(false)
  }, [onSelect])

  const handleOpenSettings = useCallback((value: string) => {
    setOpen(false)
    onOpenSettings?.(value)
  }, [onOpenSettings])

  const dropdown = open && position ? createPortal(
    <div
      ref={dropdownRef}
      style={{
        position: 'fixed',
        top: position.top === undefined ? undefined : `${position.top}px`,
        bottom: position.bottom === undefined ? undefined : `${position.bottom}px`,
        left: position.left === undefined ? undefined : `${position.left}px`,
        right: position.right === undefined ? undefined : `${position.right}px`,
        minWidth: `${position.minWidth}px`,
        width: position.width === undefined ? undefined : `${position.width}px`,
        maxWidth: `${position.maxWidth}px`,
        maxHeight: `${position.maxHeight}px`,
        zIndex: 9999,
      }}
      className="w-[calc(100vw-16px)] max-w-[calc(100vw-16px)] sm:w-max"
    >
      <div className="max-w-full overflow-hidden rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] shadow-xl shadow-black/40" style={{ maxHeight: `${position.maxHeight}px` }}>
        <div className="flex max-h-[inherit] min-h-0 flex-col">
          <div className="shrink-0 border-b border-[var(--app-border)] px-3 py-2.5">
            <span className="text-[11px] font-semibold uppercase tracking-wider text-[var(--app-text-subtle)]">Select agent</span>
          </div>
          <div className="min-h-0 flex-1 overflow-y-auto py-1">
            {[
              { label: 'Primary', profiles: primaryAgents },
              { label: 'Subagent', profiles: subagentAgents },
              { label: 'Other', profiles: otherAgents },
            ].filter((section) => section.profiles.length > 0).map((section, sectionIndex) => (
              <div
                key={section.label}
                className={sectionIndex === 0 ? '' : 'mt-1 border-t border-[var(--app-border)] pt-1'}
              >
                <div className="px-3 pb-1 pt-2 text-[10px] font-semibold uppercase tracking-wider text-[var(--app-text-subtle)]">
                  {section.label}
                </div>
                {section.profiles.map((profile) => {
                  const isSelected = profile.name === selectedAgentName
                  return (
                    <div
                      key={profile.name}
                      className={`group flex w-full items-stretch transition ${
                        isSelected
                          ? 'bg-[var(--app-surface-subtle)] text-[var(--app-text)]'
                          : 'text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]'
                      }`}
                    >
                      <button type="button" onClick={() => handleSelect(profile.name)} className="flex min-w-0 flex-1 items-start gap-2 px-3 py-2.5 text-left text-sm" aria-label={`Switch to ${profileLabel(profile)}`}>
                        {isSelected ? (
                          <Check size={14} className="shrink-0 text-[var(--app-primary)]" />
                        ) : (
                          <span className="w-[14px] shrink-0" />
                        )}
                        <span className="min-w-0 flex-1">
                          <span className="block break-words font-medium leading-snug">{profileLabel(profile)}</span>
                          <span className="mt-0.5 block max-w-[20rem] truncate text-[10px] text-[var(--app-text-subtle)] [font-variant-emoji:text]">{profileModelLabel(profile)}</span>
                        </span>
                        <span className="mt-0.5 shrink-0 text-[10px] uppercase tracking-wide text-[var(--app-text-subtle)]">
                          {profileModeLabel(profile)}
                        </span>
                      </button>
                      {onOpenSettings ? (
                        <button
                          type="button"
                          onClick={() => handleOpenSettings(profile.name)}
                          aria-label={`Open settings for ${profileLabel(profile)}`}
                          title={`Open ${profileLabel(profile)} settings`}
                          className="m-1.5 inline-flex w-8 shrink-0 items-center justify-center rounded-md text-[var(--app-text-subtle)] opacity-70 transition hover:bg-[var(--app-surface)] hover:text-[var(--app-text)] focus-visible:opacity-100 sm:opacity-0 sm:group-hover:opacity-100 sm:group-focus-within:opacity-100"
                        >
                          <Settings2 size={14} />
                        </button>
                      ) : null}
                    </div>
                  )
                })}
              </div>
            ))}
          </div>
        </div>
      </div>
    </div>,
    document.body,
  ) : null

  return (
    <div className="inline-flex min-w-0 items-center">
      <button
        ref={triggerRef}
        type="button"
        onClick={() => setOpen((prev) => !prev)}
        disabled={disabled}
        aria-expanded={open}
        aria-haspopup="menu"
        aria-label={`Agent: ${displayLabel}${selectedAgentDetail ? `, ${selectedAgentDetail}` : ''}`}
        className={`inline-flex min-h-9 min-w-0 items-center gap-2 rounded-none border-0 border-b-2 border-transparent bg-transparent px-3 py-2 text-xs font-medium text-[var(--app-text-muted)] transition hover:border-[var(--app-border-accent)] hover:text-[var(--app-text)] disabled:cursor-not-allowed disabled:opacity-50 ${triggerClassName}`}
      >
        <span className="max-w-[100px] shrink-0 truncate font-semibold text-[var(--app-text)]">{displayLabel}</span>
        {selectedAgentDetail ? (
          <>
            <span aria-hidden="true" className="shrink-0 text-[var(--app-text-subtle)]">·</span>
            <span data-testid="selected-agent-detail" className="min-w-0 max-w-[320px] truncate text-[11px] font-normal text-[var(--app-text-subtle)]">
              {selectedAgentDetail}
            </span>
          </>
        ) : null}
        <ChevronDown size={14} className={`shrink-0 ${open ? 'rotate-180 transition-transform' : 'transition-transform'}`} />
      </button>
      {dropdown}
    </div>
  )
}
