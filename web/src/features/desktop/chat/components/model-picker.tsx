import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { ChevronDown, Check, Cpu, Star } from 'lucide-react'
import type { ModelOptionRecord } from '../types/chat'
import { displayModelName, formatContextWindow, effectiveContextWindow } from '../services/model-options'

interface ModelPickerProps {
  options: ModelOptionRecord[]
  selectedKey: string
  onSelect: (key: string) => void
  openSignal?: number
  disabled?: boolean
  disabledReason?: string
}

const DROPDOWN_WIDTH = 640
const MOBILE_DROPDOWN_BREAKPOINT = 640
const DROPDOWN_VIEWPORT_GUTTER = 8

export function ModelPicker({ options, selectedKey, onSelect, openSignal = 0, disabled = false, disabledReason = '' }: ModelPickerProps) {
  const [open, setOpen] = useState(false)
  const triggerRef = useRef<HTMLButtonElement | null>(null)
  const dropdownRef = useRef<HTMLDivElement | null>(null)
  const [position, setPosition] = useState<{ top?: number; bottom?: number; left: number; width: number; maxHeight: number } | null>(null)
  const [activeProvider, setActiveProvider] = useState<string>('')
  const [activeModelIndex, setActiveModelIndex] = useState(0)

  const providers = useMemo(() => {
    const groups = new Map<string, ModelOptionRecord[]>()
    for (const option of options) {
      const list = groups.get(option.provider) ?? []
      list.push(option)
      groups.set(option.provider, list)
    }
    return Array.from(groups.entries())
  }, [options])

  const selectedOption = useMemo(
    () => options.find((option) => option.key === selectedKey) ?? null,
    [options, selectedKey],
  )

  const providerIDs = useMemo(() => providers.map(([provider]) => provider), [providers])

  const resolvedActiveProvider = useMemo(() => {
    if (activeProvider && providerIDs.includes(activeProvider)) {
      return activeProvider
    }
    if (selectedOption && providerIDs.includes(selectedOption.provider)) {
      return selectedOption.provider
    }
    return providerIDs[0] ?? ''
  }, [activeProvider, providerIDs, selectedOption])

  const activeModels = useMemo(
    () => providers.find(([provider]) => provider === resolvedActiveProvider)?.[1] ?? [],
    [providers, resolvedActiveProvider],
  )

  const displayLabel = selectedOption
    ? `${selectedOption.provider}/${displayModelName(selectedOption.provider, selectedOption.model, selectedOption.contextMode)}`
    : 'Select model'

  const updatePosition = useCallback(() => {
    if (!triggerRef.current || typeof window === 'undefined') {
      setPosition(null)
      return
    }
    const rect = triggerRef.current.getBoundingClientRect()
    const viewportWidth = window.innerWidth
    const viewportHeight = window.visualViewport?.height ?? window.innerHeight
    const mobile = viewportWidth < MOBILE_DROPDOWN_BREAKPOINT
    const width = mobile ? viewportWidth - DROPDOWN_VIEWPORT_GUTTER * 2 : Math.min(DROPDOWN_WIDTH, viewportWidth - DROPDOWN_VIEWPORT_GUTTER * 2)
    const left = mobile
      ? DROPDOWN_VIEWPORT_GUTTER
      : Math.min(Math.max(DROPDOWN_VIEWPORT_GUTTER, rect.left), Math.max(DROPDOWN_VIEWPORT_GUTTER, viewportWidth - width - DROPDOWN_VIEWPORT_GUTTER))

    if (mobile) {
      const top = Math.min(rect.bottom + DROPDOWN_VIEWPORT_GUTTER, viewportHeight - 160)
      setPosition({
        top,
        left,
        width,
        maxHeight: Math.max(120, viewportHeight - top - DROPDOWN_VIEWPORT_GUTTER),
      })
      return
    }

    setPosition({
      bottom: Math.max(DROPDOWN_VIEWPORT_GUTTER, viewportHeight - rect.top + DROPDOWN_VIEWPORT_GUTTER),
      left,
      width,
      maxHeight: Math.max(260, Math.min(420, rect.top - DROPDOWN_VIEWPORT_GUTTER * 2)),
    })
  }, [])

  useLayoutEffect(() => {
    if (!open) {
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
    setActiveProvider((current) => {
      if (current && providerIDs.includes(current)) {
        return current
      }
      if (selectedOption && providerIDs.includes(selectedOption.provider)) {
        return selectedOption.provider
      }
      return providerIDs[0] ?? ''
    })
  }, [open, providerIDs, selectedOption])

  useEffect(() => {
    if (!open) {
      return
    }
    const selectedIndex = activeModels.findIndex((option) => option.key === selectedKey)
    if (selectedIndex >= 0) {
      setActiveModelIndex(selectedIndex)
      return
    }
    setActiveModelIndex(0)
  }, [activeModels, open, selectedKey])

  useEffect(() => {
    if (openSignal <= 0) {
      return
    }
    if (!disabled) {
      setOpen(true)
    }
  }, [disabled, openSignal])

  useEffect(() => {
    if (!open) {
      return
    }
    function handlePointerDownOutside(event: PointerEvent) {
      const target = event.target as Node | null
      if (!target || !target.isConnected || !document.body.contains(target)) return
      if (triggerRef.current?.contains(target) || dropdownRef.current?.contains(target)) {
        return
      }
      setOpen(false)
    }
    function handleKeyDown(event: KeyboardEvent) {
      if (event.key === 'Escape') {
        event.preventDefault()
        setOpen(false)
        return
      }
      if (providerIDs.length === 0) {
        return
      }
      const providerIndex = Math.max(0, providerIDs.indexOf(resolvedActiveProvider))
      if (event.key === 'ArrowLeft') {
        event.preventDefault()
        const nextProvider = providerIDs[Math.max(0, providerIndex - 1)]
        if (nextProvider) {
          setActiveProvider(nextProvider)
        }
        return
      }
      if (event.key === 'ArrowRight') {
        event.preventDefault()
        const nextProvider = providerIDs[Math.min(providerIDs.length - 1, providerIndex + 1)]
        if (nextProvider) {
          setActiveProvider(nextProvider)
        }
        return
      }
      if (event.key === 'ArrowUp') {
        event.preventDefault()
        setActiveModelIndex((current) => Math.max(0, current - 1))
        return
      }
      if (event.key === 'ArrowDown') {
        event.preventDefault()
        setActiveModelIndex((current) => Math.min(Math.max(activeModels.length - 1, 0), current + 1))
        return
      }
      if (event.key === 'Enter') {
        event.preventDefault()
        const option = activeModels[activeModelIndex] ?? activeModels[0]
        if (option) {
          onSelect(option.key)
          setOpen(false)
        }
      }
    }
    document.addEventListener('pointerdown', handlePointerDownOutside)
    document.addEventListener('keydown', handleKeyDown)
    return () => {
      document.removeEventListener('pointerdown', handlePointerDownOutside)
      document.removeEventListener('keydown', handleKeyDown)
    }
  }, [activeModelIndex, activeModels, onSelect, open, providerIDs, resolvedActiveProvider])

  const dropdown = open && position ? createPortal(
    <div
      ref={dropdownRef}
      style={{
        position: 'fixed',
        top: position.top === undefined ? undefined : `${position.top}px`,
        bottom: position.bottom === undefined ? undefined : `${position.bottom}px`,
        left: `${position.left}px`,
        width: `${position.width}px`,
        maxHeight: `${position.maxHeight}px`,
        zIndex: 9999,
      }}
    >
      <div className="overflow-hidden rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)] shadow-xl shadow-black/40" style={{ maxHeight: `${position.maxHeight}px` }}>
        <div className="flex max-h-[inherit] min-h-0 flex-col min-[641px]:grid min-[641px]:grid-cols-[220px_minmax(0,1fr)]">
          <div className="min-w-0 border-b border-[var(--app-border)] bg-[var(--app-surface-subtle)] min-[641px]:border-b-0 min-[641px]:border-r">
            <div className="flex h-10 items-center border-b border-[var(--app-border)] px-3 min-[641px]:h-11 min-[641px]:px-4">
              <span className="text-[11px] font-semibold uppercase tracking-wider text-[var(--app-text-subtle)]">
                Swipe providers
              </span>
            </div>
            <div className="flex max-w-full gap-2 overflow-x-auto overflow-y-hidden p-2 [scrollbar-width:none] [-ms-overflow-style:none] [&::-webkit-scrollbar]:hidden min-[641px]:max-h-[368px] min-[641px]:flex-col min-[641px]:gap-0 min-[641px]:overflow-y-auto min-[641px]:p-0">
              {providers.map(([provider, models]) => {
                const isActive = provider === resolvedActiveProvider
                const hasSelected = models.some((model) => model.key === selectedKey)
                return (
                  <button
                    key={provider}
                    type="button"
                    onMouseEnter={() => setActiveProvider(provider)}
                    onFocus={() => setActiveProvider(provider)}
                    onClick={() => setActiveProvider(provider)}
                    className={`flex max-w-[75vw] shrink-0 items-center justify-between gap-2 rounded-xl border px-3 py-2 text-left text-sm transition min-[641px]:max-w-none min-[641px]:shrink min-[641px]:rounded-none min-[641px]:border-0 min-[641px]:px-4 min-[641px]:py-3 ${
                      isActive
                        ? 'border-[var(--app-border-accent)] bg-[var(--app-surface)] text-[var(--app-text)]'
                        : 'border-[var(--app-border)] text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]'
                    }`}
                  >
                    <div className="flex min-w-0 items-center gap-2">
                      {hasSelected ? <Check size={14} className="shrink-0 text-[var(--app-primary)]" /> : <span className="w-[14px] shrink-0" />}
                      <span className="truncate font-medium">{provider}</span>
                    </div>
                    <span className="shrink-0 text-[11px] text-[var(--app-text-subtle)]">{models.length}</span>
                  </button>
                )
              })}
            </div>
          </div>

          <div className="flex min-h-0 min-w-0 flex-1 flex-col">
            <div className="flex h-10 min-w-0 shrink-0 items-center border-b border-[var(--app-border)] px-3 min-[641px]:h-11 min-[641px]:px-4">
              <div className="truncate text-[11px] font-semibold uppercase tracking-wider text-[var(--app-text-subtle)]">
                {resolvedActiveProvider || 'Models'} models
              </div>
            </div>
            <div className="min-h-0 flex-1 overflow-y-auto py-1">
              {activeModels.length === 0 ? (
                <div className="px-4 py-6 text-sm text-[var(--app-text-muted)]">No models available.</div>
              ) : activeModels.map((option, index) => {
                const isSelected = option.key === selectedKey
                const isActive = index === activeModelIndex
                return (
                  <button
                    key={option.key}
                    type="button"
                    onMouseEnter={() => setActiveModelIndex(index)}
                    onFocus={() => setActiveModelIndex(index)}
                    onClick={() => {
                      onSelect(option.key)
                      setOpen(false)
                    }}
                    className={`flex w-full items-start gap-2 px-3 py-3 text-left text-sm transition min-[641px]:gap-3 min-[641px]:px-4 ${
                      isSelected
                        ? 'bg-[var(--app-surface-subtle)] text-[var(--app-text)]'
                        : isActive
                          ? 'bg-[var(--app-surface-hover)] text-[var(--app-text)]'
                          : 'text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]'
                    }`}
                  >
                    {isSelected ? <Check size={14} className="mt-0.5 shrink-0 text-[var(--app-primary)]" /> : <span className="mt-0.5 w-[14px] shrink-0" />}
                    {option.favorite ? <Star size={12} className="mt-1 shrink-0 text-[var(--app-primary)]" /> : <span className="mt-1 w-[12px] shrink-0" />}
                    <span className="min-w-0 flex-1">
                      <span className="block whitespace-normal break-words font-medium leading-snug text-[var(--app-text)] min-[641px]:truncate">{displayModelName(option.provider, option.model, option.contextMode)}</span>
                      <span className="mt-1 block whitespace-normal break-words text-[11px] leading-snug text-[var(--app-text-subtle)] min-[641px]:truncate">{option.label}</span>
                      {effectiveContextWindow(option.provider, option.model, option.contextMode, option.contextWindow) > 0 ? (
                        <span className="mt-1 block text-[11px] text-[var(--app-text-subtle)]">
                          Context window {formatContextWindow(effectiveContextWindow(option.provider, option.model, option.contextMode, option.contextWindow))}
                        </span>
                      ) : null}
                    </span>
                  </button>
                )
              })}
            </div>
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
        onClick={() => {
          if (disabled) return
          setOpen((prev) => !prev)
        }}
        disabled={disabled}
        title={disabled ? disabledReason : displayLabel}
        className="inline-flex items-center gap-1 text-[11px] font-medium text-[var(--app-text-muted)] hover:text-[var(--app-text)] transition disabled:cursor-not-allowed disabled:opacity-60"
      >
        <Cpu size={13} className="shrink-0 text-[var(--app-text-subtle)]" />
        <span className="max-w-[140px] truncate">{displayLabel}</span>
        <ChevronDown size={12} className={open ? 'rotate-180 transition-transform' : 'transition-transform'} />
      </button>
      {dropdown}
    </div>
  )
}
