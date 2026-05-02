import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { Check, ChevronDown, Monitor, Server } from 'lucide-react'
import type { DesktopChatRoute } from '../services/chat-routing'

interface RoutePickerProps {
  currentRoute: DesktopChatRoute
  routes: DesktopChatRoute[]
  onSelect: (routeId: string) => void
  disabled?: boolean
  title?: string
}

const VIEWPORT_GUTTER = 8
const MIN_DROPDOWN_WIDTH = 220
const MAX_DROPDOWN_WIDTH = 320

function RouteIcon({ route, className }: { route: DesktopChatRoute; className?: string }) {
  const Icon = route.swarmId ? Server : Monitor
  return <Icon size={14} className={className} />
}

function routeCaption(route: DesktopChatRoute): string {
  return route.swarmId ? 'Linked swarm' : 'Host machine'
}

export function RoutePicker({ currentRoute, routes, onSelect, disabled = false, title }: RoutePickerProps) {
  const [open, setOpen] = useState(false)
  const triggerRef = useRef<HTMLButtonElement | null>(null)
  const dropdownRef = useRef<HTMLDivElement | null>(null)
  const [position, setPosition] = useState<{ top: number; left: number; width: number } | null>(null)

  const selectedRoute = useMemo(
    () => routes.find((route) => route.id === currentRoute.id) ?? currentRoute,
    [currentRoute, routes],
  )

  const updatePosition = useCallback(() => {
    if (!triggerRef.current || typeof window === 'undefined') {
      setPosition(null)
      return
    }

    const rect = triggerRef.current.getBoundingClientRect()
    const maxWidth = Math.max(MIN_DROPDOWN_WIDTH, Math.min(MAX_DROPDOWN_WIDTH, window.innerWidth - VIEWPORT_GUTTER * 2))
    const width = Math.min(Math.max(rect.width, MIN_DROPDOWN_WIDTH), maxWidth)

    setPosition({
      top: rect.top - 8,
      left: Math.max(VIEWPORT_GUTTER, Math.min(rect.left, window.innerWidth - VIEWPORT_GUTTER - width)),
      width,
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
    if (!open) {
      return
    }
    window.addEventListener('scroll', updatePosition, true)
    window.addEventListener('resize', updatePosition)
    return () => {
      window.removeEventListener('scroll', updatePosition, true)
      window.removeEventListener('resize', updatePosition)
    }
  }, [open, updatePosition])

  useEffect(() => {
    if (!open) {
      return
    }
    function handleClickOutside(event: MouseEvent) {
      const target = event.target as Node
      if (triggerRef.current?.contains(target) || dropdownRef.current?.contains(target)) {
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

  const handleSelect = useCallback((routeId: string) => {
    onSelect(routeId)
    setOpen(false)
  }, [onSelect])

  const dropdown = open && position ? createPortal(
    <div
      ref={dropdownRef}
      style={{
        position: 'fixed',
        bottom: `${window.innerHeight - position.top}px`,
        left: `${position.left}px`,
        width: `${position.width}px`,
        zIndex: 9999,
      }}
    >
      <div className="overflow-hidden rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)] shadow-xl shadow-black/40">
        <div className="border-b border-[var(--app-border)] px-3 py-2.5">
          <span className="text-[11px] font-semibold uppercase tracking-wider text-[var(--app-text-subtle)]">
            Route chat to
          </span>
        </div>
        <div className="py-1">
          {routes.map((route) => {
            const isSelected = route.id === selectedRoute.id
            return (
              <button
                key={route.id}
                type="button"
                onClick={() => handleSelect(route.id)}
                className={isSelected
                  ? 'flex w-full items-center gap-3 bg-[var(--app-surface-subtle)] px-3 py-2.5 text-left text-[var(--app-text)] transition'
                  : 'flex w-full items-center gap-3 px-3 py-2.5 text-left text-[var(--app-text-muted)] transition hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]'}
              >
                <RouteIcon route={route} className="mt-0.5 shrink-0 text-[var(--app-text-subtle)]" />
                <span className="min-w-0 flex-1">
                  <span className="block truncate text-sm font-medium">{route.label}</span>
                  <span className="mt-0.5 block truncate text-[11px] text-[var(--app-text-subtle)]">{routeCaption(route)}</span>
                </span>
                {isSelected ? <Check size={14} className="shrink-0 text-[var(--app-primary)]" /> : null}
              </button>
            )
          })}
        </div>
      </div>
    </div>,
    document.body,
  ) : null

  return (
    <div className="inline-flex min-w-0 max-w-full items-center">
      <button
        ref={triggerRef}
        type="button"
        onClick={() => setOpen((prev) => !prev)}
        disabled={disabled}
        title={title}
        aria-haspopup="menu"
        aria-expanded={open}
        className="inline-flex h-10 min-w-0 max-w-full sm:max-w-[220px] items-center gap-1.5 sm:gap-2 rounded-xl border border-[var(--app-border)] bg-[var(--app-bg-alt)] px-2 sm:px-3 text-sm font-medium text-[var(--app-text)] transition hover:border-[var(--app-border-strong)] hover:bg-[var(--app-surface-hover)] disabled:cursor-not-allowed disabled:opacity-60"
      >
        <span className="flex min-w-0 flex-1 items-center gap-1.5 sm:gap-2">
          <RouteIcon route={selectedRoute} className="shrink-0 text-[var(--app-text-subtle)]" />
          <span className="truncate min-w-0 w-full text-left">{selectedRoute.label}</span>
        </span>
        <ChevronDown size={14} className={open ? 'shrink-0 text-[var(--app-text-subtle)] transition-transform rotate-180' : 'shrink-0 text-[var(--app-text-subtle)] transition-transform'} />
      </button>
      {dropdown}
    </div>
  )
}
