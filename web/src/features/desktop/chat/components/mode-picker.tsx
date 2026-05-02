import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { ChevronDown, Check, ChevronsUp, NotepadText } from 'lucide-react'

interface ModePickerProps {
  mode: 'plan' | 'auto'
  onSelect: (mode: 'plan' | 'auto') => void
}

const DROPDOWN_WIDTH = 180

export function ModePicker({ mode, onSelect }: ModePickerProps) {
  const [open, setOpen] = useState(false)
  const triggerRef = useRef<HTMLButtonElement | null>(null)
  const dropdownRef = useRef<HTMLDivElement | null>(null)
  const [position, setPosition] = useState<{ top: number; left: number } | null>(null)

  const modes: Array<'plan' | 'auto'> = ['plan', 'auto']
  const ModeIcon = mode === 'plan' ? NotepadText : ChevronsUp

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

  const handleSelect = useCallback((m: 'plan' | 'auto') => {
    onSelect(m)
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
              Select mode
            </span>
          </div>
          <div className="py-1">
            {modes.map((m) => {
              const isSelected = m === mode
              const OptionIcon = m === 'plan' ? NotepadText : ChevronsUp
              return (
                <button
                  key={m}
                  type="button"
                  onClick={() => handleSelect(m)}
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
                  <OptionIcon size={14} className="shrink-0 text-[var(--app-text-subtle)]" />
                  <span className="truncate uppercase tracking-wider text-xs font-medium">{m}</span>
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
        className="inline-flex items-center gap-1 text-[11px] font-medium text-[var(--app-text-muted)] hover:text-[var(--app-text)] transition"
      >
        <ModeIcon size={13} className="shrink-0 text-[var(--app-text-subtle)]" />
        <span className="uppercase tracking-wider font-semibold text-[var(--app-primary)]">{mode}</span>
        <ChevronDown size={12} className={`text-[var(--app-text-subtle)] ${open ? 'rotate-180 transition-transform' : 'transition-transform'}`} />
      </button>
      {dropdown}
    </div>
  )
}
