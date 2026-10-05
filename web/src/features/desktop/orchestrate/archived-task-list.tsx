import { useEffect, useState } from 'react'
import type { RunningTask } from './orchestrate-types'

// Selection is local UI state; restoration is owned by the project runtime.
export function ArchivedTaskList({ tasks, busy, errors, onUnarchive }: {
  tasks: readonly RunningTask[]; busy: boolean; errors: Record<string, string>
  onUnarchive: (tasks: RunningTask[]) => void
}) {
  const [selected, setSelected] = useState<Set<string>>(new Set())
  useEffect(() => { setSelected(previous => new Set([...previous].filter(id => tasks.some(task => task.id === id)))) }, [tasks])
  return <ArchivedTaskControls tasks={tasks} busy={busy} errors={errors} onUnarchive={onUnarchive} selected={selected} setSelected={setSelected} />
}

export function ArchivedTaskControls({ tasks, busy, errors, onUnarchive, selected, setSelected }: {
  tasks: readonly RunningTask[]; busy: boolean; errors: Record<string, string>
  onUnarchive: (tasks: RunningTask[]) => void; selected: Set<string>
  setSelected: (update: Set<string> | ((previous: Set<string>) => Set<string>)) => void
}) {
  return <>
    <button type="button" disabled={busy || !tasks.length} onClick={() => setSelected(new Set(tasks.map(task => task.id)))}>Select all archived tasks</button>
    <button type="button" disabled={busy || !selected.size} onClick={() => onUnarchive(tasks.filter(task => selected.has(task.id)))}>Unarchive selected</button>
    <ul className="overflow-y-auto min-h-0 space-y-2">{tasks.map(row => <li key={row.id} className="p-3 rounded border border-slate-700">
      <label><input type="checkbox" aria-label={`Select archived task: ${row.title}`} disabled={busy} checked={selected.has(row.id)} onChange={event => { const checked = event.target.checked; setSelected(previous => { const next = new Set(previous); if (checked) next.add(row.id); else next.delete(row.id); return next }) }} />{row.title}</label>
      <span className="block text-xs text-slate-400">{row.status} · {row.workerName || 'Task'}</span>
      <button type="button" disabled={busy} aria-label={`Unarchive task: ${row.title}`} onClick={() => onUnarchive([row])}>Unarchive</button>
      {errors[row.id] && <p role="alert">{errors[row.id]}</p>}
    </li>)}</ul>
  </>
}
