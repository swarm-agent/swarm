import React from 'react'
import { Archive, GitBranch, Folder, ListChecks, Plus, Search, Trash2 } from 'lucide-react'

export type TaskStatusFilter = 'all' | 'running' | 'needs_review' | 'queued' | 'completed'
export type TaskSourceFilter = 'all' | 'worker'

export function TaskListHeader({ title, branch, workspaceCount, orchestratorState, onNewTask }: {
  title: string
  branch?: string
  workspaceCount: number
  orchestratorState: 'active' | 'inactive' | 'unknown'
  onNewTask: () => void
}) {
  return <header className="swarm-task-list-header px-4 pt-4 pb-3 space-y-2">
    <div className="flex flex-wrap items-center justify-between gap-3">
      <div className="flex flex-wrap items-center gap-2 min-w-0">
        <h1 className="min-w-0 max-w-full break-words text-base font-bold tracking-tight text-white">{title}</h1>
        <span role="status" className="flex items-center gap-1.5 rounded border border-slate-800 px-2 py-0.5 text-[11px] text-slate-400">
          <span aria-hidden="true" className={`h-1.5 w-1.5 rounded-full ${orchestratorState === 'active' ? 'bg-emerald-400' : 'bg-slate-500'}`} />
          Orchestrator {orchestratorState === 'unknown' ? 'state unavailable' : orchestratorState}
        </span>
      </div>
      <button type="button" onClick={onNewTask} className="swarm-task-list-new-task flex shrink-0 items-center gap-1.5 rounded font-medium text-xs px-3 py-1.5">
        <Plus size={13} aria-hidden="true" />New task
      </button>
    </div>
    <div className="flex flex-wrap items-center gap-4 text-xs font-mono text-slate-400">
      <span className="flex items-center gap-1.5 min-w-0"><GitBranch size={12} aria-hidden="true" /><span className="truncate">{branch || 'Branch unavailable'}</span></span>
      <span className="flex items-center gap-1.5 shrink-0"><Folder size={12} aria-hidden="true" />{workspaceCount} workspaces</span>
    </div>
  </header>
}

export function TaskListToolbar({ search, onSearch, source, onSource, status, onStatus, counts, total, selected, busy, integrationEligible = 0, integrationDisabled = false, onIntegrate, onSelectAll, onClear, onArchive, onDelete, onArchived, archivedRef }: {
  search: string; onSearch: (value: string) => void
  source: TaskSourceFilter; onSource: (value: TaskSourceFilter) => void
  status: TaskStatusFilter; onStatus: (value: TaskStatusFilter) => void
  counts: Record<TaskStatusFilter, number>
  total: number; selected: number; busy: boolean
  integrationEligible?: number; integrationDisabled?: boolean; onIntegrate?: () => void
  onSelectAll: () => void; onClear: () => void; onArchive: () => void; onDelete: () => void; onArchived: () => void
  archivedRef?: React.Ref<HTMLButtonElement>
}) {
  const chips = [
    { value: 'all', label: 'All', dot: '' },
    { value: 'running', label: 'Running', dot: 'bg-rose-400' },
    { value: 'needs_review', label: 'Review', dot: 'bg-amber-400' },
    { value: 'queued', label: 'Queued', dot: 'bg-slate-500' },
    { value: 'completed', label: 'Done', dot: 'bg-emerald-400' },
  ] as const
  const outline = 'swarm-outline-action flex items-center gap-1.5 rounded px-2.5 py-1 text-xs'
  return <div className="swarm-task-list-toolbar shrink-0 min-w-0 px-4">
    <div className="swarm-task-search-row flex flex-wrap items-center gap-3 py-2">
      <div className="relative flex-1 min-w-0">
        <Search size={13} aria-hidden="true" className="absolute left-3 top-2 text-slate-500" />
        <input type="search" value={search} onChange={event => onSearch(event.target.value)} placeholder="Search tasks" aria-label="Search tasks" className="w-full bg-transparent border border-slate-800 rounded pl-8 pr-3 py-1.5 text-xs text-white placeholder-slate-500 focus:outline-none focus:border-[var(--swarm-accent)]" />
      </div>
      <div role="group" aria-label="Task scope" className="flex shrink-0 rounded border border-slate-800 p-0.5">
        {([{ value: 'all', label: 'All' }, { value: 'worker', label: 'Workers' }] as const).map(item => <button key={item.value} type="button" data-testid={item.value === 'all' ? 'filter-all-tasks' : 'filter-worker-tasks'} aria-pressed={source === item.value} onClick={() => onSource(item.value)} className="swarm-outline-action swarm-task-source-filter rounded px-2.5 py-1 text-[11px]">{item.label}</button>)}
      </div>
    </div>
    <div className="flex flex-wrap items-center gap-1.5 py-2 border-b-[0.5px] border-slate-800" role="group" aria-label="Task status filter">
      {chips.map(chip => <button key={chip.value} type="button" aria-pressed={status === chip.value} onClick={() => onStatus(chip.value)} className={`flex items-center gap-1.5 rounded border px-2 py-1 text-[11px] ${status === chip.value ? 'border-slate-600 text-white' : 'border-transparent text-slate-400 hover:text-white'}`}>
        {chip.dot && <span aria-hidden="true" className={`h-1.5 w-1.5 rounded-full ${chip.dot}`} />}{chip.label}<span className={`font-mono ${counts[chip.value] === 0 ? 'text-slate-600' : 'text-slate-400'}`}>{counts[chip.value]}</span>
      </button>)}
      <button type="button" ref={archivedRef} onClick={onArchived} className="ml-auto flex items-center gap-1.5 px-2 py-1 text-xs text-slate-400 hover:text-white"><Archive size={13} aria-hidden="true" />Archived</button>
    </div>
    <div aria-label="Task management" className="swarm-task-management min-h-11 flex flex-wrap items-center justify-between gap-2 py-2">
      <div className="flex flex-wrap items-center gap-3 min-w-0">
        <button type="button" disabled={busy || total === 0} onClick={onSelectAll} className={outline}><ListChecks size={13} aria-hidden="true" />Select all</button>
        <span className="text-[11px] text-slate-400" role="status">{selected > 0 ? `${selected} of ${total} selected` : `${total} tasks`}</span>
      </div>
      {selected > 0 && <div className="flex flex-wrap items-center gap-2 min-w-0">
        <button type="button" disabled={busy} onClick={onClear} className="px-2 py-1 text-xs text-slate-400 hover:text-white disabled:opacity-40">Clear</button>
        <button type="button" disabled={busy || integrationDisabled || integrationEligible === 0} onClick={onIntegrate} className={outline}>Integrate selected ({integrationEligible})</button>
        <button type="button" disabled={busy} onClick={onArchive} className={outline}>Archive</button>
        <button type="button" disabled={busy} onClick={onDelete} className="flex items-center gap-1.5 rounded border border-rose-500/50 px-2.5 py-1 text-xs text-rose-400 hover:border-rose-400 disabled:opacity-40"><Trash2 size={13} aria-hidden="true" />Delete</button>
      </div>}
    </div>
  </div>
}
