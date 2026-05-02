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
}

const DROPDOWN_WIDTH = 640

export function ModelPicker({ options, selectedKey, onSelect, openSignal = 0 }: ModelPickerProps) {
  const [open, setOpen] = useState(false)
  const triggerRef = useRef<HTMLButtonElement | null>(null)
  const dropdownRef = useRef<HTMLDivElement | null>(null)
  const [position, setPosition] = useState<{ top: number; left: number } | null>(null)
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
    if (!triggerRef.current) {
      setPosition(null)
      return
    }
    const rect = triggerRef.current.getBoundingClientRect()
    const maxWidth = Math.min(DROPDOWN_WIDTH, window.innerWidth - 16)
    setPosition({
      top: rect.top - 8,
      left: Math.min(Math.max(8, rect.left), Math.max(8, window.innerWidth - maxWidth - 8)),
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
    setOpen(true)
  }, [openSignal])

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
    document.addEventListener('mousedown', handleClickOutside)
    document.addEventListener('keydown', handleKeyDown)
    return () => {
      document.removeEventListener('mousedown', handleClickOutside)
      document.removeEventListener('keydown', handleKeyDown)
    }
  }, [activeModelIndex, activeModels, onSelect, open, providerIDs, resolvedActiveProvider])

  const dropdown = open && position ? createPortal(
    <div
      ref={dropdownRef}
      style={{
        position: 'fixed',
        bottom: `${window.innerHeight - position.top}px`,
        left: `${position.left}px`,
        width: `${Math.min(DROPDOWN_WIDTH, window.innerWidth - 16)}px`,
        zIndex: 9999,
      }}
    >
      <div className="overflow-hidden rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)] shadow-xl shadow-black/40">
        <div className="grid max-h-[420px] grid-cols-[220px_minmax(0,1fr)]">
          <div className="border-r border-[var(--app-border)] bg-[var(--app-surface-subtle)]">
            <div className="flex h-11 items-center border-b border-[var(--app-border)] px-4">
              <span className="text-[11px] font-semibold uppercase tracking-wider text-[var(--app-text-subtle)]">
                Providers
              </span>
            </div>
            <div className="max-h-[368px] overflow-y-auto py-1">
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
                    className={`flex w-full items-center justify-between gap-2 px-4 py-3 text-left text-sm transition ${
                      isActive
                        ? 'bg-[var(--app-surface)] text-[var(--app-text)]'
                        : 'text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]'
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

          <div className="min-w-0">
            <div className="flex h-11 min-w-0 items-center border-b border-[var(--app-border)] px-4">
              <div className="truncate text-[11px] font-semibold uppercase tracking-wider text-[var(--app-text-subtle)]">
                {resolvedActiveProvider || 'Models'}
              </div>
            </div>
            <div className="max-h-[368px] overflow-y-auto py-1">
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
                    className={`flex w-full items-start gap-3 px-4 py-3 text-left text-sm transition ${
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
                      <span className="block truncate font-medium text-[var(--app-text)]">{displayModelName(option.provider, option.model, option.contextMode)}</span>
                      <span className="mt-1 block truncate text-[11px] text-[var(--app-text-subtle)]">{option.label}</span>
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
        onClick={() => setOpen((prev) => !prev)}
        className="inline-flex items-center gap-1 text-[11px] font-medium text-[var(--app-text-muted)] hover:text-[var(--app-text)] transition"
      >
        <Cpu size={13} className="shrink-0 text-[var(--app-text-subtle)]" />
        <span className="max-w-[140px] truncate">{displayLabel}</span>
        <ChevronDown size={12} className={open ? 'rotate-180 transition-transform' : 'transition-transform'} />
      </button>
      {dropdown}
    </div>
  )
}
