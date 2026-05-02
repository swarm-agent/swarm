import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { Bot, ChevronDown, Check } from 'lucide-react'
import type { AgentProfileRecord } from '../types/chat'

interface AgentPickerProps {
  currentAgent: string
  selectedPrimaryAgent: string
  agents: AgentProfileRecord[]
  onSelect: (agent: string) => void
  dropdownAlign?: 'left' | 'right'
}

const DROPDOWN_VIEWPORT_GUTTER = 8

export function AgentPicker({ currentAgent, selectedPrimaryAgent, agents, onSelect, dropdownAlign = 'right' }: AgentPickerProps) {
  const [open, setOpen] = useState(false)
  const triggerRef = useRef<HTMLButtonElement | null>(null)
  const dropdownRef = useRef<HTMLDivElement | null>(null)
  const [position, setPosition] = useState<{ top: number; left?: number; right?: number; minWidth: number; maxWidth: number } | null>(null)

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
  const selectedProfile = agents.find((agent) => agent.name === selectedPrimaryAgent)
  const displayLabel = currentAgent || selectedProfile?.name || selectedPrimaryAgent

  const updatePosition = useCallback(() => {
    if (!triggerRef.current || typeof window === 'undefined') {
      setPosition(null)
      return
    }

    const rect = triggerRef.current.getBoundingClientRect()
    const maxWidth = Math.max(160, window.innerWidth - DROPDOWN_VIEWPORT_GUTTER * 2)

    setPosition({
      top: rect.top - 8,
      left: dropdownAlign === 'left'
        ? Math.min(Math.max(DROPDOWN_VIEWPORT_GUTTER, rect.left), Math.max(DROPDOWN_VIEWPORT_GUTTER, window.innerWidth - maxWidth - DROPDOWN_VIEWPORT_GUTTER))
        : undefined,
      right: dropdownAlign === 'right' ? Math.max(DROPDOWN_VIEWPORT_GUTTER, window.innerWidth - rect.right) : undefined,
      minWidth: Math.min(rect.width, maxWidth),
      maxWidth,
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
    function handleClickOutside(event: MouseEvent) {
      const target = event.target as Node
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
    document.addEventListener('mousedown', handleClickOutside)
    document.addEventListener('keydown', handleEscape)
    return () => {
      document.removeEventListener('mousedown', handleClickOutside)
      document.removeEventListener('keydown', handleEscape)
    }
  }, [open])

  const handleSelect = useCallback((value: string) => {
    onSelect(value)
    setOpen(false)
  }, [onSelect])

  const dropdown = open && position ? createPortal(
    <div
      ref={dropdownRef}
      style={{
        position: 'fixed',
        bottom: `${window.innerHeight - position.top}px`,
        left: position.left === undefined ? undefined : `${position.left}px`,
        right: position.right === undefined ? undefined : `${position.right}px`,
        minWidth: `${position.minWidth}px`,
        maxWidth: `${position.maxWidth}px`,
        zIndex: 9999,
      }}
      className="w-max max-w-[calc(100vw-16px)]"
    >
      <div className="w-max max-w-full overflow-hidden rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] shadow-xl shadow-black/40">
        <div className="flex flex-col">
          <div className="border-b border-[var(--app-border)] px-3 py-2">
            <span className="text-[11px] font-semibold uppercase tracking-wider text-[var(--app-text-subtle)]">
              Select Agent
            </span>
          </div>
          <div className="max-h-[240px] overflow-y-auto py-1">
            {agents.map((profile) => {
              const isSelected = profile.name === selectedPrimaryAgent
              return (
                <button
                  key={profile.name}
                  type="button"
                  onClick={() => handleSelect(profile.name)}
                  className={`flex w-full items-center gap-2 px-3 py-2 text-left text-sm transition ${
                    isSelected
                      ? 'bg-[var(--app-surface-subtle)] text-[var(--app-text)]'
                      : 'text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]'
                  }`}
                >
                  {isSelected ? (
                    <Check size={14} className="shrink-0 text-[var(--app-primary)]" />
                  ) : (
                    <span className="w-[14px] shrink-0" />
                  )}
                  <Bot size={14} className="shrink-0 text-[var(--app-text-subtle)]" />
                  <span className="min-w-0 flex-1 truncate">{profileLabel(profile)}</span>
                  <span className="shrink-0 text-[10px] uppercase tracking-wide text-[var(--app-text-subtle)]">
                    {profileModeLabel(profile)}
                  </span>
                </button>
              )
            })}
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
        className="inline-flex items-center gap-1 text-[11px] font-medium text-[var(--app-text-muted)] transition hover:text-[var(--app-text)]"
      >
        <Bot size={13} className="shrink-0 text-[var(--app-text-subtle)]" />
        <span className="max-w-[100px] truncate">{displayLabel}</span>
        <ChevronDown size={12} className={open ? 'rotate-180 transition-transform' : 'transition-transform'} />
      </button>
      {dropdown}
    </div>
  )
}
