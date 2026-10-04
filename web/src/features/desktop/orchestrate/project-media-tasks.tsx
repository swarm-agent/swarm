import { useEffect, useMemo, useRef, useState } from 'react'
import { subscribeDesktopSessionReset } from '../../../app/api'
import { DesignMediaTasks, useProjectDesigns } from '../tools/media-library/design-media'
import { desktopDesigns } from '../runtime/desktop-design-runtime'
import { desktopProjects } from '../runtime/desktop-projects'
import { archiveProjectTask, projectTaskArchiveQueue } from '../runtime/project-task-archive'
import type { MediaLibraryItem } from '../tools/media-library/types'
import type { RunningTask } from './orchestrate-types'
import { isCreativeMediaTask, MediaTaskThreads, type MediaTaskActions } from './media-task-card'
import { mediaSelectionCards, type MediaSelectionCard } from './media-selection'

// Project runtimes retain ownership of records and archive receipts. Selection is
// navigation state only; never remove cards optimistically or delete artifacts.
export function ProjectMediaTasks({ projectId, tasks, actions, onPreviewDesign, onLibrary, loading, error, onRetry,
  archivedTasks, archivedLoading, archivedError, onLoadArchived, active = true, selectionScope = '',
}: {
  projectId: string; tasks: readonly RunningTask[]; actions: (task: RunningTask) => MediaTaskActions
  onPreviewDesign: (item: MediaLibraryItem) => void; onLibrary: () => void
  loading?: boolean; error?: string; onRetry: () => void
  archivedTasks: readonly RunningTask[]; archivedLoading: boolean; archivedError: string; onLoadArchived: () => void
  active?: boolean; selectionScope?: string
}) {
  const [archived, setArchived] = useState(false)
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [busy, setBusy] = useState(false)
  const [message, setMessage] = useState('')
  const [failures, setFailures] = useState<Record<string, string>>({})
  const pending = useRef(false)
  const epoch = useRef(0)
  const intents = useRef(new Map<string, { identity: string; key: string }>())
  const designs = useProjectDesigns(projectId)
  const cards = useMemo(() => mediaSelectionCards(tasks, designs.data?.designs ?? []), [tasks, designs.data])
  const records = useMemo(() => new Map(cards.flatMap(card => card.records.map(record => [record.key, record] as const))), [cards])
  const selectedRecords = [...selected].flatMap(key => records.has(key) ? [records.get(key)!] : [])
  const selectedCards = cards.filter(card => card.records.some(record => selected.has(record.key))).length
  const media = tasks.filter(isCreativeMediaTask)
  const archivedMedia = archivedTasks.filter(isCreativeMediaTask)
  useEffect(() => { setSelected(new Set()); setMessage(''); setFailures({}) }, [active, selectionScope, archived, projectId])
  useEffect(() => {
    setSelected(previous => [...previous].every(key => records.has(key)) ? previous : new Set([...previous].filter(key => records.has(key))))
  }, [records])
  useEffect(() => {
    const unsubscribe = subscribeDesktopSessionReset(() => { epoch.current++; pending.current = false; setBusy(false); setSelected(new Set()); setFailures({}); setMessage(''); intents.current.clear() })
    return () => { epoch.current++; unsubscribe() }
  }, [projectId])

  const archiveSelected = async () => {
    if (pending.current || !active || !selectedRecords.length) return
    pending.current = true; setBusy(true); setMessage(''); setFailures({})
    const attempt = epoch.current
    const current = () => attempt === epoch.current
    const errors: Record<string, string> = {}; const succeeded = new Set<string>()
    await Promise.all(selectedRecords.map(async record => {
      try {
        if (!current()) return
        if (record.kind === 'task') {
          await projectTaskArchiveQueue.run(JSON.stringify(['media', projectId, record.key]), async () => {
            const receipt = await archiveProjectTask(projectId, record.task, current)
            if (receipt) desktopProjects.archiveReceipt(projectId, receipt)
          })
        } else {
          const identity = JSON.stringify([record.reference, record.version])
          let intent = intents.current.get(record.key)
          if (intent?.identity !== identity) { intent = { identity, key: crypto.randomUUID() }; intents.current.set(record.key, intent) }
          await desktopDesigns.archive(record.session, record.reference, record.version, true, intent.key, current)
          intents.current.delete(record.key)
        }
        if (current()) succeeded.add(record.key)
      } catch (cause) {
        if (current()) errors[record.key] = `${record.title}: ${cause instanceof Error ? cause.message : String(cause)}`
      }
    }))
    if (!current()) return
    setSelected(previous => new Set([...previous].filter(key => !succeeded.has(key))))
    setFailures(errors)
    setMessage(`${succeeded.size} records archived, ${Object.keys(errors).length} failed.${Object.keys(errors).length ? ' Failed records remain selected; refresh and retry Archive.' : ''}`)
    pending.current = false; setBusy(false)
    onLoadArchived()
  }
  const control = (card: MediaSelectionCard | undefined) => {
    if (!card) return null
    const count = card.records.filter(record => selected.has(record.key)).length
    return <div className="px-3 py-2 text-xs text-slate-400">
      <label className="flex items-center gap-2"><input type="checkbox" aria-label={`Select media card: ${card.title}`} checked={count > 0 && count === card.records.length}
        ref={input => { if (input) input.indeterminate = count > 0 && count < card.records.length }} disabled={busy || !active || !card.records.length}
        onChange={event => { const checked = event.target.checked; setSelected(previous => { const next = new Set(previous); card.records.forEach(record => checked ? next.add(record.key) : next.delete(record.key)); return next }) }} />
        {count ? `${count} selected · ` : ''}{card.records.length} archiveable {card.records.length === 1 ? 'record' : 'records'}{card.unavailable > 0 ? ` · ${card.unavailable} unfinished design candidates cannot be archived yet` : ''}
      </label>
      {card.records.map(record => failures[record.key] && <p role="alert" key={record.key}>{failures[record.key]}</p>)}
    </div>
  }
  const buttonClass = 'swarm-outline-action rounded border border-slate-700 px-2.5 py-1 text-xs disabled:opacity-40'
  return <>
    <header className="flex items-center justify-between gap-2"><h2>Project media</h2><button type="button" onClick={onLibrary}>Historical library</button></header>
    <div aria-label="Media management" className="flex flex-wrap items-center gap-3 text-xs">
      <button className={buttonClass} type="button" disabled={busy || !records.size} onClick={() => setSelected(new Set(records.keys()))}>Select all</button>
      <span role="status">{selectedCards ? `${selectedCards} of ${cards.length} cards selected · ${selectedRecords.length} records` : `${cards.length} media cards`}</span>
      <button className={buttonClass} type="button" disabled={busy || !selected.size} onClick={() => setSelected(new Set())}>Clear selection</button>
      <button className={buttonClass} type="button" disabled={busy || !selectedRecords.length} aria-busy={busy} onClick={() => void archiveSelected()}>{busy ? 'Archiving…' : 'Archive selected'}</button>
    </div>
    <details className="text-xs text-slate-400"><summary>Selection scope · active cards only</summary><p>Counts are loaded active cards. Select all includes only their currently shown turns and archiveable records, never archived media or later arrivals. Designs archive the selected artifact’s revision history; nothing is deleted.</p></details>
    {message && <p role="status">{message}</p>}
    {Object.keys(failures).length > 0 && <button type="button" disabled={busy} onClick={() => { onRetry(); void designs.refresh() }}>Refresh media for retry</button>}
    {loading && !media.length && <p role="status">Loading media…</p>}
    {error && <p role="alert">{error} <button type="button" onClick={onRetry}>Retry media</button></p>}
    <DesignMediaTasks projectId={projectId} onPreview={onPreviewDesign} selectionControl={id => control(cards.find(card => card.id === id))} archiveDisabled />
    <MediaTaskThreads tasks={media} visibleTaskIds={new Set(media.map(task => task.id))} actions={task => ({ ...actions(task), onArchive: undefined, onDelete: undefined })} selectionControl={turns => control(cards.find(card => card.records.some(record => record.kind === 'task' && record.task.id === turns[0].id)))} />
    {!loading && !error && !media.length && <p className="text-xs text-slate-400">No image, video or audio generations yet.</p>}
    <button type="button" aria-expanded={archived} onClick={() => { if (!archived) onLoadArchived(); setArchived(!archived) }}>Archived media</button>
    {archived && <section aria-label="Archived media" className="space-y-3">
      <DesignMediaTasks projectId={projectId} archived onPreview={onPreviewDesign} />
      {archivedLoading && <p role="status">Loading archived media…</p>}
      {archivedError && <p role="alert">{archivedError} <button type="button" onClick={onLoadArchived}>Retry archived media</button></p>}
      <MediaTaskThreads tasks={archivedMedia} visibleTaskIds={new Set(archivedMedia.map(task => task.id))} actions={task => ({ onPreview: actions(task).onPreview })} />
    </section>}
  </>
}
