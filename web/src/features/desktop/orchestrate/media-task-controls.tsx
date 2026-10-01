import { useId, useState, type ReactNode } from 'react'

// Inline disclosure avoids clipped floating panels in the scrollable New Task dialog.
// Hover, focus and tap all expose the same help; Escape, blur and Close dismiss it.
export function MediaTaskHelp({ label, children }: { label: string; children: ReactNode }) {
  const id = useId()
  const [open, setOpen] = useState(false)
  return (
    <span className="inline-flex max-w-full flex-col align-middle" onMouseEnter={() => setOpen(true)} onMouseLeave={event => { if (!event.currentTarget.contains(document.activeElement)) setOpen(false) }}
      onBlur={event => { if (!event.currentTarget.contains(event.relatedTarget)) setOpen(false) }}
      onKeyDown={event => { if (event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); setOpen(false) } }}>
      <button type="button" aria-label={label} aria-expanded={open} aria-controls={id} aria-describedby={open ? id : undefined}
        onFocus={() => setOpen(true)} onClick={() => setOpen(true)}
        className="min-h-9 self-start rounded px-2 py-1 text-sm text-slate-400 hover:text-white focus-visible:outline focus-visible:outline-blue-400">ⓘ</button>
      {open && <span id={id} role="note" className="max-w-full rounded border border-slate-700 bg-slate-900 p-2 text-xs leading-relaxed text-slate-300 break-words">
        {children}
        <button type="button" aria-label={`Close ${label}`} onClick={() => setOpen(false)} className="ml-2 rounded px-2 py-1 text-blue-300">Close</button>
      </span>}
    </span>
  )
}

export function MediaTaskSelect({ label, value, values, onChange, suffix = '' }: {
  label: string; value: string | number; values: readonly (string | number)[]
  onChange: (value: string) => void; suffix?: string
}) {
  return <label className="min-w-0 space-y-1 text-xs text-slate-400">
    <span className="block">{label}</span>
    {values.length > 0 ? <select aria-label={label} value={value} onChange={event => onChange(event.target.value)}
      className="min-h-9 w-full min-w-0 rounded border border-slate-700 bg-slate-900 px-2 py-1.5 text-sm text-white focus-visible:outline focus-visible:outline-blue-400">
      {values.map(option => <option key={option} value={option}>{option}{suffix}</option>)}
    </select> : <span className="block py-2 text-xs text-slate-400">Not configurable</span>}
  </label>
}

export function MediaTaskDefault({ isDefault, disabled, saving, onSave }: {
  isDefault: boolean; disabled?: boolean; saving?: boolean; onSave: () => void
}) {
  return <div className="flex flex-wrap items-center justify-between gap-2 text-xs">
    <span className={isDefault ? 'text-slate-400' : 'text-amber-300'}>{isDefault ? 'Default' : 'This task only'}</span>
    {!isDefault && <button type="button" disabled={disabled} onClick={onSave}
      className="min-h-9 rounded px-2 text-blue-300 hover:text-white disabled:opacity-50">{saving ? 'Saving…' : 'Set default'}</button>}
  </div>
}

export function MediaTaskScenes({ value, onChange }: { value: string; onChange: (value: string) => void }) {
  return <details open={value.trim() ? true : undefined} className="text-xs text-slate-300">
    <summary className="cursor-pointer rounded py-2 focus-visible:outline focus-visible:outline-blue-400">Scenes (optional){value.trim() ? ' · added' : ''}</summary>
    <label className="block">
      <span className="sr-only">Scene prompts</span>
      <textarea id="video-scene-prompts" aria-label="Scene prompts" value={value} onChange={event => onChange(event.target.value)} rows={3}
        className="w-full rounded border border-slate-700 bg-slate-900 p-2 text-sm text-slate-100" placeholder="One scene per line · 2–8 scenes" />
    </label>
    <MediaTaskHelp label="About scenes">Scenes generate sequentially and assemble in order. Select one clip. Each scene is billed separately; generated scene audio is retained. Timeline editing and audio mixing are available in Video Studio.</MediaTaskHelp>
  </details>
}

export function MediaTaskCost({ total, approximate, details, scenes = false }: {
  total?: number; approximate?: boolean; details: string; scenes?: boolean
}) {
  return <div className="flex flex-wrap items-start gap-1 text-xs text-slate-300">
    <span className="py-1">Estimated cost: <strong className="text-white">{total === undefined ? 'Unknown' : `${approximate ? '≈' : ''}$${total.toFixed(2)}`}</strong>{scenes ? ' / scene' : ''}</span>
    <MediaTaskHelp label="Cost details">{details} {scenes && 'Each scene is charged separately; this is not the assembled total.'}</MediaTaskHelp>
  </div>
}
