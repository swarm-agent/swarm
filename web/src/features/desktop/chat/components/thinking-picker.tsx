import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { ChevronDown, Check } from 'lucide-react'

interface ThinkingPickerProps {
  value: string
  options: string[]
  onSelect: (value: string) => void
  label?: string
  tagsEnabled?: boolean
  onToggleTags?: (enabled: boolean) => void
  tagsBusy?: boolean
}

const DROPDOWN_WIDTH = 170

function normalizeThinkingValue(value: string): string {
  return value.trim() === '' ? 'off' : value
}

export function ThinkingPicker({
  value,
  options,
  onSelect,
  label = 'Thinking',
  tagsEnabled,
  onToggleTags,
  tagsBusy = false,
}: ThinkingPickerProps) {
  const [open, setOpen] = useState(false)
  const triggerRef = useRef<HTMLButtonElement | null>(null)
  const dropdownRef = useRef<HTMLDivElement | null>(null)
  const [position, setPosition] = useState<{ top: number; left: number } | null>(null)

  const normalizedValue = normalizeThinkingValue(value)
  const displayLabel = normalizedValue
  const showTagsToggle = typeof tagsEnabled === 'boolean' && typeof onToggleTags === 'function'

  useLayoutEffect(() => {
    if (!open || !triggerRef.current) {
      setPosition(null)
      return
    }
    const rect = triggerRef.current.getBoundingClientRect()
    setPosition({
      top: rect.top - 8,
      left: Math.max(4, rect.left),
    })
  }, [open])

  useEffect(() => {
    if (!open) return
    function reposition() {
      if (!triggerRef.current) return
      const rect = triggerRef.current.getBoundingClientRect()
      setPosition({
        top: rect.top - 8,
        left: Math.max(4, rect.left),
      })
    }
    window.addEventListener('scroll', reposition, true)
    window.addEventListener('resize', reposition)
    return () => {
      window.removeEventListener('scroll', reposition, true)
      window.removeEventListener('resize', reposition)
    }
  }, [open])

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

  const handleSelect = useCallback((v: string) => {
    onSelect(v)
    setOpen(false)
  }, [onSelect])

  const dropdown = open && position ? createPortal(
    <div
      ref={dropdownRef}
      style={{
        position: 'fixed',
        bottom: `${window.innerHeight - position.top}px`,
        left: `${position.left}px`,
        width: `${DROPDOWN_WIDTH}px`,
        zIndex: 9999,
      }}
    >
      <div className="overflow-hidden rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] shadow-xl shadow-black/40">
        <div className="flex flex-col">
          <div className="border-b border-[var(--app-border)] px-3 py-2">
            <span className="text-[11px] font-semibold uppercase tracking-wider text-[var(--app-text-subtle)]">
              {label}
            </span>
          </div>
          <div className="py-1">
            {options.map((option) => {
              const normalizedOption = normalizeThinkingValue(option)
              const isSelected = normalizedOption === normalizedValue
              return (
                <button
                  key={normalizedOption}
                  type="button"
                  onClick={() => handleSelect(normalizedOption)}
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
                  <span className="truncate">{normalizedOption}</span>
                </button>
              )
            })}
          </div>
          {showTagsToggle ? (
            <>
              <div className="border-t border-[var(--app-border)]" />
              <button
                type="button"
                onClick={() => onToggleTags?.(!tagsEnabled)}
                disabled={tagsBusy}
                className="flex w-full items-center justify-between gap-3 px-3 py-2 text-left text-sm text-[var(--app-text-muted)] transition hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)] disabled:cursor-not-allowed disabled:opacity-60"
              >
                <span className="truncate">Thinking tags</span>
                <span className="shrink-0 text-[11px] font-medium uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">
                  {tagsBusy ? 'Saving…' : tagsEnabled ? 'On' : 'Off'}
                </span>
              </button>
            </>
          ) : null}
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
        <span className="text-[var(--app-text-subtle)]">{label}</span>
        <span className="truncate">{displayLabel}</span>
        <ChevronDown size={12} className={open ? 'rotate-180 transition-transform' : 'transition-transform'} />
      </button>
      {dropdown}
    </div>
  )
}
