import { useCallback, useEffect, useLayoutEffect, useRef, useState, type MouseEvent as ReactMouseEvent } from 'react'
import { createPortal } from 'react-dom'
import { ChevronDown, Check, Settings2 } from 'lucide-react'
import type { DesktopSessionMode } from '../../settings/swarm/types/swarm-settings'
import type { AgentProfileRecord } from '../types/chat'
import { displayAgentName } from '../services/agent-display'

interface AgentPickerProps {
  currentAgent: string
  selectedPrimaryAgent: string
  agents: AgentProfileRecord[]
  mode?: DesktopSessionMode
  onSelect: (agent: string) => void | Promise<void>
  onOpenSettings?: (agent: string) => void
  disabled?: boolean
  triggerClassName?: string
}

const DROPDOWN_VIEWPORT_GUTTER = 8
const MOBILE_DROPDOWN_BREAKPOINT = 640
const DESKTOP_DROPDOWN_WIDTH = 480

export function AgentPicker({ currentAgent, selectedPrimaryAgent, agents, mode = 'auto', onSelect, onOpenSettings, disabled = false, triggerClassName = '' }: AgentPickerProps) {
  const [open, setOpen] = useState(false)
  const triggerRef = useRef<HTMLButtonElement | null>(null)
  const dropdownRef = useRef<HTMLDivElement | null>(null)
  const pointerAnchorRef = useRef<{ x: number; y: number } | null>(null)
  const [position, setPosition] = useState<{ top?: number; bottom?: number; left?: number; right?: number; minWidth: number; width?: number; maxWidth: number; maxHeight: number } | null>(null)

  const profileLabel = (profile: AgentProfileRecord) => displayAgentName(profile.name)
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
  const displayLabel = currentProfile ? profileLabel(currentProfile) : displayAgentName(currentAgent || selectedPrimaryAgent) || 'Agent'
  const settingLabel = (provider: string, model: string, thinking: string, serviceTier: string) => {
    const normalizedServiceTier = serviceTier.trim().toLowerCase()
    const priorityActive = Boolean(normalizedServiceTier && !['standard', 'default', 'off', 'none'].includes(normalizedServiceTier))
    return [
      [provider.trim(), model.trim()].filter(Boolean).join('/') || 'Default model',
      thinking.trim() || 'off',
      priorityActive ? serviceTier.trim() : '',
    ].filter(Boolean).join(' · ')
  }
  const activeProfileModelLabel = (profile: AgentProfileRecord) => {
    if (profile.modelMode !== 'split') {
      return settingLabel(profile.provider, profile.model, profile.thinking, profile.autoServiceTier)
    }
    return mode === 'plan'
      ? settingLabel(profile.planProvider, profile.planModel, profile.planThinking, profile.planServiceTier)
      : settingLabel(profile.autoProvider, profile.autoModel, profile.autoThinking, profile.autoServiceTier)
  }
  const profileModelLabels = (profile: AgentProfileRecord) => profile.modelMode === 'split'
    ? [
        `Plan ${settingLabel(profile.planProvider, profile.planModel, profile.planThinking, profile.planServiceTier)}`,
        `Auto ${settingLabel(profile.autoProvider, profile.autoModel, profile.autoThinking, profile.autoServiceTier)}`,
      ]
    : [activeProfileModelLabel(profile)]
  const selectedAgentDetail = currentProfile ? activeProfileModelLabel(currentProfile) : ''

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
    const pointerAnchor = pointerAnchorRef.current
    const anchorX = pointerAnchor?.x ?? rect.left + rect.width / 2
    const anchorY = pointerAnchor?.y ?? rect.top

    if (mobile) {
      setPosition({
        bottom: Math.max(DROPDOWN_VIEWPORT_GUTTER, viewportHeight - anchorY + DROPDOWN_VIEWPORT_GUTTER),
        left: DROPDOWN_VIEWPORT_GUTTER,
        minWidth: maxWidth,
        width: maxWidth,
        maxWidth,
        maxHeight: Math.max(0, Math.min(320, anchorY - DROPDOWN_VIEWPORT_GUTTER * 2)),
      })
      return
    }

    const width = Math.min(Math.max(300, rect.width), DESKTOP_DROPDOWN_WIDTH, maxWidth)
    const left = Math.min(
      Math.max(DROPDOWN_VIEWPORT_GUTTER, anchorX - width / 2),
      viewportWidth - width - DROPDOWN_VIEWPORT_GUTTER,
    )

    setPosition({
      bottom: Math.max(DROPDOWN_VIEWPORT_GUTTER, viewportHeight - anchorY + DROPDOWN_VIEWPORT_GUTTER),
      left,
      minWidth: width,
      width,
      maxWidth,
      maxHeight: Math.max(0, Math.min(320, anchorY - DROPDOWN_VIEWPORT_GUTTER * 2)),
    })
  }, [])

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

  const handleTriggerClick = useCallback((event: ReactMouseEvent<HTMLButtonElement>) => {
    setOpen((wasOpen) => {
      if (wasOpen) {
        pointerAnchorRef.current = null
        return false
      }
      pointerAnchorRef.current = event.detail > 0
        ? { x: event.clientX, y: event.clientY }
        : null
      return true
    })
  }, [])

  const handleSelect = useCallback(async (value: string) => {
    await onSelect(value)
    pointerAnchorRef.current = null
    setOpen(false)
  }, [onSelect])

  const handleOpenSettings = useCallback((value: string) => {
    pointerAnchorRef.current = null
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
              <section
                key={section.label}
                aria-label={`${section.label} agents`}
                className={`px-2 py-2 ${
                  sectionIndex === 0 ? '' : 'border-t border-[var(--app-border)]'
                }`}
              >
                <div className="px-1 pb-1.5 text-[10px] font-semibold uppercase tracking-wider text-[var(--app-text-subtle)]">
                  {section.label}
                </div>
                <div className="w-full overflow-hidden rounded-lg border border-[var(--app-border)]">
                  {section.profiles.map((profile, profileIndex) => {
                    const isSelected = profile.name === selectedAgentName
                    return (
                      <div
                        key={profile.name}
                        className={`group flex w-full items-stretch transition ${
                          profileIndex === 0 ? '' : 'border-t border-[var(--app-border)]'
                        } ${
                          isSelected
                            ? 'bg-[var(--app-surface-subtle)] text-[var(--app-text)]'
                            : 'text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]'
                        }`}
                      >
                        <button type="button" onClick={() => handleSelect(profile.name)} className="flex min-w-0 flex-1 items-start gap-2.5 px-3 py-2.5 text-left text-sm" aria-label={`Switch to ${profileLabel(profile)}`}>
                          {isSelected ? (
                            <Check size={14} className="mt-0.5 shrink-0 text-[var(--app-primary)]" />
                          ) : (
                            <span className="w-[14px] shrink-0" />
                          )}
                          <span className="min-w-0 flex-1">
                            <span className="block break-words font-medium leading-5 text-[var(--app-text)]">{profileLabel(profile)}</span>
                            <span className="mt-0.5 block break-words text-[10px] leading-4 text-[var(--app-text-subtle)]">
                              {profileModelLabels(profile).map((label) => (
                                <span key={label} className="block">{label}</span>
                              ))}
                            </span>
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
              </section>
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
        onClick={handleTriggerClick}
        disabled={disabled}
        aria-expanded={open}
        aria-haspopup="menu"
        aria-label={`Agent: ${displayLabel}${selectedAgentDetail ? `, ${selectedAgentDetail}` : ''}`}
        className={`inline-flex min-h-9 min-w-0 items-center gap-2 rounded-lg border-0 bg-transparent px-3 py-2 text-xs font-medium text-[var(--app-text-muted)] transition-all hover:-translate-y-0.5 hover:text-[var(--app-text)] hover:shadow-sm disabled:cursor-not-allowed disabled:opacity-50 disabled:hover:translate-y-0 disabled:hover:shadow-none ${triggerClassName}`}
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
